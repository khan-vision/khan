package crudtest

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/khan-vision/khan/auth"
	"github.com/khan-vision/khan/crud"
	"github.com/khan-vision/khan/lookup"
	"github.com/khan-vision/khan/query"
	"github.com/khan-vision/khan/tenant"
	"github.com/khan-vision/khan/testkit"
	"github.com/khan-vision/khan/validate"
	"gorm.io/gorm"
)

type dbFx struct {
	t      *testing.T
	db     *gorm.DB
	schema string
	seq    atomic.Int64
}

func newDBFx(t *testing.T) *dbFx {
	t.Helper()
	db := testkit.DB(t)
	f := &dbFx{t: t, db: db, schema: fmt.Sprintf("khan_crudtest_%d", time.Now().UnixNano()%1000000000)}
	f.exec("DROP SCHEMA IF EXISTS " + f.schema + " CASCADE")
	f.exec("CREATE SCHEMA " + f.schema)
	t.Cleanup(func() { db.Exec("DROP SCHEMA IF EXISTS " + f.schema + " CASCADE") })
	s := f.schema
	f.exec(`CREATE TABLE ` + s + `.person (id uuid PRIMARY KEY, org_id uuid NOT NULL, name text NOT NULL, deleted_at timestamptz)`)
	f.exec(`CREATE TABLE ` + s + `.room (id uuid PRIMARY KEY, org_id uuid NOT NULL, name text NOT NULL, code text, owner_id uuid,
		version int NOT NULL DEFAULT 1, created_at timestamptz, updated_at timestamptz, deleted_at timestamptz)`)
	f.exec(`CREATE UNIQUE INDEX room_code_uq ON ` + s + `.room (org_id, code) WHERE deleted_at IS NULL`)
	f.exec(`CREATE TABLE ` + s + `.invoice (id uuid PRIMARY KEY, org_id uuid NOT NULL, number text NOT NULL, version int NOT NULL DEFAULT 1,
		created_at timestamptz, updated_at timestamptz)`)
	f.exec(`CREATE TABLE ` + s + `.invoice_line (id uuid PRIMARY KEY, org_id uuid NOT NULL, invoice_id uuid NOT NULL REFERENCES ` + s + `.invoice(id),
		item_id uuid, qty int NOT NULL, created_at timestamptz, updated_at timestamptz)`)
	names := []string{s + ".person", s + ".room", s + ".invoice", s + ".invoice_line"}
	if err := db.Use(tenant.NewGuardFor(names, tenant.GuardStrict, func(format string, a ...any) { t.Errorf(format, a...) })); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *dbFx) exec(sql string, args ...any) {
	f.t.Helper()
	if err := f.db.Exec(sql, args...).Error; err != nil {
		f.t.Fatalf("%v\n%s", err, sql)
	}
}

func (f *dbFx) tbl(n string) string { return f.schema + "." + n }

func (f *dbFx) person(org string) string {
	id := newUUID()
	f.exec(`INSERT INTO `+f.tbl("person")+` (id, org_id, name) VALUES (?,?,?)`, id, org, "Хүн "+id[:6])
	return id
}

func (f *dbFx) uniq(prefix string) string { return fmt.Sprintf("%s-%d", prefix, f.seq.Add(1)) }

func (f *dbFx) personRef() *lookup.Spec {
	return &lookup.Spec{Table: f.tbl("person"), Label: []string{"name"}, SoftDelete: true}
}

func allow(fiber.Ctx, crud.Action) bool { return true }

func (f *dbFx) rooms() *crud.Resource[map[string]any] {
	return &crud.Resource[map[string]any]{
		Name: "test.room", Label: "Өрөө", Endpoint: "/rooms", Table: f.tbl("room"),
		Fields: []crud.Field{
			{Name: "name", Label: "Нэр", Kind: validate.String, Rules: validate.Rules{Required: true, MaxLen: 100}},
			{Name: "code", Label: "Код", Kind: validate.String, Unique: true, Rules: validate.Rules{Nullable: true, MaxLen: 30}},
			{Name: "owner_id", Label: "Эзэмшигч", Kind: validate.UUID, Rules: validate.Rules{Nullable: true}, Ref: f.personRef()},
		},
		List:   query.Spec{Search: []string{"t.name"}, DefaultOrder: "t.name ASC, t.id ASC"},
		Export: &query.ExportSpec{Columns: []query.ExportColumn{{Key: "name", Header: "Нэр", Expr: "t.name"}}, FileName: "rooms"},
		Bulk: &query.BulkSpec{
			Fields:      map[string]query.BulkField{"name": {Col: "name", Kind: query.KindText}},
			AllowDelete: true,
		},
		SoftDelete: true, Version: "version", ExposeMeta: true,
		Can: allow, DB: func() *gorm.DB { return f.db },
	}
}

