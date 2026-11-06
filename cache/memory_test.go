package cache

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

var bg = context.Background()

func mustGet(t *testing.T, m *Memory, k string) ([]byte, bool) {
	t.Helper()
	v, ok, err := m.Get(bg, k)
	if err != nil {
		t.Fatal(err)
	}
	return v, ok
}

func TestMemoryLRUByEntries(t *testing.T) {
	m := NewMemory(MemoryConfig{MaxEntries: 3, Shards: 1})
	for _, k := range []string{"a", "b", "c"} {
		_ = m.Set(bg, k, []byte(k), 0)
	}
	mustGet(t, m, "a") // a — сүүлд ашигласан; b хамгийн хуучин
	_ = m.Set(bg, "d", []byte("d"), 0)
	if _, ok := mustGet(t, m, "b"); ok {
		t.Fatal("b шахагдах ёстой байсан")
	}
	for _, k := range []string{"a", "c", "d"} {
		if _, ok := mustGet(t, m, k); !ok {
			t.Fatalf("%s алга", k)
		}
	}
	if m.Len() != 3 || m.Evictions() != 1 {
		t.Fatalf("len=%d evictions=%d", m.Len(), m.Evictions())
	}
}

func TestMemoryLRUByBytes(t *testing.T) {
	// entry бүр ~ 1+100+96 = 197 байт; хязгаар 450 → 2 entry.
	m := NewMemory(MemoryConfig{MaxBytes: 450, Shards: 1})
	val := bytes.Repeat([]byte("x"), 100)
	for _, k := range []string{"a", "b", "c"} {
		_ = m.Set(bg, k, val, 0)
	}
	if m.Len() != 2 || m.Bytes() > 450 {
		t.Fatalf("len=%d bytes=%d", m.Len(), m.Bytes())
	}
	if _, ok := mustGet(t, m, "a"); ok {
		t.Fatal("a шахагдах ёстой")
	}
	// Хязгаараас том нэг entry хадгалагдахгүй, өмнөх утгыг ч устгана (хуучин утга үлдэхгүй).
	_ = m.Set(bg, "b", bytes.Repeat([]byte("y"), 1000), 0)
	if _, ok := mustGet(t, m, "b"); ok {
		t.Fatal("том entry хадгалагдсан / хуучин утга үлдсэн")
	}
	if m.Len() != 1 {
		t.Fatalf("len=%d", m.Len())
	}
}

func TestMemoryTTLFakeClock(t *testing.T) {
	clk := newClock()
	m := NewMemory(MemoryConfig{Now: clk.Now})
	_ = m.Set(bg, "k", []byte("v"), time.Minute)
	_ = m.Set(bg, "forever", []byte("f"), 0)
	clk.Advance(59 * time.Second)
	if _, ok := mustGet(t, m, "k"); !ok {
		t.Fatal("хугацаа дуусаагүй")
	}
	clk.Advance(time.Second) // яг дуусах мөч — хүчингүй
	if _, ok := mustGet(t, m, "k"); ok {
		t.Fatal("хугацаа дууссан entry буцлаа")
	}
	if m.Len() != 1 {
		t.Fatalf("дууссан entry устаагүй: len=%d", m.Len())
	}
	clk.Advance(1000 * time.Hour)
	if _, ok := mustGet(t, m, "forever"); !ok {
		t.Fatal("ttl<=0 хугацаагүй байх ёстой")
	}
}

func TestMemoryOverwriteAndDelete(t *testing.T) {
	m := NewMemory(MemoryConfig{})
	buf := []byte("one")
	_ = m.Set(bg, "k", buf, 0)
	buf[0] = 'X' // дуудагч өөрчилсөн ч cache эвдэрч болохгүй (Set хуулна)
	if v, _ := mustGet(t, m, "k"); string(v) != "one" {
		t.Fatalf("Set хуулаагүй: %s", v)
	}
	_ = m.Set(bg, "k", []byte("three"), 0)
	if v, _ := mustGet(t, m, "k"); string(v) != "three" {
		t.Fatal(string(v))
	}
	if m.Bytes() != int64(1+5)+entryOverhead {
		t.Fatalf("bytes=%d (дарж бичихэд хуучин хэмжээ хасагдах ёстой)", m.Bytes())
	}
	m.Delete("k")
	m.Delete("missing")
	if _, ok := mustGet(t, m, "k"); ok || m.Len() != 0 || m.Bytes() != 0 {
		t.Fatalf("len=%d bytes=%d", m.Len(), m.Bytes())
	}
}

func TestMemoryDefaultsAndShardClamp(t *testing.T) {
	m := NewMemory(MemoryConfig{})
	if len(m.shards) != DefaultShards {
		t.Fatalf("shards=%d", len(m.shards))
	}
	m2 := NewMemory(MemoryConfig{MaxEntries: 2})
	if len(m2.shards) != 2 {
		t.Fatalf("shards=%d (MaxEntries-ээс их байж болохгүй)", len(m2.shards))
	}
}

func TestMemoryConcurrent(t *testing.T) {
	m := NewMemory(MemoryConfig{MaxEntries: 64})
	var wg sync.WaitGroup
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				k := fmt.Sprintf("k%d", (g*31+i)%200)
				_ = m.Set(bg, k, []byte(k), time.Minute)
				m.Get(bg, k)
				if i%7 == 0 {
					m.Delete(k)
				}
				m.Len()
				m.Bytes()
			}
		}()
	}
	wg.Wait()
	if m.Len() > 64+DefaultShards { // shard бүрийн ceil
		t.Fatalf("len=%d хязгаараас хэтэрсэн", m.Len())
	}
}

func TestMemoryGens(t *testing.T) {
	var g MemoryGens // тэг утга бэлэн
	got, _ := g.Gen(bg, []string{"a", "b"})
	if got[0] != 0 || got[1] != 0 {
		t.Fatal(got)
	}
	_ = g.Bump(bg, []string{"a", "a", "b"})
	got, _ = g.Gen(bg, []string{"a", "b", "c"})
	if got[0] != 2 || got[1] != 1 || got[2] != 0 {
		t.Fatal(got)
	}
	g2 := NewMemoryGens()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = g2.Bump(bg, []string{"x"}); g2.Gen(bg, []string{"x"}) }()
	}
	wg.Wait()
	if v, _ := g2.Gen(bg, []string{"x"}); v[0] != 50 {
		t.Fatalf("race: %v", v)
	}
}
