package rediscache

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/khan-vision/khan/cache"
	"github.com/redis/go-redis/v9"
)

var bg = context.Background()

func newClient(addr string) *redis.Client {
	return redis.NewClient(&redis.Options{
		Addr: addr, MaxRetries: -1, DialTimeout: 200 * time.Millisecond,
		ReadTimeout: 200 * time.Millisecond, WriteTimeout: 200 * time.Millisecond,
	})
}

func setup(t *testing.T) (*miniredis.Miniredis, *Store) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := newClient(mr.Addr())
	t.Cleanup(func() { _ = rdb.Close() })
	return mr, New(rdb)
}

func TestStoreGetSet(t *testing.T) {
	mr, s := setup(t)
	if _, ok, err := s.Get(bg, "k"); ok || err != nil {
		t.Fatal("байхгүй түлхүүр: ok=false, алдаагүй")
	}
	if err := s.Set(bg, "k", []byte("v"), 1500*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if got, ok, err := s.Get(bg, "k"); !ok || err != nil || string(got) != "v" {
		t.Fatalf("%q %v %v", got, ok, err)
	}
	if ttl := mr.TTL("k"); ttl != 1500*time.Millisecond {
		t.Fatalf("PX ms-ээр тавигдаагүй: %v", ttl)
	}
	mr.FastForward(2 * time.Second)
	if _, ok, _ := s.Get(bg, "k"); ok {
		t.Fatal("TTL дууссан")
	}
	// ttl<=0 → хугацаагүй; <1мс → 1мс
	_ = s.Set(bg, "forever", []byte("f"), 0)
	if mr.TTL("forever") != 0 {
		t.Fatal("хугацаагүй байх ёстой")
	}
	_ = s.Set(bg, "tiny", []byte("t"), time.Microsecond)
	if mr.TTL("tiny") != time.Millisecond {
		t.Fatalf("tiny ttl=%v", mr.TTL("tiny"))
	}
	// binary-safe
	bin := []byte{0, 1, 2, 255, '\n'}
	_ = s.Set(bg, "bin", bin, time.Minute)
	if got, _, _ := s.Get(bg, "bin"); string(got) != string(bin) {
		t.Fatal("binary эвдэрсэн")
	}
}

func TestStoreGens(t *testing.T) {
	mr, s := setup(t)
	if g, err := s.Gen(bg, nil); err != nil || len(g) != 0 {
		t.Fatal(g, err)
	}
	if err := s.Bump(bg, nil); err != nil {
		t.Fatal(err)
	}
	g, err := s.Gen(bg, []string{"a", "b"})
	if err != nil || g[0] != 0 || g[1] != 0 {
		t.Fatal(g, err)
	}
	_ = s.Bump(bg, []string{"a", "c"})
	_ = s.Bump(bg, []string{"a"})
	g, _ = s.Gen(bg, []string{"a", "b", "c"})
	if g[0] != 2 || g[1] != 0 || g[2] != 1 {
		t.Fatal(g)
	}
	if v, _ := mr.Get("a"); v != "2" {
		t.Fatalf("INCR хэлбэр: %q", v)
	}
	// эвдэрсэн утга → алдаа (буруу generation-аар уншихгүй)
	_ = mr.Set("bad", "abc")
	if _, err := s.Gen(bg, []string{"a", "bad"}); err == nil {
		t.Fatal("эвдэрсэн generation алдаа өгөх ёстой")
	}
	// Эвдэрсэн түлхүүр өөр төрлийн (list) → WRONGTYPE алдаа
	_, _ = mr.Push("lst", "x")
	if _, err := s.Gen(bg, []string{"a", "lst"}); err == nil {
		t.Fatal("WRONGTYPE алдаа")
	}
}

func TestTwoInstancesShareRedis(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb1, rdb2 := newClient(mr.Addr()), newClient(mr.Addr())
	t.Cleanup(func() { _ = rdb1.Close(); _ = rdb2.Close() })
	c1, err := cache.New(cache.Config{L2: New(rdb1)})
	if err != nil {
		t.Fatal(err)
	}
	c2, err := cache.New(cache.Config{L2: New(rdb2)})
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	data := "v1"
	fn := func(context.Context) ([]byte, error) { calls.Add(1); return []byte(data), nil }
	k := cache.Key{OrgID: "org", Resource: "finance.invoice", Parts: []string{"list"}}

	v, hit, _ := c1.Remember(bg, k, time.Minute, fn)
	if hit || string(v) != "v1" {
		t.Fatal(string(v), hit)
	}
	// c2 нь c1-ийн entry-г Redis-ээс авна
	if v, hit, _ = c2.Remember(bg, k, time.Minute, fn); !hit || string(v) != "v1" || calls.Load() != 1 {
		t.Fatalf("c2: %s %v calls=%d", v, hit, calls.Load())
	}
	// c1 бичилт хийж bump → c2 (L1-д хуучин утгатай байсан ч) ШУУД шинийг харна
	data = "v2"
	if err := c1.Bump(bg, "org", "finance.invoice"); err != nil {
		t.Fatal(err)
	}
	if v1, _ := c2.Version(bg, "org", "finance.invoice"); v1 != "g1" {
		t.Fatalf("c2 version=%s", v1)
	}
	v, hit, _ = c2.Remember(bg, k, time.Minute, fn)
	if hit || string(v) != "v2" {
		t.Fatalf("c2 bump-ийг харсангүй: %s hit=%v", v, hit)
	}
	// c1 мөн c2-ийн шинэ entry-г харна
	if v, hit, _ = c1.Remember(bg, k, time.Minute, fn); !hit || string(v) != "v2" || calls.Load() != 2 {
		t.Fatalf("c1: %s %v calls=%d", v, hit, calls.Load())
	}
	// Redis-д generation түлхүүр байна, entry-ийн түлхүүр v1:... TTL-тэй
	if got, _ := mr.Get("khan:gen:org:finance.invoice"); got != "1" {
		t.Fatalf("gen=%q", got)
	}
	// Org тусгаарлалт Redis дээр
	other := cache.Key{OrgID: "org2", Resource: "finance.invoice", Parts: []string{"list"}}
	if _, hit, _ = c2.Remember(bg, other, time.Minute, val("other")); hit {
		t.Fatal("org2 org-ийн entry-г харлаа")
	}
	// TTL: Redis entry дууссан, L1 (өөр instance) хүртэл
	mr.FastForward(2 * time.Minute)
	for _, k := range mr.Keys() {
		if k != "khan:gen:org:finance.invoice" {
			t.Errorf("TTL-тэй entry үлдсэн: %s", k)
		}
	}
}

func val(s string) func(context.Context) ([]byte, error) {
	return func(context.Context) ([]byte, error) { return []byte(s), nil }
}

func TestRedisDownFailOpen(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := newClient(mr.Addr())
	t.Cleanup(func() { _ = rdb.Close() })
	logs := 0
	c, err := cache.New(cache.Config{L2: New(rdb), Logf: func(string, ...any) { logs++ }})
	if err != nil {
		t.Fatal(err)
	}
	k := cache.Key{OrgID: "o", Resource: "r", Parts: []string{"p"}}
	var calls atomic.Int32
	fn := func(context.Context) ([]byte, error) { calls.Add(1); return []byte("db"), nil }
	c.Remember(bg, k, time.Minute, fn)

	mr.Close() // Redis унав
	// Version → алдаа (crud үүнийг cache-ийг алгасах дохио гэж ойлгоно)
	if _, err := c.Version(bg, "o", "r"); err == nil {
		t.Fatal("Redis унахад Version алдаа өгөх ёстой")
	}
	if _, err := c.Resolve(bg, k); err == nil {
		t.Fatal("Resolve алдаа")
	}
	// Remember fail-open: fn дуудагдаж утга буцна, хуучин (L1) утга ХЭЗЭЭ Ч буцахгүй
	v, hit, err := c.Remember(bg, k, time.Minute, fn)
	if err != nil || hit || string(v) != "db" || calls.Load() != 2 {
		t.Fatalf("%s hit=%v err=%v calls=%d", v, hit, err, calls.Load())
	}
	// Bump алдаа буцна (дуудагч логлоно)
	if err := c.Bump(bg, "o", "r"); err == nil {
		t.Fatal("Redis унахад Bump алдаа буцаах ёстой")
	}
	if c.Stats().Errors < 3 || logs == 0 {
		t.Fatalf("%+v logs=%d", c.Stats(), logs)
	}
	// Store-ын шууд дуудлага мөн алдаа
	s := New(rdb)
	if _, _, err := s.Get(bg, "x"); err == nil {
		t.Fatal("Get")
	}
	if err := s.Set(bg, "x", nil, time.Second); err == nil {
		t.Fatal("Set")
	}
}

func TestContextCanceled(t *testing.T) {
	_, s := setup(t)
	ctx, cancel := context.WithCancel(bg)
	cancel()
	if _, _, err := s.Get(ctx, "k"); err == nil {
		t.Fatal("цуцлагдсан ctx")
	}
	if _, err := s.Gen(ctx, []string{"k"}); err == nil {
		t.Fatal("цуцлагдсан ctx Gen")
	}
	if err := s.Bump(ctx, []string{"k"}); err == nil {
		t.Fatal("цуцлагдсан ctx Bump")
	}
}
