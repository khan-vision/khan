package access

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/khan-vision/khan/cache"
)

// cache.MemoryGens нь Generations-ийг хангана (compile-time).
var _ Generations = (*cache.MemoryGens)(nil)

func TestCache_TTL(t *testing.T) {
	src := NewMemorySource()
	src.Set(orgA, "u", g(map[string]ModuleGrant{"finance": {Roles: []string{"viewer"}}}))
	p, clk := newTestPolicy(t, src, WithTTL(5*time.Second))

	if !mustAllowed(t, p, "u", "finance", "invoices.view") {
		t.Fatal("эхний")
	}
	// DB-д эрх өөрчлөгдсөн ч TTL дотор cache-ээс
	src.Set(orgA, "u", Grants{})
	clk.Advance(4 * time.Second)
	if !mustAllowed(t, p, "u", "finance", "invoices.view") || src.Loads() != 1 {
		t.Fatalf("TTL дотор cache hit байх ёстой, loads=%d", src.Loads())
	}
	clk.Advance(2 * time.Second) // 6с > 5с
	if mustAllowed(t, p, "u", "finance", "invoices.view") {
		t.Fatal("TTL дууссан → шинэ эрх (байхгүй)")
	}
	if src.Loads() != 2 {
		t.Fatalf("loads=%d", src.Loads())
	}
}

func TestCache_TTLZeroDisables(t *testing.T) {
	src := NewMemorySource()
	p, _ := newTestPolicy(t, src, WithTTL(0))
	for i := 0; i < 3; i++ {
		mustAllowed(t, p, "u", "finance", "invoices.view")
	}
	if src.Loads() != 3 || p.CacheSize() != 0 {
		t.Fatalf("loads=%d size=%d", src.Loads(), p.CacheSize())
	}
}

func TestInvalidate_UserAndOrg(t *testing.T) {
	src := NewMemorySource()
	for _, u := range []string{"u1", "u2"} {
		src.Set(orgA, u, g(map[string]ModuleGrant{"finance": {Roles: []string{"viewer"}}}))
	}
	src.Set(orgB, "u1", g(map[string]ModuleGrant{"finance": {Roles: []string{"viewer"}}}))
	p, _ := newTestPolicy(t, src)
	ctx := context.Background()
	check := func(org, u string) bool {
		d, err := p.Check(ctx, org, u, "finance", "invoices.view")
		if err != nil {
			t.Fatal(err)
		}
		return d.Allowed
	}
	check(orgA, "u1")
	check(orgA, "u2")
	check(orgB, "u1")

	// бүгдийн эрхийг хураана, гэхдээ cache хүртэл хүчинтэй
	for _, k := range [][2]string{{orgA, "u1"}, {orgA, "u2"}, {orgB, "u1"}} {
		src.Set(k[0], k[1], Grants{})
	}
	if !check(orgA, "u1") || !check(orgA, "u2") {
		t.Fatal("cache")
	}
	if err := p.Invalidate(ctx, orgA, "u1"); err != nil {
		t.Fatal(err)
	}
	if check(orgA, "u1") {
		t.Fatal("u1 invalidate хийгдсэн")
	}
	if !check(orgA, "u2") || !check(orgB, "u1") {
		t.Fatal("бусад cache-д үлдэх ёстой")
	}
	if err := p.InvalidateOrg(ctx, orgA); err != nil {
		t.Fatal(err)
	}
	if check(orgA, "u2") {
		t.Fatal("org бүхэлдээ invalidate")
	}
	if !check(orgB, "u1") {
		t.Fatal("org B нөлөөлөгдөх ёсгүй")
	}
}

