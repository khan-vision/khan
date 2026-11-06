package access

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"gorm.io/gorm"
)

func TestUsersWithCapability_Memory(t *testing.T) {
	src := NewMemorySource()
	src.Set(orgA, "viewer", g(map[string]ModuleGrant{"finance": {Roles: []string{"viewer"}}}))
	src.Set(orgA, "acct", g(map[string]ModuleGrant{"finance": {Roles: []string{"accountant"}}}))
	src.Set(orgA, "acct-denied", g(map[string]ModuleGrant{"finance": {Roles: []string{"accountant"}, Deny: []string{"invoices.post"}}}))
	src.Set(orgA, "allow-only", g(map[string]ModuleGrant{"finance": {Allow: []string{"invoices.post"}}}))
	src.Set(orgA, "boss", Grants{Owner: true})
	src.Set(orgA, "boss-denied", Grants{Owner: true, Modules: map[string]ModuleGrant{"finance": {Deny: []string{"invoices.post"}}}})
	src.Set(orgB, "other", g(map[string]ModuleGrant{"finance": {Roles: []string{"accountant"}}}))
	ctx := context.Background()

	cases := []struct {
		name string
		opts []Option
		want []string
	}{
		{"default owner-all", nil, []string{"acct", "allow-only", "boss", "boss-denied"}},
		{"owner deniable", []Option{WithOwnerPolicy(OwnerDeniable)}, []string{"acct", "allow-only", "boss"}},
		{"owner none", []Option{WithOwnerPolicy(OwnerNone)}, []string{"acct", "allow-only"}},
		// шилжилтийн горим ҮЛЧЛЭХГҮЙ: олгоогүй хүн жагсаалтад орохгүй
		{"transition ignored", []Option{WithTransition("finance", TransitionUser)}, []string{"acct", "allow-only", "boss", "boss-denied"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, _ := newTestPolicy(t, src, c.opts...)
			got, err := p.UsersWithCapability(ctx, orgA, "finance", "invoices.post")
			if err != nil || !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %v err %v want %v", got, err, c.want)
			}
		})
	}

	p, _ := newTestPolicy(t, src)
	if got, err := p.UsersWithCapability(ctx, "33333333-3333-3333-3333-333333333333", "finance", "invoices.post"); err != nil || got == nil || len(got) != 0 {
		t.Fatalf("хоосон org: %v %v", got, err)
	}
	if _, err := p.UsersWithCapability(ctx, "", "finance", "invoices.post"); !errors.Is(err, ErrNoOrg) {
		t.Fatal(err)
	}
	if _, err := p.UsersWithCapability(ctx, orgA, "finance", "nope"); err == nil {
		t.Fatal("үл мэдэгдэх cap")
	}
	if _, err := p.UsersWithCapability(ctx, orgA, "nope", "x"); err == nil {
		t.Fatal("үл мэдэгдэх модуль")
	}
	src.SetError(errors.New("db down"))
	if _, err := p.UsersWithCapability(ctx, orgA, "finance", "invoices.post"); err == nil {
		t.Fatal("Source алдаа")
	}
	if _, err := src.ListUsers(ctx, ""); !errors.Is(err, ErrNoOrg) {
		t.Fatal(err)
	}
}

type noListSource struct{}

func (noListSource) Load(context.Context, string, string) (Grants, error) { return Grants{}, nil }

type dupSource struct{}

func (dupSource) Load(context.Context, string, string) (Grants, error) { return Grants{}, nil }
func (dupSource) ListUsers(context.Context, string) ([]UserGrants, error) {
	gr := g(map[string]ModuleGrant{"finance": {Roles: []string{"accountant"}}})
	return []UserGrants{{"b", gr}, {"", gr}, {"a", gr}, {"b", gr}}, nil
}

func TestUsersWithCapability_NoLister_Dedup(t *testing.T) {
	p, _ := newTestPolicy(t, noListSource{})
	if _, err := p.UsersWithCapability(context.Background(), orgA, "finance", "invoices.post"); !errors.Is(err, ErrNoLister) {
		t.Fatal(err)
	}
	p, _ = newTestPolicy(t, dupSource{})
	got, err := p.UsersWithCapability(context.Background(), orgA, "finance", "invoices.post")
	if err != nil || !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("%v %v", got, err)
	}
}

