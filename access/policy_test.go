package access

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func g(mods map[string]ModuleGrant) Grants { return Grants{Modules: mods} }

func TestNew_Validation(t *testing.T) {
	src := NewMemorySource()
	if _, err := New(nil, src); err == nil {
		t.Fatal("nil registry")
	}
	if _, err := New(NewRegistry(), src); err == nil {
		t.Fatal("хоосон registry")
	}
	if _, err := New(testRegistry(t), nil); err == nil {
		t.Fatal("nil source")
	}
	bad := map[string]Option{
		"ttl":        WithTTL(-1),
		"orgttl":     WithOrgTTL(-1),
		"errttl":     WithErrorTTL(-1),
		"clock":      WithClock(nil),
		"max":        WithMaxEntries(0),
		"timeout":    WithLoadTimeout(0),
		"owner":      WithOwnerPolicy(OwnerPolicy(9)),
		"transition": WithTransition("finance", Transition(9)),
		"gens":       WithGenerations(nil, "", 0),
	}
	for name, o := range bad {
		if _, err := New(testRegistry(t), src, o); err == nil {
			t.Errorf("%s: алдаа хүлээсэн", name)
		}
	}
	if _, err := New(testRegistry(t), src, WithTransition("ghost", TransitionUser)); err == nil {
		t.Error("үл мэдэгдэх модуль")
	}
	plain := SourceFunc(func(context.Context, string, string) (Grants, error) { return Grants{}, nil })
	if _, err := New(testRegistry(t), plain, WithTransition("finance", TransitionOrg)); err == nil || !strings.Contains(err.Error(), "OrgConfigurer") {
		t.Errorf("TransitionOrg нь OrgConfigurer шаардана: %v", err)
	}
	if _, err := New(testRegistry(t), plain, WithTransition("finance", TransitionUser)); err != nil {
		t.Errorf("TransitionUser OrgConfigurer шаардахгүй: %v", err)
	}
}

// Шийдвэрийн матриц.
func TestResolutionMatrix(t *testing.T) {
	type tc struct {
		name   string
		grants Grants
		module string
		caps   []string
		want   bool
		reason Reason
	}
	cases := []tc{
		{"no grants", Grants{}, "finance", []string{"invoices.view"}, false, ReasonNoGrant},
		{"role default allow", g(map[string]ModuleGrant{"finance": {Roles: []string{"viewer"}}}), "finance", []string{"invoices.view"}, true, ReasonGranted},
		{"role default miss", g(map[string]ModuleGrant{"finance": {Roles: []string{"viewer"}}}), "finance", []string{"invoices.create"}, false, ReasonNoGrant},
		{"manager role", g(map[string]ModuleGrant{"finance": {Roles: []string{"manager"}}}), "finance", []string{"invoices.edit"}, true, ReasonGranted},
		{"manager no post", g(map[string]ModuleGrant{"finance": {Roles: []string{"manager"}}}), "finance", []string{"invoices.post"}, false, ReasonNoGrant},
		{"multi-role union", g(map[string]ModuleGrant{"finance": {Roles: []string{"viewer", "accountant"}}}), "finance", []string{"invoices.post"}, true, ReasonGranted},
		{"explicit allow without role", g(map[string]ModuleGrant{"finance": {Allow: []string{"extra.manage"}}}), "finance", []string{"extra.manage"}, true, ReasonGranted},
		{"allow adds to role", g(map[string]ModuleGrant{"finance": {Roles: []string{"viewer"}, Allow: []string{"invoices.post"}}}), "finance", []string{"invoices.post"}, true, ReasonGranted},
		{"deny beats role default", g(map[string]ModuleGrant{"finance": {Roles: []string{"manager"}, Deny: []string{"invoices.edit"}}}), "finance", []string{"invoices.edit"}, false, ReasonDenied},
		{"deny beats explicit allow", g(map[string]ModuleGrant{"finance": {Allow: []string{"extra.manage"}, Deny: []string{"extra.manage"}}}), "finance", []string{"extra.manage"}, false, ReasonDenied},
		{"deny other cap keeps rest", g(map[string]ModuleGrant{"finance": {Roles: []string{"manager"}, Deny: []string{"invoices.edit"}}}), "finance", []string{"invoices.create"}, true, ReasonGranted},
		{"any-of: second cap matches", g(map[string]ModuleGrant{"finance": {Roles: []string{"viewer"}}}), "finance", []string{"invoices.post", "invoices.view"}, true, ReasonGranted},
		{"any-of: first denied second granted", g(map[string]ModuleGrant{"finance": {Roles: []string{"manager"}, Deny: []string{"invoices.edit"}}}), "finance", []string{"invoices.edit", "invoices.view"}, true, ReasonGranted},
		{"grant in other module only", g(map[string]ModuleGrant{"school": {Roles: []string{"teacher"}}}), "finance", []string{"invoices.view"}, false, ReasonNoGrant},
		{"school teacher", g(map[string]ModuleGrant{"school": {Roles: []string{"teacher"}}}), "school", []string{"grades.edit"}, true, ReasonGranted},
		{"unknown role ignored", g(map[string]ModuleGrant{"finance": {Roles: []string{"ghost"}}}), "finance", []string{"invoices.view"}, false, ReasonNoGrant},
		{"unknown allow cap ignored", g(map[string]ModuleGrant{"finance": {Allow: []string{"ghost.cap"}}}), "finance", []string{"ghost.cap"}, false, ReasonUnknown},
		{"unknown module", g(map[string]ModuleGrant{"ghost": {Roles: []string{"x"}}}), "ghost", []string{"a.b"}, false, ReasonUnknown},
		{"unknown cap", g(map[string]ModuleGrant{"finance": {Roles: []string{"owner"}}}), "finance", []string{"nope.nope"}, false, ReasonUnknown},
		{"no caps given", g(map[string]ModuleGrant{"finance": {Roles: []string{"owner"}}}), "finance", nil, false, ReasonUnknown},
		{"unknown + known any-of", g(map[string]ModuleGrant{"finance": {Roles: []string{"viewer"}}}), "finance", []string{"typo.cap", "invoices.view"}, true, ReasonGranted},
		{"owner all", Grants{Owner: true}, "finance", []string{"periods.reopen"}, true, ReasonOwner},
		{"owner ignores deny (default)", Grants{Owner: true, Modules: map[string]ModuleGrant{"finance": {Deny: []string{"periods.reopen"}}}}, "finance", []string{"periods.reopen"}, true, ReasonOwner},
		{"owner unknown module", Grants{Owner: true}, "ghost", []string{"a.b"}, false, ReasonUnknown},
		{"owner unknown cap", Grants{Owner: true}, "finance", []string{"nope.nope"}, false, ReasonUnknown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := NewMemorySource()
			src.Set(orgA, "u", c.grants)
			p, _ := newTestPolicy(t, src)
			d, err := p.Check(context.Background(), orgA, "u", c.module, c.caps...)
			if err != nil {
				t.Fatal(err)
			}
			if d.Allowed != c.want || d.Reason != c.reason {
				t.Fatalf("got %+v, want allowed=%v reason=%s", d, c.want, c.reason)
			}
		})
	}
}

