package crud

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/khan-vision/khan/audit"
	"github.com/khan-vision/khan/auth"
	"github.com/khan-vision/khan/cache"
	"github.com/khan-vision/khan/events"
	"github.com/khan-vision/khan/lookup"
	"github.com/khan-vision/khan/query"
	"github.com/khan-vision/khan/tenant"
	"github.com/khan-vision/khan/testkit"
	"github.com/khan-vision/khan/validate"
	"gorm.io/gorm"
)

// Scope тестийн дүрүүд (X-T-Role): 1 = tenant админ (бүх Can, SystemCan үгүй), 2 = унших-л,
// 4 = sysadmin (бүх Can + SystemCan).
const (
	roleAdmin = 1
	roleRead  = 2
	roleSys   = 4
)

func scopeCan(c fiber.Ctx, a Action) bool {
	p, ok := auth.User(c)
	if !ok {
		return false
	}
	switch p.Role {
	case roleAdmin, roleSys:
		return true
	case roleRead:
		return a == ActionRead
	}
	return false
}

func sysCan(c fiber.Ctx, _ Action) bool {
	p, ok := auth.User(c)
	return ok && p.Role == roleSys
}

// sfx — ScopeShared / ScopeGlobal тестийн fixture: өөрийн schema, өөрийн strict Guard (org_id-тай
// хүснэгтүүд: kind, kind_item, grp, owner; global хүснэгтэд Guard үйлчлэхгүй — тэдгээрт org_id алга).
type sfx struct {
	t                *testing.T
	db               *gorm.DB
	schema           string
	orgA, orgB, orgS string // S = sysadmin-ий org
}

func newSfx(t *testing.T) *sfx {
	t.Helper()
	db := testkit.DB(t)
	f := &sfx{t: t, db: db, orgA: newUUID(), orgB: newUUID(), orgS: newUUID()}
	f.schema = fmt.Sprintf("khan_scope_%d", time.Now().UnixNano()%1000000000)
	s := f.schema
	f.exec("DROP SCHEMA IF EXISTS " + s + " CASCADE")
	f.exec("CREATE SCHEMA " + s)
	t.Cleanup(func() {
		db.Exec("DROP SCHEMA IF EXISTS " + s + " CASCADE")
		db.Exec("DELETE FROM khan.outbox_events WHERE org_id IN (?, ?, ?)", f.orgA, f.orgB, f.orgS)
		for _, o := range []string{f.orgA, f.orgB, f.orgS} {
			_, _ = audit.Purge(context.Background(), db, time.Now(), audit.ForOrg(o))
		}
	})
	f.exec(events.SchemaSQL)
	f.exec(audit.SchemaSQL)
	f.exec(`CREATE TABLE ` + s + `.kind (
		id uuid PRIMARY KEY, org_id uuid, name text NOT NULL, code text, note text,
		group_id uuid, owner_id uuid, country_id uuid, version int NOT NULL DEFAULT 1,
		created_at timestamptz, updated_at timestamptz, deleted_at timestamptz)`)
	f.exec(`CREATE TABLE ` + s + `.kind_item (
		id uuid PRIMARY KEY, org_id uuid, kind_id uuid NOT NULL REFERENCES ` + s + `.kind(id), label text NOT NULL,
		created_at timestamptz, updated_at timestamptz, deleted_at timestamptz)`)
	// IDInt child хүснэгтүүд (bigserial / int4 serial): kind_note — org_id nullable (shared), country_tag — org_id алга.
	f.exec(`CREATE TABLE ` + s + `.kind_note (
		id bigserial PRIMARY KEY, org_id uuid, kind_id uuid NOT NULL REFERENCES ` + s + `.kind(id), body text NOT NULL,
		created_at timestamptz, updated_at timestamptz, deleted_at timestamptz)`)
	f.exec(`CREATE TABLE ` + s + `.grp (id uuid PRIMARY KEY, org_id uuid, name text NOT NULL)`)
	f.exec(`CREATE TABLE ` + s + `.owner (id uuid PRIMARY KEY, org_id uuid NOT NULL, name text NOT NULL)`)
	// Global хүснэгтүүд: org_id БАЙХГҮЙ.
	f.exec(`CREATE TABLE ` + s + `.country (
		id uuid PRIMARY KEY, name text NOT NULL, code text, pop int, version int NOT NULL DEFAULT 1,
		created_at timestamptz, updated_at timestamptz, deleted_at timestamptz)`)
	f.exec(`CREATE TABLE ` + s + `.country_city (
		id uuid PRIMARY KEY, country_id uuid NOT NULL REFERENCES ` + s + `.country(id), name text NOT NULL,
		created_at timestamptz, updated_at timestamptz, deleted_at timestamptz)`)
	f.exec(`CREATE TABLE ` + s + `.country_tag (
		tag_id serial PRIMARY KEY, country_id uuid NOT NULL REFERENCES ` + s + `.country(id), name text NOT NULL,
		created_at timestamptz, updated_at timestamptz)`)
	names := []string{s + ".kind", s + ".kind_item", s + ".kind_note", s + ".grp", s + ".owner"}
	if err := db.Use(tenant.NewGuardFor(names, tenant.GuardStrict, func(format string, a ...any) { t.Errorf(format, a...) })); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *sfx) exec(sql string, args ...any) {
	f.t.Helper()
	if err := f.db.Exec(sql, args...).Error; err != nil {
		f.t.Fatalf("%v\n%s", err, sql)
	}
}

