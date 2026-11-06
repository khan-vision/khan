package crud

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5/pgconn"
	khandb "github.com/khan-vision/khan/db"
	"github.com/khan-vision/khan/lookup"
	"github.com/khan-vision/khan/query"
	"github.com/khan-vision/khan/validate"
	"gorm.io/gorm"
)

func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	os.Exit(m.Run())
}

// base — DB-гүй хүчинтэй тохиргоо.
func base() *Resource[map[string]any] {
	return &Resource[map[string]any]{
		Name: "test.thing", Label: "Зүйл", Endpoint: "/things", Table: "test.thing",
		Fields: []Field{
			{Name: "name", Label: "Нэр", Kind: validate.String, Rules: validate.Rules{Required: true}},
			{Name: "qty", Kind: validate.Int, Rules: validate.Rules{Nullable: true}},
		},
		Can: func(c fiber.Ctx, a Action) bool { return true },
		DB:  func() *gorm.DB { return nil },
	}
}

func TestMountConfigErrors(t *testing.T) {
	type mod func(r *Resource[map[string]any])
	person := &lookup.Spec{Table: "test.person", Label: []string{"name"}}
	cases := map[string]mod{
		"nil Can":                func(r *Resource[map[string]any]) { r.Can = nil },
		"nil DB":                 func(r *Resource[map[string]any]) { r.DB = nil },
		"empty Name":             func(r *Resource[map[string]any]) { r.Name = "" },
		"Name upper":             func(r *Resource[map[string]any]) { r.Name = "Test.Thing" },
		"Name space":             func(r *Resource[map[string]any]) { r.Name = "test thing" },
		"Table без schema":       func(r *Resource[map[string]any]) { r.Table = "thing" },
		"Table injection":        func(r *Resource[map[string]any]) { r.Table = "test.thing; drop table x" },
		"Table upper":            func(r *Resource[map[string]any]) { r.Table = "Test.Thing" },
		"Table 3 parts":          func(r *Resource[map[string]any]) { r.Table = "a.b.c" },
		"bad Alias":              func(r *Resource[map[string]any]) { r.Alias = "t x" },
		"bad ID column":          func(r *Resource[map[string]any]) { r.ID.Column = "id;" },
		"bad ID kind":            func(r *Resource[map[string]any]) { r.ID.Kind = "bigint" },
		"IDInt + Generate":       func(r *Resource[map[string]any]) { r.ID = IDSpec{Kind: IDInt, Generate: func() string { return "1" }} },
		"no fields":              func(r *Resource[map[string]any]) { r.Fields = nil },
		"field empty name":       func(r *Resource[map[string]any]) { r.Fields[0].Name = "" },
		"field name injection":   func(r *Resource[map[string]any]) { r.Fields[0].Name = "a-b" },
		"field name camel":       func(r *Resource[map[string]any]) { r.Fields[0].Name = "roomName" },
		"field column injection": func(r *Resource[map[string]any]) { r.Fields[0].Column = `x"; drop` },
		"duplicate name":         func(r *Resource[map[string]any]) { r.Fields[1].Name = "name" },
		"duplicate column":       func(r *Resource[map[string]any]) { r.Fields[1].Column = "name" },
		"bad kind":               func(r *Resource[map[string]any]) { r.Fields[0].Kind = "blob" },
		"empty kind":             func(r *Resource[map[string]any]) { r.Fields[0].Kind = "" },
		"bad rules":              func(r *Resource[map[string]any]) { r.Fields[0].Rules.MinLen = 10; r.Fields[0].Rules.MaxLen = 2 },
		"enum без list":          func(r *Resource[map[string]any]) { r.Fields[0].Kind = validate.Enum },
		"default invalid":        func(r *Resource[map[string]any]) { r.Fields[1].Default = "abc" },
		"default password": func(r *Resource[map[string]any]) {
			r.Fields[0].Kind = validate.Password
			r.Fields[0].Default = "x12345678"
		},
		"unique json":  func(r *Resource[map[string]any]) { r.Fields[0].Kind = validate.JSON; r.Fields[0].Unique = true },
		"ref on money": func(r *Resource[map[string]any]) { r.Fields[1].Kind = validate.Money; r.Fields[1].Ref = person },
		"ref kind mismatch": func(r *Resource[map[string]any]) {
			r.Fields[1].Ref = &lookup.Spec{Table: "t.p", Label: []string{"n"}, Kind: validate.UUID}
		},
		"ref bad spec":           func(r *Resource[map[string]any]) { r.Fields[1].Ref = &lookup.Spec{Table: "bad;", Label: []string{"n"}} },
		"password без ListQuery": func(r *Resource[map[string]any]) { r.Fields[1].Kind = validate.Password },
		"bad Version":            func(r *Resource[map[string]any]) { r.Version = "ver sion" },
		"child bad name":         func(r *Resource[map[string]any]) { r.Children = []Child{childOK("Bad-Name")} },
		"child clash with field": func(r *Resource[map[string]any]) { r.Children = []Child{childOK("name")} },
		"child duplicate":        func(r *Resource[map[string]any]) { r.Children = []Child{childOK("lines"), childOK("lines")} },
		"child bad table":        func(r *Resource[map[string]any]) { c := childOK("lines"); c.Table = "lines"; r.Children = []Child{c} },
		"child bad fk":           func(r *Resource[map[string]any]) { c := childOK("lines"); c.FK = "x y"; r.Children = []Child{c} },
		"child no fields":        func(r *Resource[map[string]any]) { c := childOK("lines"); c.Fields = nil; r.Children = []Child{c} },
		"child int id generate": func(r *Resource[map[string]any]) {
			c := childOK("lines")
			c.ID = IDSpec{Kind: IDInt, Generate: newUUID}
			r.Children = []Child{c}
		},
		"child bad order": func(r *Resource[map[string]any]) { c := childOK("lines"); c.Order = "a desc"; r.Children = []Child{c} },
		"child unique": func(r *Resource[map[string]any]) {
			c := childOK("lines")
			c.Fields[0].Unique = true
			r.Children = []Child{c}
		},
		"child field fk": func(r *Resource[map[string]any]) {
			c := childOK("lines")
			c.Fields[0].Name = "thing_id"
			r.Children = []Child{c}
		},
		"child protected": func(r *Resource[map[string]any]) {
			c := childOK("lines")
			c.Fields[0].Name = "org_id"
			r.Children = []Child{c}
		},
		"child ref bad": func(r *Resource[map[string]any]) {
			c := childOK("lines")
			c.Fields[0].Ref = &lookup.Spec{Table: "x"}
			r.Children = []Child{c}
		},
		"bulk table mismatch":      func(r *Resource[map[string]any]) { r.Bulk = &query.BulkSpec{Table: "test.other", Fields: bulkQty()} },
		"bulk id mismatch":         func(r *Resource[map[string]any]) { r.Bulk = &query.BulkSpec{IDCol: "uid", Fields: bulkQty()} },
		"bulk softdelete mismatch": func(r *Resource[map[string]any]) { r.Bulk = &query.BulkSpec{SoftDelete: true, Fields: bulkQty()} },
		"bulk panics → error": func(r *Resource[map[string]any]) {
			r.Bulk = &query.BulkSpec{Fields: map[string]query.BulkField{"x": {Col: "org_id", Kind: query.KindText}}}
		},
		"bulk ref field": func(r *Resource[map[string]any]) {
			r.Fields[1].Kind, r.Fields[1].Ref = validate.UUID, person
			r.Bulk = &query.BulkSpec{Fields: map[string]query.BulkField{"qty": {Col: "qty", Kind: query.KindUUID}}}
		},
		"bulk unique field": func(r *Resource[map[string]any]) {
			r.Fields[0].Unique = true
			r.Bulk = &query.BulkSpec{Fields: map[string]query.BulkField{"name": {Col: "name", Kind: query.KindText}}}
		},
		"bulk version": func(r *Resource[map[string]any]) {
			r.Version = "version"
			r.Bulk = &query.BulkSpec{Fields: map[string]query.BulkField{"v": {Col: "version", Kind: query.KindNumber}}}
		},
		"export panics → error": func(r *Resource[map[string]any]) { r.Export = &query.ExportSpec{} },
	}
	for name, m := range cases {
		t.Run(name, func(t *testing.T) {
			r := base()
			m(r)
			app := fiber.New()
			var err error
			func() {
				defer func() {
					if rec := recover(); rec != nil {
						t.Fatalf("Mount panic хийсэн: %v", rec)
					}
				}()
				err = r.Mount(app.Group("/x"))
			}()
			if err == nil {
				t.Fatal("Mount алдаа буцаах ёстой")
			}
			if len(app.GetRoutes()) > 0 {
				for _, rt := range app.GetRoutes() {
					if strings.HasPrefix(rt.Path, "/x/") {
						t.Fatalf("алдаатай тохиргоонд route бүртгэгдсэн: %s", rt.Path)
					}
				}
			}
		})
	}
}