func TestErrorTTL_NegativeCache(t *testing.T) {
	src := NewMemorySource()
	src.SetError(errors.New("down"))
	p, clk := newTestPolicy(t, src, WithErrorTTL(time.Second))
	for i := 0; i < 5; i++ {
		if _, err := p.Check(context.Background(), orgA, "u", "finance", "invoices.view"); err == nil {
			t.Fatal("алдаа")
		}
	}
	if src.Loads() != 1 {
		t.Fatalf("алдаа 1с cache-лэгдэх ёстой, loads=%d", src.Loads())
	}
	// Source сэргэнэ; алдааны TTL дууссаны дараа сэргэнэ (хугацаанаас өмнө биш).
	src.SetError(nil)
	src.Set(orgA, "u", Grants{Owner: true})
	if mustAllowedErr(p) {
		t.Fatal("алдааны TTL дотор")
	}
	clk.Advance(2 * time.Second)
	if !mustAllowed(t, p, "u", "finance", "invoices.view") {
		t.Fatal("сэргэсэн")
	}
	// ErrorTTL=0 → алдаа cache хийхгүй
	src2 := NewMemorySource()
	src2.SetError(errors.New("down"))
	p2, _ := newTestPolicy(t, src2, WithErrorTTL(0))
	for i := 0; i < 3; i++ {
		_, _ = p2.Check(context.Background(), orgA, "u", "finance", "invoices.view")
	}
	if src2.Loads() != 3 {
		t.Fatalf("loads=%d", src2.Loads())
	}
}

func mustAllowedErr(p *Policy) bool {
	return p.Allowed(context.Background(), orgA, "u", "finance", "invoices.view")
}

// 50 goroutine → 1 Load.
func TestSingleflight_50Goroutines(t *testing.T) {
	release := make(chan struct{})
	var loads atomic.Int32
	src := SourceFunc(func(ctx context.Context, org, user string) (Grants, error) {
		loads.Add(1)
		<-release
		return g(map[string]ModuleGrant{"finance": {Roles: []string{"viewer"}}}), nil
	})
	p, _ := newTestPolicy(t, src)

	const n = 50
	var wg, started sync.WaitGroup
	wg.Add(n)
	started.Add(n)
	results := make([]bool, n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			started.Done()
			results[i] = p.Allowed(context.Background(), orgA, "u", "finance", "invoices.view")
		}()
	}
	started.Wait()
	time.Sleep(50 * time.Millisecond) // бүгд flight-д нэгдэх завсар
	close(release)
	wg.Wait()
	if loads.Load() != 1 {
		t.Fatalf("Load %d удаа дуудагдсан (1 байх ёстой)", loads.Load())
	}
	for i, r := range results {
		if !r {
			t.Fatalf("goroutine %d татгалзагдсан", i)
		}
	}
}

// Ачаалж байх үед invalidate болсон бол хуучин үр дүн cache-д ороход болохгүй.
func TestInvalidateDuringLoad_NoStaleCache(t *testing.T) {
	entered := make(chan struct{}, 4)
	release := make(chan struct{})
	var phase atomic.Int32
	src := SourceFunc(func(ctx context.Context, org, user string) (Grants, error) {
		if phase.Load() == 0 {
			entered <- struct{}{}
			<-release
			return g(map[string]ModuleGrant{"finance": {Roles: []string{"viewer"}}}), nil // хуучин эрх
		}
		return Grants{}, nil // шинэ эрх: хоосон
	})
	p, _ := newTestPolicy(t, src)
	ctx := context.Background()

	done := make(chan bool, 1)
	go func() { done <- p.Allowed(ctx, orgA, "u", "finance", "invoices.view") }()
	<-entered
	// эрхийг хураагаад invalidate — ачаалалт дундаа
	phase.Store(1)
	if err := p.Invalidate(ctx, orgA, "u"); err != nil {
		t.Fatal(err)
	}
	// invalidate-ийн дараах шинэ дуудагч хуучин flight-д нэгдэхгүй, шинэ эрхийг харна
	if p.Allowed(ctx, orgA, "u", "finance", "invoices.view") {
		t.Fatal("invalidate-ийн дараа хуучин flight-д нэгдсэн")
	}
	close(release)
	<-done // хуучин дуудагч хуучин утга авч болно
	// гэхдээ хуучин үр дүн cache-д ороогүй
	if p.Allowed(ctx, orgA, "u", "finance", "invoices.view") {
		t.Fatal("хуучин үр дүн cache-д орсон")
	}
}