func (f *sfx) tbl(name string) string { return f.schema + "." + name }

func (f *sfx) count(sql string, args ...any) int {
	f.t.Helper()
	var n int
	if err := f.db.Raw(sql, args...).Scan(&n).Error; err != nil {
		f.t.Fatalf("%v\n%s", err, sql)
	}
	return n
}

// kindRow — kind мөр шууд DB-д (org="" → системийн мөр, org_id NULL).
func (f *sfx) kindRow(org, name, code string) string {
	id := newUUID()
	var o any
	if org != "" {
		o = org
	}
	f.exec(`INSERT INTO `+f.tbl("kind")+` (id, org_id, name, code) VALUES (?,?,?,?)`, id, o, name, nullable(code))
	return id
}

func (f *sfx) kindItem(org, kindID, label string) string {
	id := newUUID()
	var o any
	if org != "" {
		o = org
	}
	f.exec(`INSERT INTO `+f.tbl("kind_item")+` (id, org_id, kind_id, label) VALUES (?,?,?,?)`, id, o, kindID, label)
	return id
}

func (f *sfx) grp(org, name string) string {
	id := newUUID()
	var o any
	if org != "" {
		o = org
	}
	f.exec(`INSERT INTO `+f.tbl("grp")+` (id, org_id, name) VALUES (?,?,?)`, id, o, name)
	return id
}

func (f *sfx) owner(org, name string) string {
	id := newUUID()
	f.exec(`INSERT INTO `+f.tbl("owner")+` (id, org_id, name) VALUES (?,?,?)`, id, org, name)
	return id
}

func (f *sfx) country(name, code string) string {
	id := newUUID()
	f.exec(`INSERT INTO `+f.tbl("country")+` (id, name, code, pop) VALUES (?,?,?,?)`, id, name, nullable(code), 0)
	return id
}