func childOK(name string) Child {
	return Child{Name: name, Table: "test.line", FK: "thing_id",
		Fields: []Field{{Name: "qty", Kind: validate.Int, Rules: validate.Rules{Required: true}}}}
}

func bulkQty() map[string]query.BulkField {
	return map[string]query.BulkField{"qty": {Col: "qty", Kind: query.KindNumber, Nullable: true}}
}

func TestMountProtectedNames(t *testing.T) {
	for _, p := range []string{"id", "org_id", "created_at", "updated_at", "deleted_at", "created_by", "updated_by"} {
		for _, byCol := range []bool{false, true} {
			r := base()
			if byCol {
				r.Fields[1].Column = p
			} else {
				r.Fields[1].Name = p
			}
			if err := r.Mount(fiber.New().Group("/x")); err == nil || !strings.Contains(err.Error(), "хамгаалагдсан") {
				t.Errorf("%s (column=%v): %v", p, byCol, err)
			}
		}
	}
	// version багана Field болж чадахгүй, custom id багана ч
	r := base()
	r.Version = "rev"
	r.Fields[1].Name = "rev"
	if err := r.Mount(fiber.New().Group("/x")); err == nil {
		t.Error("version багана Field болсон")
	}
	r = base()
	r.ID.Column = "uid"
	r.Fields[1].Column = "uid"
	if err := r.Mount(fiber.New().Group("/x")); err == nil {
		t.Error("id багана Field болсон")
	}
}

