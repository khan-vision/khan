package access

import (
	"errors"
	"fmt"
	"sort"
	"sync"
)

// ErrFrozen — Freeze хийгдсэн Registry-д Register дуудсан.
var ErrFrozen = errors.New("access: registry хөлдсөн (Policy үүссэний дараа бүртгэх боломжгүй)")

// Registry — модулиудын manifest-ийн бүртгэл. Global биш: апп өөрөө үүсгэж,
// модуль бүр өөрийн manifest-ийг `Register` хийнэ. [New] (Policy) дуудагдахад
// Registry хөлдөнө — ингэснээр compile хийсэн policy-тэй зөрөхгүй.
type Registry struct {
	mu     sync.RWMutex
	mods   map[string]Manifest
	frozen bool
}

// NewRegistry — хоосон Registry.
func NewRegistry() *Registry { return &Registry{mods: map[string]Manifest{}} }

// Register — manifest шалгаж бүртгэнэ. Давхар түлхүүр, буруу manifest, хөлдсөн
// registry бол алдаа (бүртгэл өөрчлөгдөхгүй).
func (r *Registry) Register(m Manifest) error {
	if err := m.Validate(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.frozen {
		return ErrFrozen
	}
	if _, dup := r.mods[m.Key]; dup {
		return fmt.Errorf("%w: %s: модуль давхар бүртгэгдэж байна", ErrInvalidManifest, m.Key)
	}
	r.mods[m.Key] = m.clone()
	return nil
}

// RegisterAll — олон manifest; эхний алдаанд зогсож, түүнээс өмнөхүүд бүртгэгдсэн хэвээр.
func (r *Registry) RegisterAll(ms ...Manifest) error {
	for _, m := range ms {
		if err := r.Register(m); err != nil {
			return err
		}
	}
	return nil
}

// Freeze — цаашид Register хийхийг хориглоно (давхар дуудаж болно).
func (r *Registry) Freeze() {
	r.mu.Lock()
	r.frozen = true
	r.mu.Unlock()
}

// Frozen — Freeze хийгдсэн эсэх.
func (r *Registry) Frozen() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.frozen
}

// Get — модулийн manifest-ийн хуулбар.
func (r *Registry) Get(key string) (Manifest, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	m, ok := r.mods[key]
	if !ok {
		return Manifest{}, false
	}
	return m.clone(), true
}

// All — бүх manifest, түлхүүрээр эрэмбэлсэн (хуулбар).
func (r *Registry) All() []Manifest {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Manifest, 0, len(r.mods))
	for _, m := range r.mods {
		out = append(out, m.clone())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// Len — бүртгэгдсэн модулийн тоо.
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.mods)
}