func TestGormSource_ListUsers(t *testing.T) {
	db, sc := gormFixture(t)
	ctx := context.Background()
	insertEmp(t, db, sc, orgA, "u1", 1, `{"modules":{"finance":{"role":"viewer"}}}`)
	insertEmp(t, db, sc, orgA, "u1", 1, `{"modules":{"finance":{"role":"accountant"}}}`) // олон мөр
	insertEmp(t, db, sc, orgA, "u2", 1, nil)
	insertEmp(t, db, sc, orgA, "u3", 0, `{"modules":{"finance":{"role":"owner"}}}`) // Filter-ээр хасагдана
	insertEmp(t, db, sc, orgB, "u4", 1, `{"modules":{"finance":{"role":"owner"}}}`) // өөр org
	insertEmp(t, db, sc, orgA, "evil", 1, `{"owner":true}`)
	if err := db.Exec(`INSERT INTO `+sc+`.employee (org_id, user_id, status_id, permissions) VALUES (?, NULL, 1, '{"modules":{"finance":{"role":"owner"}}}')`, orgA).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO `+sc+`.org_users VALUES (?, 'boss', 'owner'), (?, 'boss', 'owner'), (?, 'u1', 'owner'), (?, 'mem', 'member'), (?, 'boss2', 'owner')`,
		orgA, orgA, orgA, orgA, orgB).Error; err != nil {
		t.Fatal(err)
	}
	s := newGorm(t, db, sc, nil)
	us, err := s.ListUsers(ctx, orgA)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	byID := map[string]Grants{}
	for _, u := range us {
		ids = append(ids, u.UserID)
		byID[u.UserID] = u.Grants
	}
	if want := []string{"boss", "evil", "u1", "u2"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("ids=%v want %v", ids, want)
	}
	if !byID["boss"].Owner || !byID["u1"].Owner || byID["evil"].Owner || byID["u2"].Owner {
		t.Fatalf("owner: %+v", byID)
	}
	if len(byID["u1"].Modules["finance"].Roles) != 2 {
		t.Fatalf("олон мөр нэгдээгүй: %+v", byID["u1"])
	}

	// Policy-тэй: user_id → UsersWithCapability
	p, _ := New(testRegistry(t), s)
	got, err := p.UsersWithCapability(ctx, orgA, "finance", "invoices.post")
	if err != nil || !reflect.DeepEqual(got, []string{"boss", "u1"}) {
		t.Fatalf("%v %v", got, err)
	}

	if _, err := s.ListUsers(ctx, ""); !errors.Is(err, ErrNoOrg) {
		t.Fatal(err)
	}
	// TrustJSONOwner + Owner-гүй
	s2 := newGorm(t, db, sc, func(c *GormConfig) { c.TrustJSONOwner = true; c.Owner = nil })
	us, _ = s2.ListUsers(ctx, orgA)
	trusted := false
	for _, u := range us {
		if u.UserID == "evil" && u.Grants.Owner {
			trusted = true
		}
	}
	if !trusted {
		t.Fatal("TrustJSONOwner үл тооцогдлоо")
	}
	// эвдэрхий JSON → алдаа
	insertEmp(t, db, sc, orgA, "bad", 1, `"str"`)
	if _, err := s.ListUsers(ctx, orgA); err == nil {
		t.Fatal("эвдэрхий JSON алдаа өгөөгүй")
	}
	// DB алдаа: хүснэгт алга болсон
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.ListUsers(cctx, orgA); err == nil {
		t.Fatal("цуцлагдсан ctx")
	}
	if err := db.Exec(`DROP TABLE ` + sc + `.org_users`).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := newGorm(t, db, sc, func(c *GormConfig) {
		c.Filter = func(q *gorm.DB) *gorm.DB { return q.Where("user_id <> 'bad'") }
	}).ListUsers(ctx, orgA); err == nil {
		t.Fatal("owner хүснэгт алга → алдаа")
	}
}
