package crudtest

import (
	"fmt"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/khan-vision/khan/crud"
	"github.com/khan-vision/khan/query"
	"github.com/khan-vision/khan/tenant"
	"github.com/khan-vision/khan/testkit"
	"github.com/khan-vision/khan/validate"
	"gorm.io/gorm"
)

// scopeFx — shared (org_id nullable) ба global (org_id-гүй) хүснэгт.
func scopeFx(t *testing.T) (db *gorm.DB, schema string) {
	t.Helper()
	db = testkit.DB(t)
	schema = fmt.Sprintf("khan_crudtest_sc_%d", time.Now().UnixNano()%1000000000)
	must := func(sql string, a ...any) {
		t.Helper()
		if err := db.Exec(sql, a...).Error; err != nil {
			t.Fatalf("%v\n%s", err, sql)
		}
	}
	must("DROP SCHEMA IF EXISTS " + schema + " CASCADE")
	must("CREATE SCHEMA " + schema)
	t.Cleanup(func() { db.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE") })
	must(`CREATE TABLE ` + schema + `.kind (id uuid PRIMARY KEY, org_id uuid, name text NOT NULL, version int NOT NULL DEFAULT 1,
		created_at timestamptz, updated_at timestamptz, deleted_at timestamptz)`)
	must(`CREATE TABLE ` + schema + `.country (id uuid PRIMARY KEY, name text NOT NULL, version int NOT NULL DEFAULT 1,
		created_at timestamptz, updated_at timestamptz, deleted_at timestamptz)`)
	if err := db.Use(tenant.NewGuardFor([]string{schema + ".kind"}, tenant.GuardStrict, func(f string, a ...any) { t.Errorf(f, a...) })); err != nil {
		t.Fatal(err)
	}
	return db, schema
}

func scopeRes(db *gorm.DB, name, table string, scope crud.Scope, writes bool) *crud.Resource[map[string]any] {
	return &crud.Resource[map[string]any]{
		Name: name, Label: "Төрөл", Endpoint: "/" + name, Table: table, Scope: scope, GlobalWrites: writes,
		Fields:     []crud.Field{{Name: "name", Label: "Нэр", Kind: validate.String, Rules: validate.Rules{Required: true, MaxLen: 100}}},
		List:       query.Spec{DefaultOrder: "t.name ASC, t.id ASC"},
		Bulk:       &query.BulkSpec{Fields: map[string]query.BulkField{"name": {Col: "name", Kind: query.KindText}}, AllowDelete: true},
		SoftDelete: true, Version: "version", ExposeMeta: true,
		Can: allow, DB: func() *gorm.DB { return db },
	}
}

func TestIsolationShared(t *testing.T) {
	db, s := scopeFx(t)
	sys := newUUID()
	if err := db.Exec(`INSERT INTO `+s+`.kind (id, org_id, name) VALUES (?, NULL, 'Систем')`, sys).Error; err != nil {
		t.Fatal(err)
	}
	r := scopeRes(db, "kinds", s+".kind", crud.ScopeShared, false)
	app := buildApp(t, ClaimsMiddleware(), r.Mount, "/kinds")
	n := 0
	Isolation(t, Config{
		App: app, Meta: r.Meta(), Auth: HeaderAuth(1), SharedID: sys,
		Sample: func(org string) map[string]any { n++; return map[string]any{"name": fmt.Sprintf("Төрөл-%d", n)} },
	})
}

func TestIsolationGlobal(t *testing.T) {
	db, s := scopeFx(t)
	exist := newUUID()
	if err := db.Exec(`INSERT INTO `+s+`.country (id, name) VALUES (?, 'Монгол')`, exist).Error; err != nil {
		t.Fatal(err)
	}
	for _, writes := range []bool{false, true} {
		r := scopeRes(db, "countries", s+".country", crud.ScopeGlobal, writes)
		if !writes {
			r.Bulk = nil
		}
		app := buildApp(t, ClaimsMiddleware(), r.Mount, "/countries")
		n := 0
		Isolation(t, Config{
			App: app, Meta: r.Meta(), Auth: HeaderAuth(1), SharedID: exist,
			Sample: func(org string) map[string]any { n++; return map[string]any{"name": fmt.Sprintf("Улс-%d", n)} },
		})
	}
}

// Shared resource-ийн Can-д хариуцлагагүй: SystemWritable=true үед 403 шалгалт алгасагдана.
func TestIsolationSharedSystemWritable(t *testing.T) {
	db, s := scopeFx(t)
	sys := newUUID()
	_ = db.Exec(`INSERT INTO `+s+`.kind (id, org_id, name) VALUES (?, NULL, 'Систем')`, sys).Error
	r := scopeRes(db, "kinds", s+".kind", crud.ScopeShared, false)
	r.SystemCan = func(fiber.Ctx, crud.Action) bool { return true }
	app := buildApp(t, ClaimsMiddleware(), r.Mount, "/kinds")
	n := 0
	Isolation(t, Config{
		App: app, Meta: r.Meta(), Auth: HeaderAuth(1), SharedID: sys, SystemWritable: true,
		Sample: func(org string) map[string]any { n++; return map[string]any{"name": fmt.Sprintf("Төрөл-%d", n)} },
	})
}