func TestMountOK(t *testing.T) {
	r := base()
	r.Version = "version"
	r.Events = true
	r.Children = []Child{childOK("lines")}
	r.Bulk = &query.BulkSpec{AllowDelete: true, Fields: bulkQty()}
	r.Export = &query.ExportSpec{Columns: []query.ExportColumn{{Key: "name", Header: "Нэр", Expr: "t.name"}}}
	if err := r.Check(); err != nil {
		t.Fatal(err)
	}
	app := fiber.New()
	if err := r.Mount(app.Group("/ok")); err != nil {
		t.Fatal(err)
	}
	if err := r.Mount(nil); err == nil {
		t.Error("nil router")
	}
	// Mount-гүйгээр Meta/Check нь DB, Can шаардахгүй
	r2 := base()
	r2.DB, r2.Can = nil, nil
	if err := r2.Check(); err != nil {
		t.Errorf("Check DB/Can шаардахгүй: %v", err)
	}
	if r2.Meta().Name != "test.thing" {
		t.Error("Meta DB-гүйгээр")
	}
	if err := r2.Mount(fiber.New().Group("/x")); err == nil {
		t.Error("Mount DB/Can шаардана")
	}
}

func TestMetaContent(t *testing.T) {
	re := validate.Rules{Pattern: nil}
	_ = re
	r := base()
	r.Version = "version"
	r.SoftDelete = true
	r.ID = IDSpec{Kind: IDInt}
	r.Fields = append(r.Fields,
		Field{Name: "price", Label: "Үнэ", Kind: validate.Money, Default: 0, Rules: validate.Rules{Min: validate.Bound(0), Max: validate.Bound(1e6)}},
		Field{Name: "ratio", Kind: validate.Number, Rules: validate.Rules{Scale: 3}},
		Field{Name: "status", Kind: validate.Enum, Input: "status-tag", Rules: validate.Rules{Enum: []string{"a", "b"}}, Default: "a"},
		Field{Name: "pw", Kind: validate.Password},
		Field{Name: "owner", Kind: validate.UUID, Ref: &lookup.Spec{Table: "test.person", Label: []string{"name"}, Parent: "grp"}},
	)
	r.ListQuery = func(db *gorm.DB, orgID string) *gorm.DB { return db }
	r.List = query.Spec{
		Search:  []string{"t.name"},
		Sort:    map[string]string{"name": "t.name", "qty": "t.qty"},
		Filters: map[string]query.FilterCol{"status": query.Enum("t.status"), "name": query.Text("t.name")},
		Totals:  map[string]string{"qty": "SUM(t.qty)"},
	}
	r.Bulk = &query.BulkSpec{AllowDelete: true, Fields: bulkQty()}
	r.Export = &query.ExportSpec{Columns: []query.ExportColumn{{Key: "name", Header: "Нэр", Expr: "t.name"}}}
	r.Children = []Child{childOK("lines")}
	m := r.Meta()
	if m.Name != "test.thing" || m.Label != "Зүйл" || m.Endpoint != "/things" || m.Table != "test.thing" || m.ID.Kind != IDInt || m.ID.Column != "id" ||
		m.Version != "version" || !m.SoftDelete || !m.Export || !m.Bulk || !m.BulkDelete || !m.Search {
		t.Errorf("meta: %+v", m)
	}
	if fmt.Sprint(m.Sort) != "[name qty]" || fmt.Sprint(m.Totals) != "[qty]" || m.Filterable["status"] != query.KindEnum || m.Filterable["name"] != query.KindText || fmt.Sprint(m.BulkFields) != "[qty]" {
		t.Errorf("sort/filterable: %+v", m)
	}
	by := map[string]FieldMeta{}
	for _, f := range m.Fields {
		by[f.Name] = f
	}
	if f := by["name"]; !f.Required || f.MaxLen != 255 || f.Label != "Нэр" {
		t.Errorf("name: %+v", f)
	}
	if f := by["qty"]; f.Label != "qty" || !f.Nullable {
		t.Errorf("qty label анхдагч Name: %+v", f)
	}
	if f := by["price"]; f.Scale != 2 || *f.Min != 0 || *f.Max != 1e6 || f.Default != 0 {
		t.Errorf("price: %+v", f)
	}
	if by["ratio"].Scale != 3 || by["status"].Input != "status-tag" || fmt.Sprint(by["status"].Enum) != "[a b]" || by["status"].Default != "a" {
		t.Errorf("ratio/status: %+v %+v", by["ratio"], by["status"])
	}
	if by["pw"].MinLen != 8 {
		t.Errorf("password minlen: %+v", by["pw"])
	}
	if o := by["owner"].Ref; o == nil || o.Options != "/things/options/owner" || !o.Parent {
		t.Errorf("owner ref: %+v", by["owner"])
	}
	if len(m.Children) != 1 || m.Children[0].MaxRows != 500 || m.Children[0].Name != "lines" || len(m.Children[0].Fields) != 1 {
		t.Errorf("children: %+v", m.Children)
	}
	// JSON-д сериализ болно, тогтвортой
	b1, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	b2, _ := json.Marshal(r.Meta())
	if string(b1) != string(b2) {
		t.Error("Meta тогтвортой биш")
	}
	if strings.Contains(string(b1), "null") && strings.Contains(string(b1), `"fields":null`) {
		t.Error("fields null байж болохгүй")
	}
}

