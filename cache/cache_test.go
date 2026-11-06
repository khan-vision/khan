package cache

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func newCache(t *testing.T, cfg Config) *Cache {
	t.Helper()
	c, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func key(org, res string, parts ...string) Key {
	return Key{OrgID: org, Resource: res, Parts: parts}
}

func val(s string) func(context.Context) ([]byte, error) {
	return func(context.Context) ([]byte, error) { return []byte(s), nil }
}

func TestNewValidation(t *testing.T) {
	if _, err := New(Config{Prefix: "Bad:Prefix"}); err == nil {
		t.Fatal("буруу prefix зөвшөөрөгдсөн")
	}
	// L2 өгөөд хуваалцсан GenStore өгөөгүй → алдаа
	l2only := struct{ Store }{newFlaky()}
	if _, err := New(Config{L2: l2only}); err == nil {
		t.Fatal("L2 + gens-гүй зөвшөөрөгдсөн")
	}
	// L2 нь GenStore бол тэрийг ашиглана
	fs := newFlaky()
	c := newCache(t, Config{L2: fs})
	_ = c.Bump(bg, "o", "r")
	if fs.gens["khan:gen:o:r"] != 1 {
		t.Fatalf("gens L2-д биш: %v", fs.gens)
	}
	// Тодорхой Gens өгвөл тэр
	g := NewMemoryGens()
	if _, err := New(Config{L2: l2only, Gens: g}); err == nil {
		t.Fatal("хуваалцсан L2 + MemoryGens хориотой байх ёстой")
	}
	c = newCache(t, Config{L2: l2only, Gens: g, AllowLocalGens: true})
	_ = c.Bump(bg, "o", "r")
	if v, _ := g.Gen(bg, []string{"khan:gen:o:r"}); v[0] != 1 {
		t.Fatal(v)
	}
	if c.DefaultTTL() != DefaultTTL {
		t.Fatal(c.DefaultTTL())
	}
}

func TestVersionFormatAndBump(t *testing.T) {
	c := newCache(t, Config{})
	v, err := c.Version(bg, "org", "a", "b")
	if err != nil || gensOf(v) != "g0.g0" {
		t.Fatalf("%q %v", v, err)
	}
	_ = c.Bump(bg, "org", "a", "a") // давхар нэр нэг л удаа
	_ = c.Bump(bg, "org", "a")
	_ = c.Bump(bg, "org", "b")
	v, _ = c.Version(bg, "org", "a", "b")
	if gensOf(v) != "g2.g1" {
		t.Fatalf("%q", v)
	}
	if v, _ = c.Version(bg, "org", "b", "a"); gensOf(v) != "g1.g2" {
		t.Fatalf("дараалал: %q", v)
	}
	if v, _ = c.Version(bg, "other", "a"); gensOf(v) != "g0" {
		t.Fatalf("org тусгаарлалт: %q", v)
	}
	if err := c.Bump(bg, "org"); err != nil {
		t.Fatal("resource-гүй Bump no-op")
	}
	for _, bad := range []func() error{
		func() error { return c.Bump(bg, "", "a") },
		func() error { return c.Bump(bg, "o", "") },
		func() error { _, err := c.Version(bg, "", "a"); return err },
		func() error { _, err := c.Version(bg, "o"); return err },
		func() error { _, err := c.Version(bg, "o", ""); return err },
	} {
		if !errors.Is(bad(), ErrInvalidKey) {
			t.Fatal("ErrInvalidKey хүлээсэн")
		}
	}
}

func TestEscape(t *testing.T) {
	cases := map[string]string{
		"finance.invoice": "finance.invoice",
		"0a1b-2c_3.d":     "0a1b-2c_3.d",
		"a:b":             "a%3Ab",
		"A":               "%41",
		"100%":            "100%25",
		"a b/c":           "a%20b%2Fc",
		"өө":              "%D3%A9%D3%A9",
		"":                "",
	}
	for in, want := range cases {
		if got := escape(in); got != want {
			t.Errorf("escape(%q)=%q, want %q", in, got, want)
		}
	}
}

func TestKeyInjection(t *testing.T) {
	fs := newFlaky()
	c := newCache(t, Config{L2: fs})
	resolve := func(k Key) string {
		r, err := c.Resolve(bg, k)
		if err != nil {
			t.Fatal(err)
		}
		return r.key
	}
	a := resolve(Key{OrgID: "a:b", Resource: "c", Parts: []string{"p"}})
	b := resolve(Key{OrgID: "a", Resource: "b:c", Parts: []string{"p"}})
	if a == b {
		t.Fatal("':' injection-аар өөр org/resource-ийн түлхүүртэй мөргөлдөв")
	}
	for _, k := range []string{a, b} {
		// prefix:v1:org:resource:gens:hash → яг 5 ':' (escape-лэгдсэн хэсэгт ':' байхгүй)
		if n := strings.Count(k, ":"); n != 5 {
			t.Errorf("%q: ':' тоо=%d", k, n)
		}
	}
	// Gen түлхүүр: prefix:gen:org:resource — 3 ':'
	_ = c.Bump(bg, "x:y", "r:s")
	for k := range fs.gens {
		if k != "khan:gen:x%3Ay:r%3As" {
			t.Errorf("gen key=%q", k)
		}
	}
	// parts хилийн мөргөлдөөн
	if resolve(key("o", "r", "a", "bc")) == resolve(key("o", "r", "ab", "c")) {
		t.Fatal("parts хил ялгагдсангүй")
	}
	if resolve(key("o", "r")) == resolve(key("o", "r", "")) {
		t.Fatal("хоосон part ялгагдсангүй")
	}
	// ../ org
	if k := resolve(key("../../x", "r")); strings.Contains(k, "/") {
		t.Fatal(k)
	}
}

func TestResolveDependsOn(t *testing.T) {
	c := newCache(t, Config{})
	k1 := Key{OrgID: "o", Resource: "inv", DependsOn: []string{"cust", "item"}, Parts: []string{"x"}}
	k2 := Key{OrgID: "o", Resource: "inv", DependsOn: []string{"item", "cust", "cust", "inv"}, Parts: []string{"x"}}
	r1, _ := c.Resolve(bg, k1)
	r2, _ := c.Resolve(bg, k2)
	if r1.key != r2.key || gensOf(r1.Version) != "g0.g0.g0" {
		t.Fatalf("%v %v", r1, r2)
	}
	// DependsOn өөрчлөгдвөл (gens ижил тоо байсан ч) өөр түлхүүр
	r3, _ := c.Resolve(bg, Key{OrgID: "o", Resource: "inv", DependsOn: []string{"cust", "zzz"}, Parts: []string{"x"}})
	if r3.key == r1.key {
		t.Fatal("DependsOn хэшэнд ороогүй")
	}
	// Dependency bump → түлхүүр өөрчлөгдөнө
	_ = c.Bump(bg, "o", "cust")
	r4, _ := c.Resolve(bg, k1)
	if r4.key == r1.key || gensOf(r4.Version) != "g0.g1.g0" {
		t.Fatalf("%v", r4)
	}
	if _, err := c.Resolve(bg, Key{OrgID: "o", Resource: "inv", DependsOn: []string{""}}); !errors.Is(err, ErrInvalidKey) {
		t.Fatal(err)
	}
	if _, err := c.Resolve(bg, Key{Resource: "inv"}); !errors.Is(err, ErrInvalidKey) {
		t.Fatal(err)
	}
}

func TestRememberHitMissBump(t *testing.T) {
	c := newCache(t, Config{})
	var calls atomic.Int32
	fn := func(context.Context) ([]byte, error) { calls.Add(1); return []byte(fmt.Sprint("v", calls.Load())), nil }
	k := key("o", "r", "list")
	v, hit, err := c.Remember(bg, k, 0, fn)
	if err != nil || hit || string(v) != "v1" {
		t.Fatalf("%s %v %v", v, hit, err)
	}
	v, hit, _ = c.Remember(bg, k, 0, fn)
	if !hit || string(v) != "v1" || calls.Load() != 1 {
		t.Fatalf("%s %v calls=%d", v, hit, calls.Load())
	}
	// өөр org — өөр entry
	v, hit, _ = c.Remember(bg, key("o2", "r", "list"), 0, fn)
	if hit || string(v) != "v2" {
		t.Fatalf("org тусгаарлалт: %s %v", v, hit)
	}
	// Bump → шинэ generation → miss
	_ = c.Bump(bg, "o", "r")
	v, hit, _ = c.Remember(bg, k, 0, fn)
	if hit || string(v) != "v3" {
		t.Fatalf("bump-ийн дараа: %s %v", v, hit)
	}
	// o2-ийн entry bump-д автсангүй
	if _, hit, _ = c.Remember(bg, key("o2", "r", "list"), 0, fn); !hit {
		t.Fatal("өөр org-ийн bump o2-ийг хүчингүй болгосон")
	}
	st := c.Stats()
	if st.Hits != 2 || st.Misses != 3 || st.Sets != 3 || st.Bumps != 1 {
		t.Fatalf("%+v", st)
	}
}

func TestRememberTTLFakeClock(t *testing.T) {
	clk := newClock()
	c := newCache(t, Config{Now: clk.Now, DefaultTTL: time.Minute})
	var calls atomic.Int32
	fn := func(context.Context) ([]byte, error) { calls.Add(1); return []byte("v"), nil }
	k := key("o", "r")
	c.Remember(bg, k, 0, fn)
	clk.Advance(59 * time.Second)
	if _, hit, _ := c.Remember(bg, k, 0, fn); !hit {
		t.Fatal("DefaultTTL дотор hit байх ёстой")
	}
	clk.Advance(2 * time.Second)
	if _, hit, _ := c.Remember(bg, k, 0, fn); hit || calls.Load() != 2 {
		t.Fatal("TTL дууссаны дараа miss байх ёстой")
	}
	// тодорхой ttl DefaultTTL-ийг дарна
	k2 := key("o", "r", "short")
	c.Remember(bg, k2, 5*time.Second, fn)
	clk.Advance(6 * time.Second)
	if _, hit, _ := c.Remember(bg, k2, 5*time.Second, fn); hit {
		t.Fatal("ttl=5с дууссан")
	}
	if e1, e2 := c.Epoch(time.Minute), c.Epoch(0); e1 != e2 {
		t.Fatal("Epoch(0) = Epoch(DefaultTTL)")
	}
	before := c.Epoch(time.Minute)
	clk.Advance(time.Minute)
	if c.Epoch(time.Minute) == before {
		t.Fatal("Epoch ttl өнгөрөхөд өөрчлөгдөх ёстой")
	}
}

func TestRememberSingleflight(t *testing.T) {
	c := newCache(t, Config{})
	var calls atomic.Int32
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	fn := func(context.Context) ([]byte, error) {
		calls.Add(1)
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
		return []byte("shared"), nil
	}
	const n = 50
	var wg sync.WaitGroup
	results := make([]string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, _, err := c.Remember(bg, key("o", "r", "hot"), 0, fn)
			if err != nil {
				t.Error(err)
			}
			results[i] = string(v)
		}()
	}
	<-started
	time.Sleep(50 * time.Millisecond) // бусад нь flight-д нэгдэх хугацаа
	close(release)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("fn %d удаа дуудагдлаа, 1 байх ёстой", calls.Load())
	}
	for i, r := range results {
		if r != "shared" {
			t.Fatalf("results[%d]=%q", i, r)
		}
	}
	if c.Stats().Coalesced == 0 {
		t.Error("Coalesced тоологдсонгүй")
	}
}

