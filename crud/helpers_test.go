package crud

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/golang-jwt/jwt/v5"
	"github.com/khan-vision/khan/auth"
	"github.com/khan-vision/khan/events"
	"github.com/khan-vision/khan/lookup"
	"github.com/khan-vision/khan/query"
	"github.com/khan-vision/khan/tenant"
	"github.com/khan-vision/khan/testkit"
	"github.com/khan-vision/khan/validate"
	"gorm.io/gorm"
)

// Тестийн хэрэглэгчийн дүр: role 1 = бүх эрх, 2 = зөвхөн унших, 3 = унших+үүсгэх+засах (устгах эрхгүй).
func canByRole(c fiber.Ctx, a Action) bool {
	p, ok := auth.User(c)
	if !ok {
		return false
	}
	switch p.Role {
	case 1:
		return true
	case 2:
		return a == ActionRead
	case 3:
		return a != ActionDelete
	}
	return false
}

// claimsMW — тестийн толгойгоос (X-T-*) JWT claims тавина. Production-ийн X-Org-ID БИШ.
func claimsMW(c fiber.Ctx) error {
	org := c.Get("X-T-Org")
	if org == "" && c.Get("X-T-NoOrg") == "1" { // org_id claim-гүй token (sysadmin / single-tenant)
		role := 1
		fmt.Sscan(c.Get("X-T-Role"), &role)
		c.Locals(tenant.LocalsKey, &jwt.Token{Claims: jwt.MapClaims{"id": "99999999-0000-0000-0000-000000000001", "role": role}, Valid: true})
	}
	if org != "" {
		role := 1
		fmt.Sscan(c.Get("X-T-Role"), &role)
		user := c.Get("X-T-User")
		if user == "" {
			user = "99999999-0000-0000-0000-000000000001"
		}
		c.Locals(tenant.LocalsKey, &jwt.Token{Claims: jwt.MapClaims{"id": user, "org_id": org, "role": role}, Valid: true})
	}
	return c.Next()
}

type fx struct {
	t          *testing.T
	db         *gorm.DB
	schema     string
	orgA, orgB string
	userA      string
}

// newFx — түр schema, хүснэгтүүд, tenant Guard (strict), events хүснэгт. Тест дуусахад бүгдийг цэвэрлэнэ.
func newFx(t *testing.T) *fx {
	t.Helper()
	db := testkit.DB(t)
	f := &fx{t: t, db: db, orgA: newUUID(), orgB: newUUID(), userA: newUUID()}
	f.schema = fmt.Sprintf("khan_crud_%d", time.Now().UnixNano()%1000000000)
	s := f.schema
	f.exec("DROP SCHEMA IF EXISTS " + s + " CASCADE")
	f.exec("CREATE SCHEMA " + s)
	t.Cleanup(func() {
		db.Exec("DROP SCHEMA IF EXISTS " + s + " CASCADE")
		db.Exec("DELETE FROM khan.outbox_events WHERE org_id IN (?, ?)", f.orgA, f.orgB)
	})
	f.exec(events.SchemaSQL)
	f.exec(`CREATE TABLE ` + s + `.person (id uuid PRIMARY KEY, org_id uuid NOT NULL, name text NOT NULL, group_id int, deleted_at timestamptz)`)
	f.exec(`CREATE TABLE ` + s + `.room (
		id uuid PRIMARY KEY, org_id uuid NOT NULL, name text NOT NULL, code text, capacity int, price numeric(18,2),
		active boolean NOT NULL DEFAULT true, opens date, status text NOT NULL DEFAULT 'draft', meta jsonb,
		owner_id uuid, note text, version int NOT NULL DEFAULT 1,
		created_at timestamptz, updated_at timestamptz, deleted_at timestamptz, created_by uuid, updated_by uuid)`)
	f.exec(`CREATE UNIQUE INDEX room_code_uq ON ` + s + `.room (org_id, code) WHERE deleted_at IS NULL`)
	f.exec(`CREATE TABLE ` + s + `.invoice (
		id uuid PRIMARY KEY, org_id uuid NOT NULL, number text NOT NULL, version int NOT NULL DEFAULT 1,
		created_at timestamptz, updated_at timestamptz)`)
	f.exec(`CREATE TABLE ` + s + `.invoice_line (
		id uuid PRIMARY KEY, org_id uuid NOT NULL, invoice_id uuid NOT NULL REFERENCES ` + s + `.invoice(id),
		item_id uuid, qty int NOT NULL, price numeric(18,2) NOT NULL, position int,
		created_at timestamptz, updated_at timestamptz, deleted_at timestamptz)`)
	f.exec(`CREATE TABLE ` + s + `.invoice_note (
		id uuid PRIMARY KEY, org_id uuid NOT NULL, invoice_id uuid NOT NULL REFERENCES ` + s + `.invoice(id),
		body text NOT NULL, created_at timestamptz, updated_at timestamptz)`)
	f.exec(`CREATE TABLE ` + s + `.account (
		id serial PRIMARY KEY, org_id uuid NOT NULL, login text NOT NULL, password text, display text,
		created_at timestamptz, updated_at timestamptz)`)
	f.exec(`CREATE TABLE ` + s + `.tag (id uuid PRIMARY KEY, org_id uuid NOT NULL, label text NOT NULL)`)

	// Strict Guard: crud-ийн query бүр org_id-тай эсэхийг баталгаажуулна.
	names := []string{s + ".person", s + ".room", s + ".invoice", s + ".invoice_line", s + ".invoice_note", s + ".account", s + ".tag"}
	if err := db.Use(tenant.NewGuardFor(names, tenant.GuardStrict, func(format string, a ...any) { t.Errorf(format, a...) })); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fx) exec(sql string, args ...any) {
	f.t.Helper()
	if err := f.db.Exec(sql, args...).Error; err != nil {
		f.t.Fatalf("%v\n%s", err, sql)
	}
}