func TestParseIDAndHelpers(t *testing.T) {
	e := &engine{idKind: IDUUID}
	v, s, ok := e.parseID("ABCDEF12-3456-7890-ABCD-EF1234567890")
	if !ok || v != "abcdef12-3456-7890-abcd-ef1234567890" || s != v {
		t.Errorf("%v %v %v", v, s, ok)
	}
	for _, bad := range []string{"", "1", "abcdef12-3456-7890-abcd-ef123456789", "abcdef12-3456-7890-abcd-ef12345678zz", "'; DROP TABLE x;--"} {
		if _, _, ok := e.parseID(bad); ok {
			t.Errorf("uuid %q", bad)
		}
	}
	e = &engine{idKind: IDInt}
	if v, s, ok := e.parseID("42"); !ok || v != int64(42) || s != "42" {
		t.Errorf("%v %v %v", v, s, ok)
	}
	if _, s, ok := e.parseID("007"); !ok || s != "7" {
		t.Error("тэргүүн тэг")
	}
	for _, bad := range []string{"", "0", "-1", "1.0", "+1", "9223372036854775808", "12345678901234567890", "a"} {
		if _, _, ok := e.parseID(bad); ok {
			t.Errorf("int %q", bad)
		}
	}
	u1, u2 := newUUID(), newUUID()
	if u1 == u2 || !validate.IsUUID(u1) || u1[14] != '4' || !strings.ContainsRune("89ab", rune(u1[19])) {
		t.Errorf("UUIDv4: %s %s", u1, u2)
	}
	for _, tc := range []struct {
		in   any
		want int64
	}{{int32(5), 5}, {int64(-1), -1}, {float64(3), 3}, {"12", 12}, {[]byte("7"), 7}} {
		if got, ok := asInt64(tc.in); !ok || got != tc.want {
			t.Errorf("asInt64(%v)=%v", tc.in, got)
		}
	}
	if _, ok := asInt64(1.5); ok {
		t.Error("1.5")
	}
	if _, ok := asInt64(nil); ok {
		t.Error("nil")
	}
	if got := dbValue(json.RawMessage(`{"a":1}`)); got != `{"a":1}` {
		t.Errorf("RawMessage → string: %#v", got)
	}
	if lowerUUID("ABC-DEF") != "abc-def" {
		t.Error("lowerUUID")
	}
}