func TestRememberDifferentKeysNotCoalesced(t *testing.T) {
	c := newCache(t, Config{})
	var calls atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.Remember(bg, key("o", "r", fmt.Sprint(i)), 0, func(context.Context) ([]byte, error) { calls.Add(1); return []byte("x"), nil })
		}()
	}
	wg.Wait()
	if calls.Load() != 10 {
		t.Fatalf("calls=%d", calls.Load())
	}
}

func TestRememberFnErrorNotCached(t *testing.T) {
	c := newCache(t, Config{})
	var calls atomic.Int32
	fn := func(context.Context) ([]byte, error) {
		if calls.Add(1) == 1 {
			return nil, errBoom
		}
		return []byte("ok"), nil
	}
	if _, _, err := c.Remember(bg, key("o", "r"), 0, fn); !errors.Is(err, errBoom) {
		t.Fatal(err)
	}
	v, hit, err := c.Remember(bg, key("o", "r"), 0, fn)
	if err != nil || hit || string(v) != "ok" {
		t.Fatalf("%s %v %v", v, hit, err)
	}
}

func TestRememberMaxEntryBytes(t *testing.T) {
	c := newCache(t, Config{MaxEntryBytes: 10})
	var calls atomic.Int32
	big := func(context.Context) ([]byte, error) { calls.Add(1); return []byte(strings.Repeat("x", 11)), nil }
	for i := 0; i < 2; i++ {
		v, hit, err := c.Remember(bg, key("o", "r", "big"), 0, big)
		if err != nil || hit || len(v) != 11 {
			t.Fatal(len(v), hit, err)
		}
	}
	if calls.Load() != 2 || c.Stats().TooLarge != 2 || c.l1.Len() != 0 {
		t.Fatalf("calls=%d %+v len=%d", calls.Load(), c.Stats(), c.l1.Len())
	}
	// яг хязгаар дээр хадгалагдана
	c.Remember(bg, key("o", "r", "ok"), 0, val("1234567890"))
	if _, hit, _ := c.Remember(bg, key("o", "r", "ok"), 0, val("1234567890")); !hit {
		t.Fatal("хязгаар дахь хариу хадгалагдах ёстой")
	}
}

