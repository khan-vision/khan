package cache

import (
	"context"
	"sync"
)

// MemoryGens — процесс доторх GenStore. Нэг процесст (L2 байхгүй) хангалттай; олон instance
// хуваалцсан L2-той бол хуваалцсан GenStore (rediscache) хэрэглэнэ. Тэг утга бэлэн.
type MemoryGens struct {
	mu sync.RWMutex
	m  map[string]uint64
}

// NewMemoryGens — хоосон MemoryGens.
func NewMemoryGens() *MemoryGens { return &MemoryGens{} }

// Gen — түлхүүр бүрийн generation (байхгүй бол 0).
func (g *MemoryGens) Gen(_ context.Context, keys []string) ([]uint64, error) {
	out := make([]uint64, len(keys))
	g.mu.RLock()
	for i, k := range keys {
		out[i] = g.m[k]
	}
	g.mu.RUnlock()
	return out, nil
}

// Bump — түлхүүр бүрийн generation-ийг +1.
func (g *MemoryGens) Bump(_ context.Context, keys []string) error {
	g.mu.Lock()
	if g.m == nil {
		g.m = map[string]uint64{}
	}
	for _, k := range keys {
		g.m[k]++
	}
	g.mu.Unlock()
	return nil
}
