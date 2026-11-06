package cache

import (
	"context"
	"errors"
	"sync"
	"time"
)

// fakeClock — тестийн хуурамч цаг.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *fakeClock { return &fakeClock{t: time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)} }

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

var errBoom = errors.New("boom")

// flakyStore — Get/Set/Gen/Bump-ийг асуудал үүсгэж болох L2 + GenStore. Түлхүүрүүдийг бүртгэнэ.
type flakyStore struct {
	mu                       sync.Mutex
	data                     map[string][]byte
	gens                     map[string]uint64
	failGet, failSet         bool
	failGen, failBump        bool
	getKeys, setKeys, genKey []string
}

func newFlaky() *flakyStore {
	return &flakyStore{data: map[string][]byte{}, gens: map[string]uint64{}}
}

func (s *flakyStore) Get(_ context.Context, k string) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.getKeys = append(s.getKeys, k)
	if s.failGet {
		return nil, false, errBoom
	}
	v, ok := s.data[k]
	return v, ok, nil
}

func (s *flakyStore) Set(_ context.Context, k string, v []byte, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setKeys = append(s.setKeys, k)
	if s.failSet {
		return errBoom
	}
	s.data[k] = append([]byte(nil), v...)
	return nil
}

func (s *flakyStore) Gen(_ context.Context, keys []string) ([]uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.genKey = append(s.genKey, keys...)
	if s.failGen {
		return nil, errBoom
	}
	out := make([]uint64, len(keys))
	for i, k := range keys {
		out[i] = s.gens[k]
	}
	return out, nil
}

func (s *flakyStore) Bump(_ context.Context, keys []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failBump {
		return errBoom
	}
	for _, k := range keys {
		s.gens[k]++
	}
	return nil
}

func (s *flakyStore) set(f func(*flakyStore)) {
	s.mu.Lock()
	f(s)
	s.mu.Unlock()
}
