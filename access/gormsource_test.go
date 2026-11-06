package access

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/khan-vision/khan/testkit"
	"gorm.io/gorm"
)

// gormFixture — түр schema + хүснэгтүүд (тест дуусахад DROP SCHEMA — зөвхөн өөрийн түр schema).
func gormFixture(t *testing.T) (*gorm.DB, string) {
	t.Helper()
	db := testkit.DB(t)
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	schema := "access_t_" + hex.EncodeToString(b)
	stmts := []string{
		`CREATE SCHEMA ` + schema,
		`CREATE TABLE ` + schema + `.employee (
			id serial PRIMARY KEY, org_id uuid NOT NULL, user_id text, status_id int NOT NULL DEFAULT 1,
			deleted_at timestamptz, permissions jsonb)`,
		`CREATE TABLE ` + schema + `.org_users (org_id uuid NOT NULL, user_id text NOT NULL, access text NOT NULL)`,
		`CREATE TABLE ` + schema + `.texty (org_id uuid NOT NULL, user_id text NOT NULL, permissions text)`,
	}
	for _, s := range stmts {
		if err := db.Exec(s).Error; err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _ = db.Exec(`DROP SCHEMA ` + schema + ` CASCADE`).Error })
	return db, schema
}

func insertEmp(t *testing.T, db *gorm.DB, schema, org, user string, status int, perms any) {
	t.Helper()
	if err := db.Exec(`INSERT INTO `+schema+`.employee (org_id, user_id, status_id, permissions) VALUES (?, ?, ?, ?)`,
		org, user, status, perms).Error; err != nil {
		t.Fatal(err)
	}
}