func TestCallerContextCancel(t *testing.T) {
	release := make(chan struct{})
	src := SourceFunc(func(ctx context.Context, org, user string) (Grants, error) {
		<-release
		return g(map[string]ModuleGrant{"finance": {Roles: []string{"viewer"}}}), nil
	})
	p, _ := newTestPolicy(t, src)
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		_, err := p.Check(ctx, orgA, "u", "finance", "invoices.view")
		errc <- err
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	if err := <-errc; !errors.Is(err, context.Canceled) {
		t.Fatalf("ctx цуцлагдсан үед буцах ёстой: %v", err)
	}
	close(release) // ачаалалт дуусч, дараагийн дуудагчид cache-д бэлэн
	time.Sleep(20 * time.Millisecond)
	if !p.Allowed(context.Background(), orgA, "u", "finance", "invoices.view") {
		t.Fatal("цуцлагдсан дуудагчийн ачаалсан үр дүн ашиглагдах ёстой")
	}
}

func TestMaxEntries_Eviction(t *testing.T) {
	src := NewMemorySource()
	p, clk := newTestPolicy(t, src, WithMaxEntries(10), WithTTL(time.Minute))
	for i := 0; i < 10; i++ {
		mustAllowed(t, p, string(rune('a'+i)), "finance", "invoices.view")
	}
	if p.CacheSize() != 10 {
		t.Fatalf("size=%d", p.CacheSize())
	}
	clk.Advance(2 * time.Minute) // бүгд хугацаа дууссан
	mustAllowed(t, p, "zz", "finance", "invoices.view")
	if p.CacheSize() != 1 {
		t.Fatalf("хугацаа дууссаныг цэвэрлэх ёстой, size=%d", p.CacheSize())
	}
	for i := 0; i < 50; i++ {
		mustAllowed(t, p, string(rune('A'+i)), "finance", "invoices.view")
	}
	if p.CacheSize() > 10 {
		t.Fatalf("дээд хэмжээнээс хэтэрсэн: %d", p.CacheSize())
	}
}

// ─── Generations (олон instance) ───

