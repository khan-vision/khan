package access

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func TestParseGrants_MainFormat(t *testing.T) {
	// main-ийн view_employee.permissions: modules блок + хуучин цэсний `permissions` блок.
	raw := `{"modules":{"finance":{"role":"accountant","capabilities":["extra.manage"]}},
	          "permissions":{"abc":{"show":true,"c":true}}}`
	g, err := ParseGrants([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	want := ModuleGrant{Roles: []string{"accountant"}, Allow: []string{"extra.manage"}}
	if !reflect.DeepEqual(g.Modules["finance"], want) || g.Owner {
		t.Fatalf("%+v", g)
	}
}

func TestParseGrants_Extensions(t *testing.T) {
	g, err := ParseGrants([]byte(`{"owner":true,"modules":{"finance":{"role":"viewer","roles":["manager","viewer"," "],"allow":["a.b"],"capabilities":["a.b","c.d"],"deny":["invoices.edit"]}}}`))
	if err != nil {
		t.Fatal(err)
	}
	m := g.Modules["finance"]
	if !g.Owner || !reflect.DeepEqual(m.Roles, []string{"manager", "viewer"}) ||
		!reflect.DeepEqual(m.Allow, []string{"a.b", "c.d"}) || !reflect.DeepEqual(m.Deny, []string{"invoices.edit"}) {
		t.Fatalf("%+v", g)
	}
}

func TestParseGrants_EmptyAndBad(t *testing.T) {
	for _, in := range []string{"", "  ", "null"} {
		g, err := ParseGrants([]byte(in))
		if err != nil || g.Owner || len(g.Modules) != 0 {
			t.Fatalf("%q: %+v %v", in, g, err)
		}
	}
	if _, err := ParseGrants([]byte(`{bad`)); err == nil {
		t.Fatal("эвдэрхий JSON")
	}
	if _, err := ParseGrants([]byte(`{"modules":{"x":"str"}}`)); err == nil {
		t.Fatal("буруу төрөл")
	}
}

func TestModuleGrantJSONRoundTrip(t *testing.T) {
	in := Grants{Modules: map[string]ModuleGrant{
		"finance": {Roles: []string{"manager", "viewer"}, Allow: []string{"extra.manage"}, Deny: []string{"invoices.edit"}},
		"school":  {Roles: []string{"teacher"}},
	}}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]map[string]map[string]any
	_ = json.Unmarshal(b, &raw)
	if raw["modules"]["school"]["role"] != "teacher" || raw["modules"]["school"]["roles"] != nil {
		t.Fatalf("ганц role main-тэй нийцэх ёстой: %s", b)
	}
	out, err := ParseGrants(b)
	if err != nil || !reflect.DeepEqual(in, out) {
		t.Fatalf("round trip: %s %v", b, err)
	}
}

func TestGrantsMerge(t *testing.T) {
	a := Grants{Modules: map[string]ModuleGrant{"finance": {Roles: []string{"viewer"}, Deny: []string{"x.y"}}}}
	b := Grants{Owner: true, Modules: map[string]ModuleGrant{"finance": {Roles: []string{"manager"}, Allow: []string{"extra.manage"}}, "school": {Roles: []string{"teacher"}}}}
	m := a.Merge(b)
	if !m.Owner || len(m.Modules) != 2 {
		t.Fatalf("%+v", m)
	}
	f := m.Modules["finance"]
	if !reflect.DeepEqual(f.Roles, []string{"manager", "viewer"}) || !reflect.DeepEqual(f.Allow, []string{"extra.manage"}) || !reflect.DeepEqual(f.Deny, []string{"x.y"}) {
		t.Fatalf("%+v", f)
	}
	// оролт өөрчлөгдөөгүй
	if len(a.Modules["finance"].Roles) != 1 || a.Owner {
		t.Fatal("оролт мутацлагдсан")
	}
}

func TestMemorySource(t *testing.T) {
	s := NewMemorySource()
	ctx := context.Background()
	s.Set(orgA, "u1", Grants{Modules: map[string]ModuleGrant{"finance": {Roles: []string{"viewer"}}}})
	g, err := s.Load(ctx, orgA, "u1")
	if err != nil || len(g.Modules["finance"].Roles) != 1 {
		t.Fatalf("%+v %v", g, err)
	}
	// org тусгаарлалт
	if g, _ := s.Load(ctx, orgB, "u1"); len(g.Modules) != 0 {
		t.Fatal("өөр org-ийн эрх харагдсан")
	}
	if _, err := s.Load(ctx, "", "u1"); !errors.Is(err, ErrNoOrg) {
		t.Fatalf("org хоосон: %v", err)
	}
	// буцсан утгыг мутацлахад дотоод төлөв өөрчлөгдөхгүй
	g.Modules["finance"] = ModuleGrant{}
	if g2, _ := s.Load(ctx, orgA, "u1"); len(g2.Modules["finance"].Roles) != 1 {
		t.Fatal("дотоод төлөв мутацлагдсан")
	}
	mods, _ := s.ConfiguredModules(ctx, orgA)
	if !mods["finance"] || mods["school"] {
		t.Fatalf("%v", mods)
	}
	if m, _ := s.ConfiguredModules(ctx, orgB); len(m) != 0 {
		t.Fatalf("org B: %v", m)
	}
	s.Delete(orgA, "u1")
	if m, _ := s.ConfiguredModules(ctx, orgA); len(m) != 0 {
		t.Fatalf("устгасны дараа: %v", m)
	}
	s.SetError(errors.New("boom"))
	if _, err := s.Load(ctx, orgA, "u1"); err == nil {
		t.Fatal("SetError")
	}
	if _, err := s.ConfiguredModules(ctx, orgA); err == nil {
		t.Fatal("SetError configured")
	}
}

func TestMerge_Sources(t *testing.T) {
	ctx := context.Background()
	a, b := NewMemorySource(), NewMemorySource()
	a.Set(orgA, "u", Grants{Modules: map[string]ModuleGrant{"finance": {Roles: []string{"viewer"}}}})
	b.Set(orgA, "u", Grants{Modules: map[string]ModuleGrant{"school": {Allow: []string{"grades.view"}}}})
	m := Merge(a, nil, b)
	g, err := m.Load(ctx, orgA, "u")
	if err != nil || len(g.Modules) != 2 {
		t.Fatalf("%+v %v", g, err)
	}
	oc, ok := m.(OrgConfigurer)
	if !ok {
		t.Fatal("OrgConfigurer байх ёстой")
	}
	mods, err := oc.ConfiguredModules(ctx, orgA)
	if err != nil || !mods["finance"] || !mods["school"] {
		t.Fatalf("%v %v", mods, err)
	}
	// аль нэг нь алдаа → бүхэлдээ алдаа
	b.SetError(errors.New("down"))
	if _, err := m.Load(ctx, orgA, "u"); err == nil {
		t.Fatal("алдаа дамжих ёстой")
	}
	if _, err := oc.ConfiguredModules(ctx, orgA); err == nil {
		t.Fatal("configured алдаа дамжих ёстой")
	}
	// OrgConfigurer-гүй Source-уудаас бол OrgConfigurer болохгүй
	plain := Merge(SourceFunc(func(context.Context, string, string) (Grants, error) { return Grants{}, nil }))
	if _, ok := plain.(OrgConfigurer); ok {
		t.Fatal("OrgConfigurer байж болохгүй")
	}
}