// kindResource — ScopeShared resource (children, ref-үүд, version, events, bulk, meta).
func (f *sfx) kindResource() *Resource[map[string]any] {
	return &Resource[map[string]any]{
		Name: "test.kind", Label: "Төрөл", Endpoint: "/kinds", Table: f.tbl("kind"), Scope: ScopeShared,
		Fields: []Field{
			{Name: "name", Label: "Нэр", Kind: validate.String, Rules: validate.Rules{Required: true, MaxLen: 100}},
			{Name: "code", Label: "Код", Kind: validate.String, Unique: true, Rules: validate.Rules{Nullable: true, MaxLen: 20}},
			{Name: "note", Label: "Тэмдэглэл", Kind: validate.Text, Rules: validate.Rules{Nullable: true}},
			{Name: "group_id", Label: "Бүлэг", Kind: validate.UUID, Rules: validate.Rules{Nullable: true},
				Ref: &lookup.Spec{Table: f.tbl("grp"), Label: []string{"name"}, Shared: true}},
			{Name: "owner_id", Label: "Эзэмшигч", Kind: validate.UUID, Rules: validate.Rules{Nullable: true},
				Ref: &lookup.Spec{Table: f.tbl("owner"), Label: []string{"name"}}},
			{Name: "country_id", Label: "Улс", Kind: validate.UUID, Rules: validate.Rules{Nullable: true},
				Ref: &lookup.Spec{Table: f.tbl("country"), Label: []string{"name"}, Tenant: lookup.TenantOff()}},
		},
		Children: []Child{{Name: "items", Label: "Зүйл", Table: f.tbl("kind_item"), FK: "kind_id", SoftDelete: true,
			Fields: []Field{
				{Name: "label", Label: "Шошго", Kind: validate.String, Rules: validate.Rules{Required: true}},
			}}},
		List: query.Spec{
			Search: []string{"t.name"}, DefaultOrder: "t.name ASC, t.id ASC",
			Sort:    map[string]string{"name": "t.name"},
			Filters: map[string]query.FilterCol{"name": query.Text("t.name")},
		},
		Bulk: &query.BulkSpec{
			Fields:      map[string]query.BulkField{"note": {Col: "note", Kind: query.KindText, Nullable: true}},
			AllowDelete: true,
		},
		SoftDelete: true, Version: "version", Events: true, ExposeMeta: true,
		Can: scopeCan, SystemCan: sysCan, DB: func() *gorm.DB { return f.db },
	}
}

// countryResource — ScopeGlobal resource (children, bulk, unique).
func (f *sfx) countryResource(writes bool) *Resource[map[string]any] {
	return &Resource[map[string]any]{
		Name: "test.country", Label: "Улс", Endpoint: "/countries", Table: f.tbl("country"), Scope: ScopeGlobal, GlobalWrites: writes,
		Fields: []Field{
			{Name: "name", Label: "Нэр", Kind: validate.String, Rules: validate.Rules{Required: true, MaxLen: 100}},
			{Name: "code", Label: "Код", Kind: validate.String, Unique: true, Rules: validate.Rules{Nullable: true, MaxLen: 10}},
			{Name: "pop", Label: "Хүн ам", Kind: validate.Int, Rules: validate.Rules{Nullable: true, Min: validate.Bound(0)}},
		},
		Children: []Child{{Name: "cities", Label: "Хот", Table: f.tbl("country_city"), FK: "country_id", SoftDelete: true,
			Fields: []Field{{Name: "name", Label: "Нэр", Kind: validate.String, Rules: validate.Rules{Required: true}}}}},
		List: query.Spec{Search: []string{"t.name"}, DefaultOrder: "t.name ASC, t.id ASC"},
		Bulk: bulkIf(writes, &query.BulkSpec{
			Fields:      map[string]query.BulkField{"pop": {Col: "pop", Kind: query.KindNumber, Nullable: true, Integer: true, Min: query.Bound(0)}},
			AllowDelete: true,
		}),
		SoftDelete: true, Version: "version", Events: true, ExposeMeta: true,
		Can: scopeCan, DB: func() *gorm.DB { return f.db },
	}
}

func bulkIf(on bool, b *query.BulkSpec) *query.BulkSpec {
	if on {
		return b
	}
	return nil
}

func newTestCache(t *testing.T) *cache.Cache {
	t.Helper()
	c, err := cache.New(cache.Config{})
	if err != nil {
		t.Fatal(err)
	}
	return c
}