func (f *dbFx) invoices() *crud.Resource[map[string]any] {
	return &crud.Resource[map[string]any]{
		Name: "test.invoice", Label: "Нэхэмжлэх", Endpoint: "/invoices", Table: f.tbl("invoice"),
		Fields: []crud.Field{{Name: "number", Label: "Дугаар", Kind: validate.String, Unique: true, Rules: validate.Rules{Required: true, MaxLen: 30}}},
		Children: []crud.Child{{
			Name: "lines", Label: "Мөр", Table: f.tbl("invoice_line"), FK: "invoice_id", MaxRows: 5,
			Fields: []crud.Field{
				{Name: "item_id", Label: "Бараа", Kind: validate.UUID, Rules: validate.Rules{Required: true}, Ref: f.personRef()},
				{Name: "qty", Label: "Тоо", Kind: validate.Int, Rules: validate.Rules{Required: true, Min: validate.Bound(1)}},
			},
		}},
		Version: "version", List: query.Spec{DefaultOrder: "t.number ASC"},
		Can: allow, DB: func() *gorm.DB { return f.db },
	}
}

func buildApp(t *testing.T, mw fiber.Handler, mount func(fiber.Router) error, prefix string) *fiber.App {
	t.Helper()
	app := fiber.New()
	if err := mount(app.Group(prefix, mw)); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	return app
}

func jwtMW(t *testing.T) fiber.Handler {
	t.Helper()
	kr, err := auth.NewKeyring(auth.Key{ID: "k1", Secret: []byte(testSecret)})
	if err != nil {
		t.Fatal(err)
	}
	return auth.Required(kr)
}

// Жинхэнэ crud.Resource (DB-тэй, strict Guard, жинхэнэ JWT middleware) — Isolation бүгд PASS.
func TestIsolationRealRooms_JWT(t *testing.T) {
	f := newDBFx(t)
	r := f.rooms()
	app := buildApp(t, jwtMW(t), r.Mount, "/rooms")
	Isolation(t, Config{
		App: app, Meta: r.Meta(), Secret: testSecret,
		Sample: func(org string) map[string]any {
			return map[string]any{"name": f.uniq("Өрөө"), "code": f.uniq("C"), "owner_id": f.person(org)}
		},
		RefSample: map[string]func(string) string{"owner_id": f.person},
	})
}

// Header-ээр claims тарих горим (ClaimsMiddleware + HeaderAuth).
func TestIsolationRealRooms_HeaderClaims(t *testing.T) {
	f := newDBFx(t)
	r := f.rooms()
	app := buildApp(t, ClaimsMiddleware(), r.Mount, "/rooms")
	Isolation(t, Config{
		App: app, Meta: r.Meta(), Auth: HeaderAuth(1),
		Sample: func(org string) map[string]any {
			return map[string]any{"name": f.uniq("Өрөө"), "code": f.uniq("C")}
		},
		RefSample: map[string]func(string) string{"owner_id": f.person},
	})
}

// Token callback горим + children (invoice → lines).
func TestIsolationRealInvoices_ChildrenCustomToken(t *testing.T) {
	f := newDBFx(t)
	r := f.invoices()
	app := buildApp(t, jwtMW(t), r.Mount, "/invoices")
	Isolation(t, Config{
		App: app, Meta: r.Meta(),
		Token: func(org, user string) string {
			tok, err := testkit.SignedToken(testSecret, testkit.Claims(user, org, 1))
			if err != nil {
				t.Fatal(err)
			}
			return tok
		},
		Sample: func(org string) map[string]any { return map[string]any{"number": f.uniq("INV")} },
		ChildSample: map[string]func(string) map[string]any{
			"lines": func(org string) map[string]any { return map[string]any{"item_id": f.person(org), "qty": 2} },
		},
		RefSample: map[string]func(string) string{"lines.item_id": f.person},
	})
}