func TestShapeRow(t *testing.T) {
	fields := []fieldPlan{
		{Field: Field{Name: "opens", Kind: validate.Date}, col: "opens"},
		{Field: Field{Name: "meta", Kind: validate.JSON}, col: "meta_json"},
		{Field: Field{Name: "pw", Kind: validate.Password}, col: "pw"},
		{Field: Field{Name: "title", Kind: validate.String}, col: "title"},
	}
	row := map[string]any{
		"opens": time.Date(2026, 3, 4, 0, 0, 0, 0, time.UTC), "meta_json": `{"a": 1}`, "pw": "$2a$hash", "title": []byte("бай"), "other": 1,
	}
	got := shapeRow(row, fields, map[string]bool{"pw": true})
	if got["opens"] != "2026-03-04" || string(got["meta"].(json.RawMessage)) != `{"a": 1}` || got["title"] != "бай" || got["other"] != 1 {
		t.Errorf("%v", got)
	}
	if _, has := got["pw"]; has {
		t.Error("password хасагдах ёстой")
	}
	if _, has := got["meta_json"]; has {
		t.Error("Column нэр хасагдах ёстой")
	}
	// JSON биш текст JSON талбарт → string хэвээр
	g2 := shapeRow(map[string]any{"meta_json": "plain text"}, fields, nil)
	if g2["meta"] != "plain text" {
		t.Errorf("%v", g2)
	}
}

