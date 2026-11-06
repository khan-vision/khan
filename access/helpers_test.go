package access

import (
	"context"
	"sync"
	"testing"
	"time"
)

const (
	orgA = "11111111-1111-1111-1111-111111111111"
	orgB = "22222222-2222-2222-2222-222222222222"
)

// financeManifest — main-ийн finance manifest-ийн хураангуй.
func financeManifest() Manifest {
	all := []string{"owner", "manager", "accountant", "viewer"}
	mgr := []string{"owner", "manager", "accountant"}
	post := []string{"owner", "accountant"}
	return Manifest{
		Key: "finance", Label: "Санхүү",
		Roles: []Role{{Key: "owner", Label: "Эзэн"}, {Key: "manager", Label: "Менежер"}, {Key: "accountant", Label: "Нягтлан"}, {Key: "viewer", Label: "Үзэгч"}},
		Capabilities: []Capability{
			{Key: "invoices.view", Label: "Нэхэмжлэх харах", DefaultRoles: all},
			{Key: "invoices.create", Label: "Нэхэмжлэх үүсгэх", DefaultRoles: mgr},
			{Key: "invoices.edit", Label: "Нэхэмжлэх засах", DefaultRoles: mgr},
			{Key: "invoices.post", Label: "Нэхэмжлэх батлах", DefaultRoles: post},
			{Key: "invoices.delete", Label: "Нэхэмжлэх устгах", DefaultRoles: []string{"owner", "manager"}},
			{Key: "reports.view", Label: "Тайлан харах", DefaultRoles: mgr},
			{Key: "periods.reopen", Label: "Тайлант үе нээх", DefaultRoles: []string{"owner"}},
			{Key: "extra.manage", Label: "Нэмэлт"}, // role-д анхдагчгүй
		},
	}
}

func schoolManifest() Manifest {
	return Manifest{
		Key: "school", Label: "Сургууль",
		Roles: []Role{{Key: "teacher", Label: "Багш"}},
		Capabilities: []Capability{
			{Key: "grades.view", Label: "Дүн харах", DefaultRoles: []string{"teacher"}},
			{Key: "grades.edit", Label: "Дүн засах", DefaultRoles: []string{"teacher"}},
			{Key: "enrollment.finance.edit", Label: "Төлбөр засах"},
		},
	}
}

func testRegistry(t testing.TB) *Registry {
	t.Helper()
	r := NewRegistry()
	if err := r.RegisterAll(financeManifest(), schoolManifest()); err != nil {
		t.Fatal(err)
	}
	return r
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *fakeClock { return &fakeClock{t: time.Date(2026, 11, 6, 9, 0, 0, 0, time.UTC)} }
func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// newTestPolicy — MemorySource + fake clock.
func newTestPolicy(t testing.TB, src Source, opts ...Option) (*Policy, *fakeClock) {
	t.Helper()
	clk := newClock()
	p, err := New(testRegistry(t), src, append([]Option{WithClock(clk.Now)}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	return p, clk
}

func mustAllowed(t testing.TB, p *Policy, user, module string, caps ...string) bool {
	t.Helper()
	d, err := p.Check(context.Background(), orgA, user, module, caps...)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	return d.Allowed
}