func TestRememberFailOpenStore(t *testing.T) {
	fs := newFlaky()
	logs := 0
	c := newCache(t, Config{L2: fs, Logf: func(string, ...any) { logs++ }})
	var calls atomic.Int32
	fn := func(context.Context) ([]byte, error) { calls.Add(1); return []byte("fresh"), nil }

	// L2 Get + Set бүтэлгүй → fn дуудагдаж утга буцна (алдаа биш)
	fs.set(func(s *flakyStore) { s.failGet, s.failSet = true, true })
	v, hit, err := c.Remember(bg, key("o", "r"), 0, fn)
	if err != nil || hit || string(v) != "fresh" {
		t.Fatalf("%s %v %v", v, hit, err)
	}
	// L1 дээр хадгалагдсан тул L2 муу ч дараагийн дуудлага hit
	if _, hit, _ = c.Remember(bg, key("o", "r"), 0, fn); !hit {
		t.Fatal("L1 hit байх ёстой")
	}
	if c.Stats().Errors < 2 || logs < 2 {
		t.Fatalf("алдаа тоологдоогүй: %+v logs=%d", c.Stats(), logs)
	}

	// Gen уншигдахгүй → cache алгасна: ХЭЗЭЭ Ч cache-ээс уншихгүй, fn дуудна
	fs.set(func(s *flakyStore) { s.failGet, s.failSet, s.failGen = false, false, true })
	before := calls.Load()
	for i := 0; i < 3; i++ {
		v, hit, err = c.Remember(bg, key("o", "r"), 0, fn)
		if err != nil || hit || string(v) != "fresh" {
			t.Fatalf("%s %v %v", v, hit, err)
		}
	}
	if calls.Load() != before+3 {
		t.Fatal("gen алдаатай үед cache-ээс уншсан")
	}
	if _, err := c.Version(bg, "o", "r"); err == nil {
		t.Fatal("Version алдаа буцаах ёстой (crud cache алгасна)")
	}
	if _, err := c.Resolve(bg, key("o", "r")); err == nil {
		t.Fatal("Resolve алдаа буцаах ёстой")
	}
	// fn-ийн алдаа fail-open үед ч буцна
	if _, _, err := c.Remember(bg, key("o", "r"), 0, func(context.Context) ([]byte, error) { return nil, errBoom }); !errors.Is(err, errBoom) {
		t.Fatal(err)
	}
	// Bump алдаа буцна
	fs.set(func(s *flakyStore) { s.failBump = true })
	if err := c.Bump(bg, "o", "r"); err == nil {
		t.Fatal("Bump алдаа буцаах ёстой")
	}
	// Буруу Key → fail-open (fn дуудагдана)
	if v, _, err := c.Remember(bg, Key{Resource: "r"}, 0, fn); err != nil || string(v) != "fresh" {
		t.Fatal(err)
	}
}