func TestOwnerPolicies(t *testing.T) {
	denied := Grants{Owner: true, Modules: map[string]ModuleGrant{"finance": {Deny: []string{"periods.reopen"}}}}
	cases := []struct {
		name   string
		pol    OwnerPolicy
		grants Grants
		cap    string
		want   bool
	}{
		{"all/deny ignored", OwnerAll, denied, "periods.reopen", true},
		{"deniable/denied", OwnerDeniable, denied, "periods.reopen", false},
		{"deniable/other cap", OwnerDeniable, denied, "invoices.view", true},
		{"none/owner without grants", OwnerNone, Grants{Owner: true}, "invoices.view", false},
		{"none/owner with role", OwnerNone, Grants{Owner: true, Modules: map[string]ModuleGrant{"finance": {Roles: []string{"viewer"}}}}, "invoices.view", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := NewMemorySource()
			src.Set(orgA, "u", c.grants)
			p, _ := newTestPolicy(t, src, WithOwnerPolicy(c.pol))
			if got := mustAllowed(t, p, "u", "finance", c.cap); got != c.want {
				t.Fatalf("got %v want %v", got, c.want)
			}
		})
	}
}

func TestTenantIsolation(t *testing.T) {
	src := NewMemorySource()
	src.Set(orgA, "u", g(map[string]ModuleGrant{"finance": {Roles: []string{"owner"}}}))
	p, _ := newTestPolicy(t, src)
	// ижил user id, өөр org: эрх дамжихгүй.
	d, err := p.Check(context.Background(), orgB, "u", "finance", "invoices.view")
	if err != nil || d.Allowed {
		t.Fatalf("org B-д эрх нэвтэрсэн: %+v %v", d, err)
	}
}

func TestNoIdentity(t *testing.T) {
	p, _ := newTestPolicy(t, NewMemorySource())
	for _, c := range [][2]string{{"", "u"}, {orgA, ""}, {"", ""}} {
		d, err := p.Check(context.Background(), c[0], c[1], "finance", "invoices.view")
		if err != nil || d.Allowed || d.Reason != ReasonNoIdentity {
			t.Fatalf("%v: %+v %v", c, d, err)
		}
	}
	if _, err := p.Snapshot(context.Background(), "", "u"); !errors.Is(err, ErrNoOrg) {
		t.Fatalf("snapshot: %v", err)
	}
}