// DB-гүй HTTP: DB-д хүрэхээс өмнөх зам.
func TestHTTPWithoutDB(t *testing.T) {
	r := base()
	r.Version = "version"
	r.Can = func(c fiber.Ctx, a Action) bool { return a == ActionRead }
	app := mountOn(t, "/things", r)
	org := newUUID()
	// 401
	if x := do(t, app, "POST", "/things", "", 0, `{}`); x.Status != 401 {
		t.Errorf("%d", x.Status)
	}
	// 403: body задлахаас өмнө
	if x := do(t, app, "POST", "/things", org, 1, `{bad`); x.Status != 403 {
		t.Errorf("%d %s", x.Status, x.Raw)
	}
	// DB nil → унших замд 500 generic (panic биш)
	x := do(t, app, "GET", "/things", org, 1, nil)
	if x.Status != 500 || x.str("error") != "Дотоод алдаа гарлаа" {
		t.Errorf("%d %s", x.Status, x.Raw)
	}
	r.Can = func(c fiber.Ctx, a Action) bool { return true }
	app = mountOn(t, "/things", r)
	// 422 (DB-д хүрэхгүй)
	x = do(t, app, "POST", "/things", org, 1, `{"qty":"x"}`)
	if x.Status != 422 || x.errs()["name"] == nil || x.errs()["qty"] == nil {
		t.Errorf("%d %s", x.Status, x.Raw)
	}
	x = do(t, app, "PUT", "/things/"+newUUID(), org, 1, `{"name":"a"}`) // version алга
	if x.Status != 422 || x.errs()["version"] == nil {
		t.Errorf("%d %s", x.Status, x.Raw)
	}
	if x = do(t, app, "GET", "/things/not-uuid", org, 1, nil); x.Status != 400 {
		t.Errorf("%d", x.Status)
	}
}

func TestFailMapping(t *testing.T) {
	e := &engine{name: "test.thing", label: "Зүйл"}
	app := fiber.New()
	var current error
	app.Get("/", func(c fiber.Ctx) error { return e.fail(c, ActionCreate, current) })
	for name, tc := range map[string]struct {
		err    error
		status int
	}{
		"unique":           {&pgconn.PgError{Code: "23505", Message: "duplicate key value violates unique constraint \"secret_idx\""}, 409},
		"fk":               {&pgconn.PgError{Code: "23503", Message: "secret_fk"}, 409},
		"deadlock":         {&pgconn.PgError{Code: "40P01"}, 409},
		"serialization":    {&pgconn.PgError{Code: "40001"}, 409},
		"lock timeout":     {&pgconn.PgError{Code: "55P03"}, 409},
		"too long":         {&pgconn.PgError{Code: "22001", Message: "secret"}, 422},
		"numeric overflow": {&pgconn.PgError{Code: "22003", Message: "secret"}, 422},
		"not null":         {&pgconn.PgError{Code: "23502", Message: "secret_column"}, 500},
		"syntax":           {&pgconn.PgError{Code: "42601", Message: "secret"}, 500},
		"wrapped unique":   {fmt.Errorf("x: %w", &pgconn.PgError{Code: "23505"}), 409},
		"db conflict":      {fmt.Errorf("w: %w", khandb.ErrConflict), 409},
		"not found":        {errNotFound, 404},
		"hook gorm nf":     {gorm.ErrRecordNotFound, 500},
		"plain":            {errors.New("secret plain"), 500},
	} {
		current = tc.err
		res, err := app.Test(httptest.NewRequest("GET", "/", nil), fiber.TestConfig{Timeout: 0})
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(res.Body)
		if res.StatusCode != tc.status || strings.Contains(string(b), "secret") {
			t.Errorf("%s: %d %s", name, res.StatusCode, b)
		}
	}
}
