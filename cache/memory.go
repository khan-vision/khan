package cache

import (
	"container/list"
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// Memory-ийн анхдагч утгууд.
const (
	DefaultMaxEntries = 10000
	DefaultMaxBytes   = 64 << 20
	DefaultShards     = 16

	// entryOverhead — нэг entry-ийн түлхүүр/утгаас гадуурх ойролцоо зардал (list элемент, map, struct).
	entryOverhead = 96
)

// MemoryConfig — Memory-ийн тохиргоо. Тэг утга бүр анхдагчтай.
type MemoryConfig struct {
	MaxEntries int   // 0 → 10000 (нийт, shard-уудад хуваагдана)
	MaxBytes   int64 // 0 → 64 MiB (түлхүүр+утга+зардал)
	Shards     int   // 0 → 16
	// Now — цагийн эх сурвалж (тестэд хуурамч цаг). nil → time.Now.
	Now func() time.Time
}

// Memory — shard-лагдсан LRU + TTL процесс доторх хадгалалт. Entry болон байтын хязгаартай,
// олон goroutine-оос зэрэг ашиглахад аюулгүй (`-race`). Store интерфейсийг хэрэгжүүлнэ.
//
// Get-ийн буцаасан слайсыг ЗАСАЖ БОЛОХГҮЙ (олон уншигчид хуваалцана); Set нь өгөгдлийг хуулж авдаг.
type Memory struct {
	shards    []*memShard
	now       func() time.Time
	evictions atomic.Uint64
}

type memShard struct {
	mu         sync.Mutex
	maxEntries int
	maxBytes   int64
	bytes      int64
	ll         *list.List // урд = хамгийн сүүлд ашигласан
	items      map[string]*list.Element
}

type memEntry struct {
	key     string
	val     []byte
	expires time.Time // тэг = хугацаагүй
	size    int64
}

// NewMemory — Memory үүсгэнэ (тохиргоо алдаагүй, тэг утга анхдагч авна).
func NewMemory(cfg MemoryConfig) *Memory {
	if cfg.MaxEntries <= 0 {
		cfg.MaxEntries = DefaultMaxEntries
	}
	if cfg.MaxBytes <= 0 {
		cfg.MaxBytes = DefaultMaxBytes
	}
	if cfg.Shards <= 0 {
		cfg.Shards = DefaultShards
	}
	if cfg.Shards > cfg.MaxEntries {
		cfg.Shards = cfg.MaxEntries
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	n := int64(cfg.Shards)
	m := &Memory{shards: make([]*memShard, cfg.Shards), now: cfg.Now}
	for i := range m.shards {
		m.shards[i] = &memShard{
			maxEntries: (cfg.MaxEntries + cfg.Shards - 1) / cfg.Shards,
			maxBytes:   (cfg.MaxBytes + n - 1) / n,
			ll:         list.New(),
			items:      map[string]*list.Element{},
		}
	}
	return m
}

func (m *Memory) shard(key string) *memShard {
	// FNV-1a 32
	h := uint32(2166136261)
	for i := 0; i < len(key); i++ {
		h ^= uint32(key[i])
		h *= 16777619
	}
	return m.shards[h%uint32(len(m.shards))]
}

// Get — түлхүүрийг буцаана; хугацаа дууссан бол устгаад олдоогүй гэж үзнэ. Буцаасан слайсыг бүү өөрчил.
func (m *Memory) Get(_ context.Context, key string) ([]byte, bool, error) {
	s := m.shard(key)
	s.mu.Lock()
	defer s.mu.Unlock()
	el, ok := s.items[key]
	if !ok {
		return nil, false, nil
	}
	e := el.Value.(*memEntry)
	if !e.expires.IsZero() && !m.now().Before(e.expires) {
		s.remove(el)
		return nil, false, nil
	}
	s.ll.MoveToFront(el)
	return e.val, true, nil
}

// Set — утгыг хуулж хадгална. ttl <= 0 бол хугацаагүй (зөвхөн LRU-ээр гарна). Нэг entry shard-ийн
// байтын хязгаараас том бол хадгалахгүй (өмнөх утга байсан бол устгана — хуучин утга үлдэхгүй), алдаа биш.
func (m *Memory) Set(_ context.Context, key string, val []byte, ttl time.Duration) error {
	size := int64(len(key)+len(val)) + entryOverhead
	var exp time.Time
	if ttl > 0 {
		exp = m.now().Add(ttl)
	}
	s := m.shard(key)
	s.mu.Lock()
	defer s.mu.Unlock()
	if el, ok := s.items[key]; ok {
		s.remove(el)
	}
	if size > s.maxBytes {
		return nil
	}
	cp := make([]byte, len(val))
	copy(cp, val)
	s.items[key] = s.ll.PushFront(&memEntry{key: key, val: cp, expires: exp, size: size})
	s.bytes += size
	for s.ll.Len() > s.maxEntries || s.bytes > s.maxBytes {
		s.remove(s.ll.Back())
		m.evictions.Add(1)
	}
	return nil
}

// Delete — түлхүүрийг устгана.
func (m *Memory) Delete(key string) {
	s := m.shard(key)
	s.mu.Lock()
	if el, ok := s.items[key]; ok {
		s.remove(el)
	}
	s.mu.Unlock()
}

func (s *memShard) remove(el *list.Element) {
	e := s.ll.Remove(el).(*memEntry)
	delete(s.items, e.key)
	s.bytes -= e.size
}

// Len — одоо байгаа entry-ийн тоо (хугацаа нь дууссан боловч цэвэрлэгдээгүйг оролцуулна).
func (m *Memory) Len() int {
	n := 0
	for _, s := range m.shards {
		s.mu.Lock()
		n += s.ll.Len()
		s.mu.Unlock()
	}
	return n
}

// Bytes — одоо эзэлж буй (түлхүүр+утга+зардал) байт.
func (m *Memory) Bytes() int64 {
	var n int64
	for _, s := range m.shards {
		s.mu.Lock()
		n += s.bytes
		s.mu.Unlock()
	}
	return n
}

// Evictions — хязгаараас болж (LRU) шахагдсан entry-ийн нийт тоо.
func (m *Memory) Evictions() uint64 { return m.evictions.Load() }