func (f *fx) tbl(name string) string { return f.schema + "." + name }

func (f *fx) count(sql string, args ...any) int {
	f.t.Helper()
	var n int
	if err := f.db.Raw(sql, args...).Scan(&n).Error; err != nil {
		f.t.Fatalf("%v\n%s", err, sql)
	}
	return n
}

func (f *fx) person(org, name string, group int) string {
	id := newUUID()
	f.exec(`INSERT INTO `+f.tbl("person")+` (id, org_id, name, group_id) VALUES (?,?,?,?)`, id, org, name, group)
	return id
}

func (f *fx) personRef() *lookup.Spec {
	return &lookup.Spec{Table: f.tbl("person"), Label: []string{"name"}, SoftDelete: true, Parent: "group_id"}
}

// roomResource — бүх боломжийг агуулсан стандарт resource (тест бүр өөрчилж болно).
func (f *fx) roomResource() *Resource[map[string]any] {
	return &Resource[map[string]any]{
		Name: "test.room", Label: "Өрөө", Endpoint: "/rooms", Table: f.tbl("room"),
		Fields: []Field{
			{Name: "name", Label: "Нэр", Kind: validate.String, Rules: validate.Rules{Required: true, MaxLen: 100}},
			{Name: "code", Label: "Код", Kind: validate.String, Unique: true,
				Rules: validate.Rules{Nullable: true, MaxLen: 20, Pattern: regexp.MustCompile(`^[A-Z0-9-]+$`), PatternMsg: "Код зөвхөн том латин үсэг, тоо"}},
			{Name: "capacity", Label: "Багтаамж", Kind: validate.Int, Rules: validate.Rules{Nullable: true, Min: validate.Bound(0), Max: validate.Bound(100000)}},
			{Name: "price", Label: "Үнэ", Kind: validate.Money, Rules: validate.Rules{Nullable: true, Min: validate.Bound(0)}},
			{Name: "active", Label: "Идэвхтэй", Kind: validate.Bool, Default: true},
			{Name: "opens", Label: "Нээх огноо", Kind: validate.Date, Rules: validate.Rules{Nullable: true}},
			{Name: "status", Label: "Төлөв", Kind: validate.Enum, Default: "draft", Rules: validate.Rules{Enum: []string{"draft", "open", "closed"}}},
			{Name: "meta", Label: "Мета", Kind: validate.JSON, Rules: validate.Rules{Nullable: true}},
			{Name: "owner_id", Label: "Эзэмшигч", Kind: validate.UUID, Rules: validate.Rules{Nullable: true}, Ref: f.personRef()},
			{Name: "created_note", Column: "note", Label: "Тэмдэглэл", Kind: validate.Text, CreateOnly: true, Rules: validate.Rules{Nullable: true}},
		},
		List: query.Spec{
			Search:       []string{"t.name", "t.code"},
			Sort:         map[string]string{"name": "t.name", "capacity": "t.capacity"},
			Filters:      map[string]query.FilterCol{"name": query.Text("t.name"), "status": query.Enum("t.status"), "capacity": query.Number("t.capacity")},
			DefaultOrder: "t.name ASC, t.id ASC",
			Totals:       map[string]string{"capacity": "COALESCE(SUM(t.capacity),0)"},
		},
		Export: &query.ExportSpec{Columns: []query.ExportColumn{
			{Key: "name", Header: "Нэр", Expr: "t.name"},
			{Key: "capacity", Header: "Багтаамж", Expr: "t.capacity", Numeric: true},
		}, FileName: "rooms"},
		Bulk: &query.BulkSpec{
			Fields:      map[string]query.BulkField{"capacity": {Col: "capacity", Kind: query.KindNumber, Nullable: true, Integer: true, Min: query.Bound(0)}},
			AllowDelete: true,
		},
		SoftDelete: true, Version: "version", TrackUser: true, Events: true, ExposeMeta: true,
		Can: canByRole, DB: func() *gorm.DB { return f.db },
	}
}