func TestRememberL2PromotionAndSharing(t *testing.T) {
	fs := newFlaky()
	c1 := newCache(t, Config{L2: fs})
	c2 := newCache(t, Config{L2: fs}) // өөр instance, ижил L2 + gens
	var calls atomic.Int32
	fn := func(context.Context) ([]byte, error) { calls.Add(1); return []byte("v"), nil }
	c1.Remember(bg, key("o", "r"), 0, fn)
	if _, hit, _ := c2.Remember(bg, key("o", "r"), 0, fn); !hit || calls.Load() != 1 {
		t.Fatal("c2 нь L2-оос унших ёстой")
	}
	if c2.Stats().L2Hits != 1 || c2.l1.Len() != 1 {
		t.Fatalf("L1-д дэвшээгүй: %+v", c2.Stats())
	}
	// c1 bump → c2 шууд шинэ generation харна (L1-д хуучин утга байсан ч)
	_ = c1.Bump(bg, "o", "r")
	if _, hit, _ := c2.Remember(bg, key("o", "r"), 0, fn); hit || calls.Load() != 2 {
		t.Fatal("c2 нь c1-ийн bump-ийг шууд харах ёстой")
	}
}

func TestRememberLeaderContextErrorRetry(t *testing.T) {
	c := newCache(t, Config{})
	leaderCtx, cancel := context.WithCancel(bg)
	entered := make(chan struct{})
	proceed := make(chan struct{})
	var calls atomic.Int32
	fn := func(ctx context.Context) ([]byte, error) {
		if calls.Add(1) == 1 {
			close(entered)
			<-proceed
			return nil, ctx.Err() // удирдагчийн ctx цуцлагдсан
		}
		return []byte("recovered"), nil
	}
	var lerr error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _, lerr = c.Remember(leaderCtx, key("o", "r"), 0, fn)
	}()
	<-entered
	var fv []byte
	var ferr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		fv, _, ferr = c.Remember(bg, key("o", "r"), 0, fn)
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	close(proceed)
	wg.Wait()
	if !errors.Is(lerr, context.Canceled) {
		t.Fatalf("удирдагч: %v", lerr)
	}
	if ferr != nil || string(fv) != "recovered" {
		t.Fatalf("дагагч өөрийн ctx-оор дахин оролдох ёстой: %q %v", fv, ferr)
	}
}