// Хоёр Policy нэг MemoryGens-ийг хуваалцана = хоёр instance.
func TestGenerations_CrossInstance(t *testing.T) {
	src := NewMemorySource()
	src.Set(orgA, "u", g(map[string]ModuleGrant{"finance": {Roles: []string{"viewer"}}}))
	gens := cache.NewMemoryGens()
	clk := newClock()
	mk := func() *Policy {
		p, err := New(testRegistry(t), src, WithClock(clk.Now), WithTTL(time.Hour),
			WithGenerations(gens, "t", 500*time.Millisecond))
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	p1, p2 := mk(), mk()
	ctx := context.Background()

	if !p2.Allowed(ctx, orgA, "u", "finance", "invoices.view") {
		t.Fatal("p2 эхний")
	}
	loads := src.Loads()

	// p1 дээр эрхийг хураагаад invalidate
	src.Set(orgA, "u", Grants{})
	if err := p1.Invalidate(ctx, orgA, "u"); err != nil {
		t.Fatal(err)
	}
	// p2: genInterval дотор шалгахгүй (cache hit) — хэт олон Gen дуудлага хийхгүй
	clk.Advance(100 * time.Millisecond)
	if !p2.Allowed(ctx, orgA, "u", "finance", "invoices.view") || src.Loads() != loads {
		t.Fatal("interval дотор Gen шалгахгүй байх ёстой")
	}
	// interval-аас хойш p2 generation зөрснийг мэдэж дахин ачаална
	clk.Advance(500 * time.Millisecond)
	if p2.Allowed(ctx, orgA, "u", "finance", "invoices.view") {
		t.Fatal("p2 хуучин эрхээ хадгалсан")
	}
	// өөрчлөгдөөгүй бол дахин ачаалахгүй
	loads = src.Loads()
	clk.Advance(time.Second)
	p2.Allowed(ctx, orgA, "u", "finance", "invoices.view")
	clk.Advance(time.Second)
	p2.Allowed(ctx, orgA, "u", "finance", "invoices.view")
	if src.Loads() != loads {
		t.Fatalf("generation өөрчлөгдөөгүй мөртлөө дахин ачаалсан: %d→%d", loads, src.Loads())
	}

	// org-түвшний invalidate бас хүрнэ
	src.Set(orgA, "u", g(map[string]ModuleGrant{"finance": {Roles: []string{"viewer"}}}))
	if err := p1.InvalidateOrg(ctx, orgA); err != nil {
		t.Fatal(err)
	}
	clk.Advance(time.Second)
	if !p2.Allowed(ctx, orgA, "u", "finance", "invoices.view") {
		t.Fatal("InvalidateOrg p2-д хүрээгүй")
	}
}

type failingGens struct {
	cache.MemoryGens
	genErr, bumpErr error
}

func (f *failingGens) Gen(ctx context.Context, keys []string) ([]uint64, error) {
	if f.genErr != nil {
		return nil, f.genErr
	}
	return f.MemoryGens.Gen(ctx, keys)
}
func (f *failingGens) Bump(ctx context.Context, keys []string) error {
	if f.bumpErr != nil {
		return f.bumpErr
	}
	return f.MemoryGens.Bump(ctx, keys)
}

func TestGenerations_Errors(t *testing.T) {
	src := NewMemorySource()
	src.Set(orgA, "u", g(map[string]ModuleGrant{"finance": {Roles: []string{"viewer"}}}))
	fg := &failingGens{}
	clk := newClock()
	p, err := New(testRegistry(t), src, WithClock(clk.Now), WithTTL(time.Hour), WithGenerations(fg, "", time.Second))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	p.Allowed(ctx, orgA, "u", "finance", "invoices.view")
	l := src.Loads()

	// Bump алдаа: локал цэвэрлэгдэнэ, алдаа буцна
	src.Set(orgA, "u", Grants{})
	fg.bumpErr = errors.New("redis down")
	if err := p.Invalidate(ctx, orgA, "u"); err == nil {
		t.Fatal("bump алдаа буцах ёстой")
	}
	if p.Allowed(ctx, orgA, "u", "finance", "invoices.view") {
		t.Fatal("локал invalidate bump алдаанаас үл хамаарна")
	}

	// Gen уншигдахгүй: cache-д итгэхгүй, Source-оос шууд (хуучин эрх үлдэхгүй)
	src.Set(orgA, "u", g(map[string]ModuleGrant{"finance": {Roles: []string{"viewer"}}}))
	fg.bumpErr = nil
	_ = p.Invalidate(ctx, orgA, "u")
	p.Allowed(ctx, orgA, "u", "finance", "invoices.view")
	fg.genErr = errors.New("redis down")
	clk.Advance(2 * time.Second)
	src.Set(orgA, "u", Grants{})
	if p.Allowed(ctx, orgA, "u", "finance", "invoices.view") {
		t.Fatal("Gen унасан үед хуучин эрх ашиглагдсан")
	}
	// Gen унасан үед үр дүн cache-лэгдэхгүй (бүр дуудлагад Source)
	before := src.Loads()
	p.Allowed(ctx, orgA, "u", "finance", "invoices.view")
	if src.Loads() != before+1 {
		t.Fatalf("Gen уншигдахгүй үед cache-лэж болохгүй: %d→%d", before, src.Loads())
	}
	_ = l
}
