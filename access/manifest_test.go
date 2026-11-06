package access

import (
	"errors"
	"strings"
	"testing"
)

func TestManifestValidate_OK(t *testing.T) {
	if err := financeManifest().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestManifestValidate_Errors(t *testing.T) {
	long := strings.Repeat("a", MaxKeyLen+1)
	cases := map[string]func(m *Manifest){
		"module key uppercase":    func(m *Manifest) { m.Key = "Finance" },
		"module key empty":        func(m *Manifest) { m.Key = "" },
		"module key dot":          func(m *Manifest) { m.Key = "fin.ance" },
		"module key too long":     func(m *Manifest) { m.Key = long },
		"label empty":             func(m *Manifest) { m.Label = "" },
		"role key bad":            func(m *Manifest) { m.Roles[0].Key = "Own er" },
		"role dup":                func(m *Manifest) { m.Roles[1].Key = "owner" },
		"role label empty":        func(m *Manifest) { m.Roles[0].Label = "" },
		"no capabilities":         func(m *Manifest) { m.Capabilities = nil },
		"cap key bad":             func(m *Manifest) { m.Capabilities[0].Key = "Invoices.View" },
		"cap key trailing dot":    func(m *Manifest) { m.Capabilities[0].Key = "invoices." },
		"cap key wildcard":        func(m *Manifest) { m.Capabilities[0].Key = "*" },
		"cap dup":                 func(m *Manifest) { m.Capabilities[1].Key = m.Capabilities[0].Key },
		"cap label empty":         func(m *Manifest) { m.Capabilities[0].Label = "" },
		"default role missing":    func(m *Manifest) { m.Capabilities[0].DefaultRoles = []string{"ghost"} },
		"default role duplicated": func(m *Manifest) { m.Capabilities[0].DefaultRoles = []string{"owner", "owner"} },
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			m := financeManifest().clone()
			mut(&m)
			err := m.Validate()
			if err == nil {
				t.Fatal("алдаа хүлээсэн")
			}
			if !errors.Is(err, ErrInvalidManifest) {
				t.Fatalf("ErrInvalidManifest биш: %v", err)
			}
		})
	}
}

func TestManifestValidate_JoinsAllErrors(t *testing.T) {
	m := financeManifest().clone()
	m.Key = "Bad"
	m.Label = ""
	err := m.Validate()
	if err == nil || strings.Count(err.Error(), "\n") < 1 {
		t.Fatalf("бүх зөрчлийг нэгтгэх ёстой: %v", err)
	}
}

func TestDefaultCapabilitiesFor(t *testing.T) {
	got := financeManifest().DefaultCapabilitiesFor("viewer")
	if len(got) != 1 || got[0] != "invoices.view" {
		t.Fatalf("viewer: %v", got)
	}
	if got := financeManifest().DefaultCapabilitiesFor("nope"); got == nil || len(got) != 0 {
		t.Fatalf("үл мэдэгдэх role: %v", got)
	}
}

func TestRegistry(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(financeManifest()); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(financeManifest()); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("давхар: %v", err)
	}
	bad := financeManifest()
	bad.Key = "X"
	if err := r.Register(bad); err == nil {
		t.Fatal("буруу manifest")
	}
	if r.Len() != 1 {
		t.Fatalf("len=%d", r.Len())
	}
	// Get/All нь хуулбар: гадна мутаци дотогшоо нөлөөлөхгүй.
	m, ok := r.Get("finance")
	if !ok {
		t.Fatal("Get")
	}
	m.Capabilities[0].Key = "hacked"
	m.Capabilities[0].DefaultRoles[0] = "hacked"
	m2, _ := r.Get("finance")
	if m2.Capabilities[0].Key != "invoices.view" || m2.Capabilities[0].DefaultRoles[0] != "owner" {
		t.Fatal("Registry дотоод төлөв мутацлагдсан")
	}
	if _, ok := r.Get("nope"); ok {
		t.Fatal("үл мэдэгдэх")
	}
	_ = r.Register(schoolManifest())
	all := r.All()
	if len(all) != 2 || all[0].Key != "finance" || all[1].Key != "school" {
		t.Fatalf("All эрэмбэ: %v", all)
	}
}

func TestRegistry_Mutation_ThenCaller(t *testing.T) {
	// Register нь caller-ийн slice-г хадгалахгүй (clone).
	r := NewRegistry()
	m := financeManifest()
	if err := r.Register(m); err != nil {
		t.Fatal(err)
	}
	m.Capabilities[0].Key = "changed"
	got, _ := r.Get("finance")
	if got.Capabilities[0].Key != "invoices.view" {
		t.Fatal("caller-ийн мутаци Registry-д нэвтэрсэн")
	}
}

func TestRegistryFreeze(t *testing.T) {
	r := NewRegistry()
	_ = r.Register(financeManifest())
	r.Freeze()
	if !r.Frozen() {
		t.Fatal("Frozen")
	}
	if err := r.Register(schoolManifest()); !errors.Is(err, ErrFrozen) {
		t.Fatalf("хөлдсөн: %v", err)
	}
	// New нь бас хөлдөөнө.
	r2 := NewRegistry()
	_ = r2.Register(financeManifest())
	if _, err := New(r2, NewMemorySource()); err != nil {
		t.Fatal(err)
	}
	if err := r2.Register(schoolManifest()); !errors.Is(err, ErrFrozen) {
		t.Fatalf("New дараа: %v", err)
	}
}

func TestRegisterAll_StopsAtFirstError(t *testing.T) {
	r := NewRegistry()
	bad := schoolManifest()
	bad.Key = "BAD"
	if err := r.RegisterAll(financeManifest(), bad, schoolManifest()); err == nil {
		t.Fatal("алдаа")
	}
	if r.Len() != 1 {
		t.Fatalf("len=%d", r.Len())
	}
}