func TestSourceError_FailClosed(t *testing.T) {
	src := NewMemorySource()
	src.Set(orgA, "u", Grants{Owner: true})
	boom := errors.New("db down")
	src.SetError(boom)
	p, _ := newTestPolicy(t, src)
	d, err := p.Check(context.Background(), orgA, "u", "finance", "invoices.view")
	if !errors.Is(err, boom) || d.Allowed || d.Reason != ReasonError {
		t.Fatalf("%+v %v", d, err)
	}
	if p.Allowed(context.Background(), orgA, "u", "finance", "invoices.view") {
		t.Fatal("Allowed: алдаанд false")
	}
	if _, err := p.Snapshot(context.Background(), orgA, "u"); err == nil {
		t.Fatal("snapshot алдаа")
	}
}

func TestSourcePanic_BecomesError(t *testing.T) {
	src := SourceFunc(func(context.Context, string, string) (Grants, error) { panic("kaboom") })
	p, _ := newTestPolicy(t, src)
	d, err := p.Check(context.Background(), orgA, "u", "finance", "invoices.view")
	if err == nil || d.Allowed || !strings.Contains(err.Error(), "kaboom") {
		t.Fatalf("%+v %v", d, err)
	}
}

func TestTransition_User(t *testing.T) {
	src := NewMemorySource()
	src.Set(orgA, "configured", g(map[string]ModuleGrant{"finance": {Roles: []string{"viewer"}}}))
	src.Set(orgA, "denyonly", g(map[string]ModuleGrant{"finance": {Deny: []string{"invoices.view"}}}))
	src.Set(orgA, "emptyblock", g(map[string]ModuleGrant{"finance": {}}))
	p, _ := newTestPolicy(t, src, WithTransition("finance", TransitionUser))

	// блоггүй хэрэглэгч → нээлттэй
	d, _ := p.Check(context.Background(), orgA, "nobody", "finance", "invoices.post")
	if !d.Allowed || d.Reason != ReasonTransition {
		t.Fatalf("блоггүй: %+v", d)
	}
	// хоосон блок юу ч тохируулаагүй → нээлттэй
	if !mustAllowed(t, p, "emptyblock", "finance", "invoices.post") {
		t.Fatal("хоосон блок нээлттэй байх ёстой")
	}
	// тохируулсан хэрэглэгч: зөвхөн өөрийн эрх
	if mustAllowed(t, p, "configured", "finance", "invoices.post") {
		t.Fatal("тохируулсан хэрэглэгчид transition үйлчлэх ёсгүй")
	}
	if !mustAllowed(t, p, "configured", "finance", "invoices.view") {
		t.Fatal("өөрийн эрх")
	}
	// deny-only блок тохируулсанд тооцогдоно → нээлттэй БИШ
	if mustAllowed(t, p, "denyonly", "finance", "invoices.view") {
		t.Fatal("deny-only хэрэглэгч transition-аар нэвтэрсэн")
	}
	// өөр модуль strict хэвээр
	if mustAllowed(t, p, "nobody", "school", "grades.view") {
		t.Fatal("transition зөвхөн заасан модульд")
	}
}

func TestTransition_Org(t *testing.T) {
	src := NewMemorySource()
	p, clk := newTestPolicy(t, src, WithTransition("finance", TransitionOrg), WithOrgTTL(10_000_000_000))
	ctx := context.Background()

	// org-д хэнд ч эрх тохируулаагүй → бүгдэд нээлттэй
	if !mustAllowed(t, p, "anyone", "finance", "invoices.post") {
		t.Fatal("тохируулаагүй org нээлттэй байх ёстой")
	}
	// хэн нэгэнд эрх тохируулах → org cache-ийг invalidate хийхээс өмнө хуучин утга (≤TTL)
	src.Set(orgA, "boss", g(map[string]ModuleGrant{"finance": {Roles: []string{"owner"}}}))
	if !mustAllowed(t, p, "anyone", "finance", "invoices.post") {
		t.Fatal("cache TTL дотор хуучин")
	}
	if err := p.Invalidate(ctx, orgA, "boss"); err != nil {
		t.Fatal(err)
	}
	if mustAllowed(t, p, "anyone", "finance", "invoices.post") {
		t.Fatal("тохируулсны дараа хаагдах ёстой")
	}
	if !mustAllowed(t, p, "boss", "finance", "invoices.post") {
		t.Fatal("boss өөрийн эрхтэй")
	}
	// өөр org-д нөлөөлөхгүй
	d, _ := p.Check(ctx, orgB, "anyone", "finance", "invoices.post")
	if !d.Allowed || d.Reason != ReasonTransition {
		t.Fatalf("org B: %+v", d)
	}
	// TTL-ээр шинэчлэгдэнэ
	src.Delete(orgA, "boss")
	clk.Advance(11_000_000_000)
	if !mustAllowed(t, p, "anyone", "finance", "invoices.post") {
		t.Fatal("TTL дууссаны дараа дахин нээлттэй")
	}
}