func TestRememberResolvedZeroValue(t *testing.T) {
	c := newCache(t, Config{})
	v, hit, err := c.RememberResolved(bg, Resolved{}, 0, val("x"))
	if err != nil || hit || string(v) != "x" {
		t.Fatal(string(v), hit, err)
	}
	r, _ := c.Resolve(bg, key("o", "r"))
	c.RememberResolved(bg, r, 0, val("a"))
	if v, hit, _ := c.RememberResolved(bg, r, 0, val("b")); !hit || string(v) != "a" {
		t.Fatal(string(v), hit)
	}
}

func TestRememberRace(t *testing.T) {
	c := newCache(t, Config{L2: newFlaky()})
	var wg sync.WaitGroup
	for g := 0; g < 20; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				k := key("o", "r", fmt.Sprint(i%5))
				c.Remember(bg, k, time.Second, val("v"))
				if i%10 == 0 {
					_ = c.Bump(bg, "o", "r")
				}
				c.Stats()
			}
		}()
	}
	wg.Wait()
}

func TestETag(t *testing.T) {
	a := ETag("g1.g2", "list", "p1")
	if !strings.HasPrefix(a, `W/"g1.g2-`) || !strings.HasSuffix(a, `"`) || len(a) != len(`W/"g1.g2-"`)+16 {
		t.Fatalf("формат: %s", a)
	}
	if a != ETag("g1.g2", "list", "p1") {
		t.Fatal("тогтмол биш")
	}
	for _, other := range []string{ETag("g1.g3", "list", "p1"), ETag("g1.g2", "list", "p2"), ETag("g1.g2", "listp1"), ETag("g1.g2", "list", "p1", "")} {
		if other == a {
			t.Fatal("ETag мөргөлдөв")
		}
	}
	if ETag("g1.g2", "a", "bc") == ETag("g1.g2", "ab", "c") {
		t.Fatal("хил ялгагдсангүй")
	}
}

