package access

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// ModuleGrant — нэг хэрэглэгчид нэг модульд олгосон эрх.
//
//   - Roles — role түлхүүрүүд. Хэд ч байж болно; эрх нь тэдгээрийн анхдагч
//     capability-уудын НЭГДЭЛ.
//   - Allow — role-оос гадна тусад нь олгосон capability.
//   - Deny — ямар ч эх сурвалжаас (role, allow, өөр Source) ирсэн capability-г ХАСНА.
//
// Wire (JSON) хэлбэр нь main-ийн `{"role":"x","capabilities":[...]}` хэлбэртэй
// нийцнэ; нэмэлтээр `roles`, `allow` (capabilities-ийн өөр нэр), `deny`.
type ModuleGrant struct {
	Roles []string
	Allow []string
	Deny  []string
}

type moduleGrantWire struct {
	Role         string   `json:"role,omitempty"`
	Roles        []string `json:"roles,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
	Allow        []string `json:"allow,omitempty"`
	Deny         []string `json:"deny,omitempty"`
}

// UnmarshalJSON — `role` (ганц) болон `roles` (олон) аль алиныг хүлээн авна.
func (g *ModuleGrant) UnmarshalJSON(b []byte) error {
	var w moduleGrantWire
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	g.Roles = normList(append([]string{w.Role}, w.Roles...))
	g.Allow = normList(append(append([]string{}, w.Capabilities...), w.Allow...))
	g.Deny = normList(w.Deny)
	return nil
}

// MarshalJSON — main-тэй нийцүүлж `role` (эхний role), олон бол `roles`, `capabilities`, `deny`.
func (g ModuleGrant) MarshalJSON() ([]byte, error) {
	w := moduleGrantWire{Capabilities: g.Allow, Deny: g.Deny}
	if len(g.Roles) > 0 {
		w.Role = g.Roles[0]
	}
	if len(g.Roles) > 1 {
		w.Roles = g.Roles
	}
	return json.Marshal(w)
}

// Empty — role, allow, deny аль нь ч байхгүй (хоосон блок юу ч олгодоггүй).
func (g ModuleGrant) Empty() bool { return len(g.Roles) == 0 && len(g.Allow) == 0 && len(g.Deny) == 0 }

func (g ModuleGrant) merge(o ModuleGrant) ModuleGrant {
	return ModuleGrant{
		Roles: normList(append(append([]string{}, g.Roles...), o.Roles...)),
		Allow: normList(append(append([]string{}, g.Allow...), o.Allow...)),
		Deny:  normList(append(append([]string{}, g.Deny...), o.Deny...)),
	}
}

// Grants — нэг (org, user)-ийн бүх эрх.
type Grants struct {
	// Owner — байгууллагын эзэн. Policy-ийн [OwnerPolicy] үүнийг хэрхэн тайлахыг шийднэ.
	Owner   bool                   `json:"owner,omitempty"`
	Modules map[string]ModuleGrant `json:"modules,omitempty"`
}

// Merge — хоёр Grants-ийг нэгтгэнэ (owner OR; role/allow/deny нэгдэл). Оролтыг мутацлахгүй.
func (g Grants) Merge(o Grants) Grants {
	out := Grants{Owner: g.Owner || o.Owner}
	if len(g.Modules)+len(o.Modules) == 0 {
		return out
	}
	out.Modules = make(map[string]ModuleGrant, len(g.Modules)+len(o.Modules))
	for k, v := range g.Modules {
		out.Modules[k] = v.merge(ModuleGrant{})
	}
	for k, v := range o.Modules {
		out.Modules[k] = out.Modules[k].merge(v)
	}
	return out
}

// ParseGrants — `{"owner":bool,"modules":{...}}` JSON-ийг уншина. Хоосон/null → хоосон
// Grants. Үл мэдэгдэх түлхүүр (жишээ нь хуучин `permissions` цэсний блок) алгасагдана.
func ParseGrants(raw []byte) (Grants, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return Grants{}, nil
	}
	var g Grants
	if err := json.Unmarshal(raw, &g); err != nil {
		return Grants{}, fmt.Errorf("access: эрхийн JSON буруу: %w", err)
	}
	return g, nil
}

// normList — хоосон/зайтай утгыг хасаж, давхардлыг арилгаад эрэмбэлнэ (тогтвортой, nil-able).
func normList(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil
	}
	sort.Strings(out)
	return out
}

// Source — апп өөрийн хадгалалтаас (org, user)-ийн эрхийг уншуулах интерфейс.
//
// Гэрээ: orgID хоосон бол алдаа буцаана; Source нь org-оор ЗААВАЛ хязгаарлана.
// Хэрэглэгчид эрх байхгүй бол алдаа биш, хоосон Grants. Зөвхөн хадгалалтын алдааг
// error-оор буцаа — Policy үүнийг татгалзалт (fail-closed) гэж үзнэ. Concurrent-safe байна.
type Source interface {
	Load(ctx context.Context, orgID, userID string) (Grants, error)
}

// SourceFunc — функцийг Source болгоно.
type SourceFunc func(ctx context.Context, orgID, userID string) (Grants, error)

// Load — Source.
func (f SourceFunc) Load(ctx context.Context, orgID, userID string) (Grants, error) {
	return f(ctx, orgID, userID)
}

// OrgConfigurer — Source-ийн нэмэлт чадвар: байгууллагад аль модуль(ууд)-д эрх
// ТОХИРУУЛСАН (ямар нэг хэрэглэгчид role/allow/deny олгосон) бэ. Зөвхөн org-түвшний
// шилжилтийн горимд ([TransitionOrg]) хэрэгтэй.
type OrgConfigurer interface {
	ConfiguredModules(ctx context.Context, orgID string) (map[string]bool, error)
}

// ErrNoOrg — org хоосон.
var ErrNoOrg = errors.New("access: org_id хоосон")

// ─── MemorySource ───

type memKey struct{ org, user string }

// MemorySource — санах ойн Source (тест, жижиг апп, "implicit grant" шиг код-тодорхойлсон эрх).
// Concurrent-safe. [NewMemorySource] ашигла.
type MemorySource struct {
	mu    sync.RWMutex
	m     map[memKey]Grants
	err   error
	loads int
}

// NewMemorySource — хоосон MemorySource.
func NewMemorySource() *MemorySource { return &MemorySource{m: map[memKey]Grants{}} }

// Set — (org, user)-ийн эрхийг орлуулна. Policy-ийн cache-ийг Invalidate хийх нь дуудагчийн үүрэг.
func (s *MemorySource) Set(orgID, userID string, g Grants) {
	s.mu.Lock()
	s.m[memKey{orgID, userID}] = g.Merge(Grants{})
	s.mu.Unlock()
}

// Delete — (org, user)-ийн эрхийг устгана.
func (s *MemorySource) Delete(orgID, userID string) {
	s.mu.Lock()
	delete(s.m, memKey{orgID, userID})
	s.mu.Unlock()
}

// SetError — дараагийн Load-ууд энэ алдааг буцаана (nil → хэвийн). Тестэд.
func (s *MemorySource) SetError(err error) {
	s.mu.Lock()
	s.err = err
	s.mu.Unlock()
}

// Loads — Load-ийн нийт дуудлагын тоо (cache/singleflight шалгахад).
func (s *MemorySource) Loads() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.loads
}

// Load — Source.
func (s *MemorySource) Load(_ context.Context, orgID, userID string) (Grants, error) {
	s.mu.Lock()
	s.loads++
	err := s.err
	g := s.m[memKey{orgID, userID}]
	s.mu.Unlock()
	if err != nil {
		return Grants{}, err
	}
	if orgID == "" {
		return Grants{}, ErrNoOrg
	}
	return g.Merge(Grants{}), nil
}

// ConfiguredModules — OrgConfigurer: байгууллагын хэн нэгэнд эрх олгосон модулиуд.
func (s *MemorySource) ConfiguredModules(_ context.Context, orgID string) (map[string]bool, error) {
	if orgID == "" {
		return nil, ErrNoOrg
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.err != nil {
		return nil, s.err
	}
	out := map[string]bool{}
	for k, g := range s.m {
		if k.org != orgID {
			continue
		}
		for mod, mg := range g.Modules {
			if !mg.Empty() {
				out[mod] = true
			}
		}
	}
	return out, nil
}

// ListUsers — [Lister]: org-ийн бүх хэрэглэгчийн эрх (эрэмбэлсэн, user бүр нэг).
func (s *MemorySource) ListUsers(_ context.Context, orgID string) ([]UserGrants, error) {
	if orgID == "" {
		return nil, ErrNoOrg
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.err != nil {
		return nil, s.err
	}
	var out []UserGrants
	for k, g := range s.m {
		if k.org == orgID {
			out = append(out, UserGrants{UserID: k.user, Grants: g.Merge(Grants{})})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UserID < out[j].UserID })
	return out, nil
}

// ─── Merge ───

// Merge — олон Source-ийн эрхийг НЭГДЭЛ болгоно (main-ийн "implicit grant": ажлын байрны
// эрх ∪ модулийн өөрийн өгөгдлөөс гарах эрх). Аль нэг Source алдаа өгвөл бүхэлдээ алдаа
// (fail-closed — дутуу эрхээр шийдэхгүй). Заасан Source-үүдийн ядаж нэг нь [OrgConfigurer]
// бол буцсан Source бас OrgConfigurer болно.
func Merge(sources ...Source) Source {
	srcs := make([]Source, 0, len(sources))
	configurers := false
	for _, s := range sources {
		if s == nil {
			continue
		}
		srcs = append(srcs, s)
		if _, ok := s.(OrgConfigurer); ok {
			configurers = true
		}
	}
	m := &mergedSource{srcs: srcs}
	if configurers {
		return &mergedConfigSource{m}
	}
	return m
}

type mergedSource struct{ srcs []Source }

func (m *mergedSource) Load(ctx context.Context, orgID, userID string) (Grants, error) {
	var out Grants
	for _, s := range m.srcs {
		g, err := s.Load(ctx, orgID, userID)
		if err != nil {
			return Grants{}, err
		}
		out = out.Merge(g)
	}
	return out, nil
}

type mergedConfigSource struct{ *mergedSource }

func (m *mergedConfigSource) ConfiguredModules(ctx context.Context, orgID string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, s := range m.srcs {
		oc, ok := s.(OrgConfigurer)
		if !ok {
			continue
		}
		mods, err := oc.ConfiguredModules(ctx, orgID)
		if err != nil {
			return nil, err
		}
		for k, v := range mods {
			if v {
				out[k] = true
			}
		}
	}
	return out, nil
}