func newGorm(t *testing.T, db *gorm.DB, schema string, mut func(*GormConfig)) *GormSource {
	t.Helper()
	cfg := GormConfig{
		Table:  schema + ".employee",
		Filter: func(q *gorm.DB) *gorm.DB { return q.Where("status_id = ? AND deleted_at IS NULL", 1) },
		Owner: &GormOwner{
			Table:  schema + ".org_users",
			Filter: func(q *gorm.DB) *gorm.DB { return q.Where("access = ?", "owner") },
		},
	}
	if mut != nil {
		mut(&cfg)
	}
	s, err := NewGormSource(db, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestGormSource_Load(t *testing.T) {
	db, sc := gormFixture(t)
	ctx := context.Background()
	insertEmp(t, db, sc, orgA, "u1", 1, `{"modules":{"finance":{"role":"accountant","capabilities":["extra.manage"]}},"permissions":{"menu-1":{"show":true}}}`)
	insertEmp(t, db, sc, orgB, "u1", 1, `{"modules":{"finance":{"role":"owner"}}}`) // өөр org
	insertEmp(t, db, sc, orgA, "u2", 1, nil)                                        // NULL
	insertEmp(t, db, sc, orgA, "u3", 0, `{"modules":{"finance":{"role":"owner"}}}`) // идэвхгүй
	// олон ажлын байр (мөр) → нэгдэл
	insertEmp(t, db, sc, orgA, "u4", 1, `{"modules":{"finance":{"role":"viewer"},"school":{"role":"teacher"}}}`)
	insertEmp(t, db, sc, orgA, "u4", 1, `{"modules":{"finance":{"role":"manager","deny":["invoices.delete"]}}}`)
	insertEmp(t, db, sc, orgA, "evil", 1, `{"owner":true,"modules":{}}`) // JSON owner: үл тооно
	if err := db.Exec(`INSERT INTO `+sc+`.org_users VALUES (?, 'boss', 'owner'), (?, 'member', 'member')`, orgA, orgA).Error; err != nil {
		t.Fatal(err)
	}
	s := newGorm(t, db, sc, nil)

	g, err := s.Load(ctx, orgA, "u1")
	if err != nil {
		t.Fatal(err)
	}
	f := g.Modules["finance"]
	if g.Owner || len(f.Roles) != 1 || f.Roles[0] != "accountant" || len(f.Allow) != 1 || f.Allow[0] != "extra.manage" {
		t.Fatalf("%+v", g)
	}
	// org тусгаарлалт: u1 org B-д өөр эрхтэй
	if gb, _ := s.Load(ctx, orgB, "u1"); gb.Modules["finance"].Roles[0] != "owner" {
		t.Fatalf("org B: %+v", gb)
	}
	if g, err := s.Load(ctx, orgA, "u2"); err != nil || len(g.Modules) != 0 {
		t.Fatalf("NULL: %+v %v", g, err)
	}
	if g, _ := s.Load(ctx, orgA, "u3"); len(g.Modules) != 0 {
		t.Fatalf("Filter хэрэглэгдсэнгүй: %+v", g)
	}
	if g, _ := s.Load(ctx, orgA, "nobody"); len(g.Modules) != 0 || g.Owner {
		t.Fatalf("%+v", g)
	}
	g4, _ := s.Load(ctx, orgA, "u4")
	if len(g4.Modules["finance"].Roles) != 2 || len(g4.Modules["school"].Roles) != 1 || len(g4.Modules["finance"].Deny) != 1 {
		t.Fatalf("олон мөр нэгдээгүй: %+v", g4)
	}
	if ge, _ := s.Load(ctx, orgA, "evil"); ge.Owner {
		t.Fatal("JSON-ийн owner:true-д итгэсэн (privilege escalation)")
	}
	if gb, _ := s.Load(ctx, orgA, "boss"); !gb.Owner {
		t.Fatal("org_users owner танигдсангүй")
	}
	if gm, _ := s.Load(ctx, orgA, "member"); gm.Owner {
		t.Fatal("member owner болсон")
	}
	if gb, _ := s.Load(ctx, orgB, "boss"); gb.Owner {
		t.Fatal("owner org хооронд нэвтэрсэн")
	}
}

func TestGormSource_TrustJSONOwner(t *testing.T) {
	db, sc := gormFixture(t)
	insertEmp(t, db, sc, orgA, "evil", 1, `{"owner":true}`)
	s := newGorm(t, db, sc, func(c *GormConfig) { c.TrustJSONOwner = true; c.Owner = nil })
	if g, err := s.Load(context.Background(), orgA, "evil"); err != nil || !g.Owner {
		t.Fatalf("%+v %v", g, err)
	}
}

func TestGormSource_Errors(t *testing.T) {
	db, sc := gormFixture(t)
	ctx := context.Background()
	insertEmp(t, db, sc, orgA, "bad", 1, `"just a string"`) // JSON хүчинтэй боловч бүтэц буруу
	s := newGorm(t, db, sc, nil)
	if _, err := s.Load(ctx, "", "u"); err == nil {
		t.Fatal("org хоосон")
	}
	if _, err := s.Load(ctx, orgA, ""); err == nil {
		t.Fatal("user хоосон")
	}
	if _, err := s.Load(ctx, orgA, "bad"); err == nil {
		t.Fatal("эвдэрхий бүтэц → алдаа (fail-closed)")
	}
	if _, err := s.ConfiguredModules(ctx, ""); err == nil {
		t.Fatal("org хоосон")
	}
	// буруу org uuid → DB алдаа, дамжина
	if _, err := s.Load(ctx, "not-a-uuid", "u"); err == nil {
		t.Fatal("DB алдаа дамжих ёстой")
	}
	// Policy-тэй: Source алдаа → татгалзана
	p, _ := newTestPolicy(t, s)
	d, err := p.Check(ctx, orgA, "bad", "finance", "invoices.view")
	if err == nil || d.Allowed {
		t.Fatalf("%+v %v", d, err)
	}
}

func TestGormSource_ConfiguredModules(t *testing.T) {
	db, sc := gormFixture(t)
	ctx := context.Background()
	insertEmp(t, db, sc, orgA, "a", 1, `{"modules":{"finance":{"role":"viewer"},"school":{"role":"","capabilities":[]}}}`)
	insertEmp(t, db, sc, orgA, "b", 1, `{"modules":{"school":{"deny":["grades.view"]}}}`)
	insertEmp(t, db, sc, orgA, "c", 1, `{"permissions":{"x":{"show":true}}}`) // modules блоггүй
	insertEmp(t, db, sc, orgA, "d", 0, `{"modules":{"crm":{"role":"x"}}}`)    // идэвхгүй
	insertEmp(t, db, sc, orgB, "e", 1, `{"modules":{"hr":{"role":"x"}}}`)
	s := newGorm(t, db, sc, nil)
	m, err := s.ConfiguredModules(ctx, orgA)
	if err != nil {
		t.Fatal(err)
	}
	if !m["finance"] || !m["school"] || m["crm"] || m["hr"] || len(m) != 2 {
		t.Fatalf("%v", m)
	}

	// Policy + TransitionOrg бодит DB-тэй
	p, _ := newTestPolicy(t, s, WithTransition("school", TransitionOrg))
	if mustAllowed(t, p, "z", "school", "grades.view") {
		t.Fatal("school тохируулсан org-д нээлттэй болохгүй")
	}
	d, _ := p.Check(ctx, orgB, "z", "school", "grades.view")
	if !d.Allowed || d.Reason != ReasonTransition {
		t.Fatalf("org B-д school тохируулаагүй: %+v", d)
	}
}

func TestGormSource_TextColumn_AndCustomColumns(t *testing.T) {
	db, sc := gormFixture(t)
	if err := db.Exec(`INSERT INTO `+sc+`.texty VALUES (?, 'u', ?)`, orgA, `{"modules":{"finance":{"role":"viewer"}}}`).Error; err != nil {
		t.Fatal(err)
	}
	s, err := NewGormSource(db, GormConfig{Table: sc + ".texty", PermissionsColumn: "permissions"})
	if err != nil {
		t.Fatal(err)
	}
	p, _ := newTestPolicy(t, s)
	if !mustAllowed(t, p, "u", "finance", "invoices.view") {
		t.Fatal("text багана")
	}
}

func TestGormSource_ConfigValidation(t *testing.T) {
	db, _ := gormFixture(t)
	bad := []GormConfig{
		{},
		{Table: "a b"},
		{Table: "x; DROP TABLE y"},
		{Table: `"x"`},
		{Table: "a.b.c"},
		{Table: "t", OrgColumn: "org id"},
		{Table: "t", UserColumn: "u;--"},
		{Table: "t", PermissionsColumn: "p) OR (1=1"},
		{Table: "t", Owner: &GormOwner{Table: "bad name"}},
		{Table: "t", Owner: &GormOwner{Table: "o", OrgColumn: "x y"}},
	}
	for i, c := range bad {
		if _, err := NewGormSource(db, c); err == nil {
			t.Errorf("case %d: алдаа хүлээсэн (%+v)", i, c)
		}
	}
	if _, err := NewGormSource(nil, GormConfig{Table: "t"}); err == nil {
		t.Error("nil db")
	}
	s, err := NewGormSource(db, GormConfig{Table: "public.t"})
	if err != nil || s.cfg.OrgColumn != "org_id" || s.cfg.UserColumn != "user_id" || s.cfg.PermissionsColumn != "permissions" {
		t.Fatalf("defaults: %+v %v", s, err)
	}
}

// SQL injection оролт: user id нь параметр тул өгөгдөл төдий.
func TestGormSource_UserIDIsParameter(t *testing.T) {
	db, sc := gormFixture(t)
	insertEmp(t, db, sc, orgA, "victim", 1, `{"modules":{"finance":{"role":"owner"}}}`)
	s := newGorm(t, db, sc, nil)
	for _, evil := range []string{`x' OR '1'='1`, `victim' --`, `%`, `x"; DROP TABLE ` + sc + `.employee; --`} {
		g, err := s.Load(context.Background(), orgA, evil)
		if err != nil || len(g.Modules) != 0 {
			t.Fatalf("%q: %+v %v", evil, g, err)
		}
	}
	if g, _ := s.Load(context.Background(), orgA, "victim"); len(g.Modules) != 1 {
		t.Fatal("хүснэгт устсан эсвэл эвдэрсэн")
	}
	if !strings.HasPrefix(sc, "access_t_") {
		t.Fatal("schema")
	}
}

// Бүрэн хэлхээ: DB → Policy → invalidate.
func TestGormSource_PolicyInvalidate(t *testing.T) {
	db, sc := gormFixture(t)
	ctx := context.Background()
	insertEmp(t, db, sc, orgA, "u", 1, `{"modules":{"finance":{"role":"viewer"}}}`)
	s := newGorm(t, db, sc, nil)
	p, _ := newTestPolicy(t, s)
	if !mustAllowed(t, p, "u", "finance", "invoices.view") || mustAllowed(t, p, "u", "finance", "invoices.edit") {
		t.Fatal("эхний")
	}
	if err := db.Exec(`UPDATE `+sc+`.employee SET permissions = ? WHERE user_id = 'u'`,
		`{"modules":{"finance":{"role":"viewer","deny":["invoices.view"],"capabilities":["invoices.edit"]}}}`).Error; err != nil {
		t.Fatal(err)
	}
	_ = p.Invalidate(ctx, orgA, "u")
	if mustAllowed(t, p, "u", "finance", "invoices.view") || !mustAllowed(t, p, "u", "finance", "invoices.edit") {
		t.Fatal("шинэчлэгдсэн эрх")
	}
}