func TestETagMatch(t *testing.T) {
	e := ETag("g1", "x")
	strong := strings.TrimPrefix(e, "W/")
	cases := []struct {
		hdr  string
		want bool
	}{
		{e, true},
		{strong, true}, // weak харьцуулалт
		{`"other", ` + e, true},
		{e + `,"o"`, true},
		{`  ` + e + `  `, true},
		{`"other"`, false},
		{"", false},
		{"*", false}, // байхгүй мөрт 304 өгөхгүй
		{`"other", *`, false},
		{`W/"g1-0000"`, false},
	}
	for _, tc := range cases {
		if got := ETagMatch(tc.hdr, e); got != tc.want {
			t.Errorf("ETagMatch(%q)=%v, want %v", tc.hdr, got, tc.want)
		}
	}
	if ETagMatch(e, "") {
		t.Fatal("хоосон etag")
	}
}

var saltRe = regexp.MustCompile(`^s[0-9a-f]{8}\.`)

// gensOf — Version-оос salt/boot nonce угтварыг хасна ("s1a2b3c4d.g1.g0" → "g1.g0").
func gensOf(v string) string { return saltRe.ReplaceAllString(v, "") }

// Process доторх generation-д boot nonce: restart (шинэ Cache) бүр өөр Version → хуучин ETag-аар
// 304 гарахгүй. Salt нь deploy-ийн хувилбарыг Version-д оруулна.
func TestSaltAndBootNonce(t *testing.T) {
	a := newCache(t, Config{})
	b := newCache(t, Config{})
	va, _ := a.Version(bg, "o", "r")
	vb, _ := b.Version(bg, "o", "r")
	if !saltRe.MatchString(va) || va == vb || gensOf(va) != gensOf(vb) {
		t.Fatalf("boot nonce: %q %q", va, vb)
	}
	// Хуваалцсан gens (Redis-тэй адил) + Salt: instance-ууд ижил Version, Salt өөр бол өөр.
	g := NewMemoryGens()
	shared := sharedGens{g}
	x := newCache(t, Config{Gens: shared, Salt: "build-1"})
	y := newCache(t, Config{Gens: shared, Salt: "build-1"})
	z := newCache(t, Config{Gens: shared, Salt: "build-2"})
	vx, _ := x.Version(bg, "o", "r")
	vy, _ := y.Version(bg, "o", "r")
	vz, _ := z.Version(bg, "o", "r")
	if vx != vy || vx == vz || !saltRe.MatchString(vx) {
		t.Fatalf("salt: %q %q %q", vx, vy, vz)
	}
	none := newCache(t, Config{Gens: shared})
	if v, _ := none.Version(bg, "o", "r"); v != "g0" {
		t.Fatalf("salt-гүй хуваалцсан gens: %q", v)
	}
}

// sharedGens — *MemoryGens биш GenStore (хуваалцсан store-ийн адил: boot nonce нэмэгдэхгүй).
type sharedGens struct{ g *MemoryGens }

func (s sharedGens) Gen(ctx context.Context, keys []string) ([]uint64, error) {
	return s.g.Gen(ctx, keys)
}
func (s sharedGens) Bump(ctx context.Context, keys []string) error { return s.g.Bump(ctx, keys) }