func (f *fx) invoiceResource() *Resource[map[string]any] {
	return &Resource[map[string]any]{
		Name: "test.invoice", Label: "Нэхэмжлэх", Endpoint: "/invoices", Table: f.tbl("invoice"),
		Fields: []Field{
			{Name: "number", Label: "Дугаар", Kind: validate.String, Unique: true, Rules: validate.Rules{Required: true, MaxLen: 30}},
		},
		Children: []Child{
			{Name: "lines", Label: "Мөр", Table: f.tbl("invoice_line"), FK: "invoice_id", Order: "position", MaxRows: 3, SoftDelete: true,
				Fields: []Field{
					{Name: "item_id", Label: "Бараа", Kind: validate.UUID, Rules: validate.Rules{Required: true}, Ref: f.personRef()},
					{Name: "qty", Label: "Тоо", Kind: validate.Int, Rules: validate.Rules{Required: true, Min: validate.Bound(1)}},
					{Name: "price", Label: "Үнэ", Kind: validate.Money, Rules: validate.Rules{Required: true}},
					{Name: "position", Label: "Дараалал", Kind: validate.Int, Rules: validate.Rules{Nullable: true}},
				}},
			{Name: "notes", Label: "Тэмдэглэл", Table: f.tbl("invoice_note"), FK: "invoice_id",
				Fields: []Field{{Name: "body", Label: "Агуулга", Kind: validate.Text, Rules: validate.Rules{Required: true}}}},
		},
		Version: "version", Events: true,
		List: query.Spec{DefaultOrder: "t.number ASC", Search: []string{"t.number"}},
		Can:  canByRole, DB: func() *gorm.DB { return f.db },
	}
}

func (f *fx) accountResource() *Resource[map[string]any] {
	return &Resource[map[string]any]{
		Name: "test.account", Label: "Хэрэглэгч", Endpoint: "/accounts", Table: f.tbl("account"),
		ID: IDSpec{Kind: IDInt},
		Fields: []Field{
			{Name: "login", Label: "Нэвтрэх нэр", Kind: validate.String, Unique: true, Rules: validate.Rules{Required: true, MaxLen: 40}},
			{Name: "password", Label: "Нууц үг", Kind: validate.Password, Rules: validate.Rules{Required: true}},
			{Name: "display", Label: "Нэр", Kind: validate.String, Rules: validate.Rules{Nullable: true}},
		},
		ListQuery: func(db *gorm.DB, orgID string) *gorm.DB {
			return db.Table(f.tbl("account")+" t").Select("t.id, t.login, t.display").Where("t.org_id = ?", orgID)
		},
		List:   query.Spec{DefaultOrder: "t.id ASC"},
		Events: true,
		Can:    canByRole, DB: func() *gorm.DB { return f.db },
	}
}

func (f *fx) tagResource() *Resource[map[string]any] {
	return &Resource[map[string]any]{
		Name: "test.tag", Label: "Шошго", Table: f.tbl("tag"), Timestamps: Off(),
		Fields: []Field{{Name: "label", Label: "Нэр", Kind: validate.String, Rules: validate.Rules{Required: true}}},
		List:   query.Spec{DefaultOrder: "t.label ASC"},
		Can:    canByRole, DB: func() *gorm.DB { return f.db },
	}
}

// mount — resource-ийг шинэ app дээр prefix-д mount хийнэ.
func mountOn[T any](t *testing.T, prefix string, r *Resource[T]) *fiber.App {
	t.Helper()
	app := fiber.New()
	g := app.Group(prefix, claimsMW)
	if err := r.Mount(g); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	return app
}

type resp struct {
	Status int
	Raw    []byte
	JSON   map[string]any
}

func (r resp) errs() map[string][]string {
	out := map[string][]string{}
	if m, ok := r.JSON["errors"].(map[string]any); ok {
		for k, v := range m {
			for _, s := range v.([]any) {
				out[k] = append(out[k], s.(string))
			}
		}
	}
	return out
}

func (r resp) str(key string) string { s, _ := r.JSON[key].(string); return s }

func (r resp) list(key string) []map[string]any {
	var out []map[string]any
	if arr, ok := r.JSON[key].([]any); ok {
		for _, x := range arr {
			out = append(out, x.(map[string]any))
		}
	}
	return out
}

// do — HTTP хүсэлт. body: string/[]byte (түүхий) эсвэл JSON болгох утга.
func do(t *testing.T, app *fiber.App, method, path, org string, role int, body any) resp {
	t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	case []byte:
		rd = bytes.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			t.Fatal(err)
		}
		rd = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, rd)
	req.Header.Set("Content-Type", "application/json")
	if org != "" {
		req.Header.Set("X-T-Org", org)
		req.Header.Set("X-T-Role", fmt.Sprint(role))
	}
	res, err := app.Test(req, fiber.TestConfig{Timeout: 0})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(res.Body)
	out := resp{Status: res.StatusCode, Raw: raw}
	_ = json.Unmarshal(raw, &out.JSON)
	return out
}

func mustStatus(t *testing.T, r resp, want int) {
	t.Helper()
	if r.Status != want {
		t.Fatalf("status=%d, хүлээсэн %d: %s", r.Status, want, r.Raw)
	}
}

func queryDefault() query.Spec { return query.Spec{DefaultOrder: "t.id ASC"} }