func TestTransition_Org_SourceErrorFailsClosed(t *testing.T) {
	src := NewMemorySource()
	p, _ := newTestPolicy(t, src, WithTransition("finance", TransitionOrg))
	// user Load амжилттай, харин org-configured алдаатай → main fail-open байсан; бид deny.
	orgErr := errors.New("jobs table down")
	wrap := &failingConfigurer{MemorySource: src, err: orgErr}
	p2, _ := newTestPolicy(t, wrap, WithTransition("finance", TransitionOrg))
	d, err := p2.Check(context.Background(), orgA, "u", "finance", "invoices.view")
	if !errors.Is(err, orgErr) || d.Allowed || d.Reason != ReasonError {
		t.Fatalf("%+v %v", d, err)
	}
	_ = p
}

type failingConfigurer struct {
	*MemorySource
	err error
}

func (f *failingConfigurer) ConfiguredModules(context.Context, string) (map[string]bool, error) {
	return nil, f.err
}

func TestSnapshot(t *testing.T) {
	src := NewMemorySource()
	src.Set(orgA, "mgr", g(map[string]ModuleGrant{
		"finance": {Roles: []string{"manager", "viewer"}, Allow: []string{"extra.manage"}, Deny: []string{"invoices.delete"}},
		"school":  {Allow: []string{"grades.view"}},
	}))
	src.Set(orgA, "boss", Grants{Owner: true})
	src.Set(orgA, "empty", g(map[string]ModuleGrant{"finance": {}, "school": {Deny: []string{"grades.view"}}}))
	p, _ := newTestPolicy(t, src)
	ctx := context.Background()

	s, err := p.Snapshot(ctx, orgA, "mgr")
	if err != nil || s.Owner {
		t.Fatal(err, s)
	}
	f := s.Modules["finance"]
	if f.Role != "manager" || len(f.Roles) != 2 || contains(f.Capabilities, "invoices.delete") || !contains(f.Capabilities, "extra.manage") || !contains(f.Capabilities, "invoices.edit") {
		t.Fatalf("%+v", f)
	}
	if !isSorted(f.Capabilities) {
		t.Fatalf("эрэмбэгүй: %v", f.Capabilities)
	}
	sc := s.Modules["school"]
	if sc.Role != "" || sc.Roles == nil || len(sc.Capabilities) != 1 {
		t.Fatalf("role-гүй explicit grant: %+v", sc)
	}

	o, _ := p.Snapshot(ctx, orgA, "boss")
	if !o.Owner || len(o.Modules) != 2 || o.Modules["finance"].Role != "owner" || len(o.Modules["finance"].Capabilities) != 8 {
		t.Fatalf("%+v", o)
	}
	// хоосон/deny-only блок → модуль харагдахгүй, nil биш map
	e, _ := p.Snapshot(ctx, orgA, "empty")
	if e.Modules == nil || len(e.Modules) != 0 {
		t.Fatalf("%+v", e)
	}
	n, _ := p.Snapshot(ctx, orgA, "nobody")
	if n.Modules == nil || len(n.Modules) != 0 || n.Owner {
		t.Fatalf("%+v", n)
	}
}

func TestSnapshot_OwnerDeniable(t *testing.T) {
	src := NewMemorySource()
	src.Set(orgA, "boss", Grants{Owner: true, Modules: map[string]ModuleGrant{"finance": {Deny: []string{"periods.reopen"}}}})
	p, _ := newTestPolicy(t, src, WithOwnerPolicy(OwnerDeniable))
	s, _ := p.Snapshot(context.Background(), orgA, "boss")
	if contains(s.Modules["finance"].Capabilities, "periods.reopen") || len(s.Modules["finance"].Capabilities) != 7 {
		t.Fatalf("%v", s.Modules["finance"].Capabilities)
	}
}

func TestVerify(t *testing.T) {
	p, _ := newTestPolicy(t, NewMemorySource())
	if err := p.Verify("finance", "invoices.view", "invoices.post"); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		m    string
		caps []string
	}{{"ghost", []string{"a.b"}}, {"finance", nil}, {"finance", []string{"nope.nope"}}, {"finance", []string{"invoices.view", "typo"}}} {
		if err := p.Verify(c.m, c.caps...); err == nil {
			t.Errorf("%v: алдаа хүлээсэн", c)
		}
	}
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func isSorted(l []string) bool {
	for i := 1; i < len(l); i++ {
		if l[i-1] > l[i] {
			return false
		}
	}
	return true
}
