package access

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
)

// Role — модуль доторх үүрэг (ж: "manager", "accountant").
type Role struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// Capability — нэг тодорхой үйлдлийн эрх (ж: "invoices.post").
// DefaultRoles-д бичигдсэн role-той хэрэглэгч энэ эрхийг автоматаар авна.
type Capability struct {
	Key          string   `json:"key"`
	Label        string   `json:"label"`
	Description  string   `json:"description,omitempty"`
	DefaultRoles []string `json:"default_roles,omitempty"`
}

// Manifest — нэг модулийн эрхийн тодорхойлолт. JSON хэлбэр нь main-ийн
// `ModuleManifest`-тэй ижил тул front_end-ийн manifest UI-д өөрчлөлтгүй.
type Manifest struct {
	Key          string       `json:"key"`
	Label        string       `json:"label"`
	Icon         string       `json:"icon,omitempty"`
	Roles        []Role       `json:"roles"`
	Capabilities []Capability `json:"capabilities"`
}

// Түлхүүрийн хэлбэр: модуль/role = `snake_case`; capability = `.`-ээр тусгаарласан
// `snake_case` хэсгүүд (ж: `enrollment.finance.edit`). Том үсэг, зай, `*` зөвшөөрөхгүй.
var (
	segmentRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	capKeyRe  = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)*$`)
)

// MaxKeyLen — түлхүүрийн дээд урт.
const MaxKeyLen = 64

// ErrInvalidManifest — Manifest.Validate / Registry.Register-ийн алдааны үндэс.
var ErrInvalidManifest = errors.New("access: manifest буруу")

func validModuleKey(s string) bool { return len(s) <= MaxKeyLen && segmentRe.MatchString(s) }
func validRoleKey(s string) bool   { return len(s) <= MaxKeyLen && segmentRe.MatchString(s) }
func validCapKey(s string) bool    { return len(s) <= MaxKeyLen && capKeyRe.MatchString(s) }

// Validate — manifest-ийн бүтцийг шалгана: түлхүүрийн хэлбэр, давхардал, label,
// DefaultRoles нь заасан role-ууд бодитоор байгаа эсэх. Бүх зөрчлийг нэг алдаанд
// нэгтгэнэ (errors.Is(err, ErrInvalidManifest)).
func (m Manifest) Validate() error {
	var errs []error
	bad := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf("%w: %s: %s", ErrInvalidManifest, m.Key, fmt.Sprintf(format, args...)))
	}
	if !validModuleKey(m.Key) {
		bad("модулийн түлхүүр буруу (%q): snake_case, ≤%d тэмдэгт", m.Key, MaxKeyLen)
	}
	if m.Label == "" {
		bad("label хоосон")
	}
	roles := make(map[string]bool, len(m.Roles))
	for _, r := range m.Roles {
		switch {
		case !validRoleKey(r.Key):
			bad("role түлхүүр буруу (%q)", r.Key)
		case roles[r.Key]:
			bad("role давхардсан (%q)", r.Key)
		}
		if r.Label == "" {
			bad("role %q: label хоосон", r.Key)
		}
		roles[r.Key] = true
	}
	if len(m.Capabilities) == 0 {
		bad("capability нэг ч алга")
	}
	caps := make(map[string]bool, len(m.Capabilities))
	for _, c := range m.Capabilities {
		switch {
		case !validCapKey(c.Key):
			bad("capability түлхүүр буруу (%q)", c.Key)
		case caps[c.Key]:
			bad("capability давхардсан (%q)", c.Key)
		}
		if c.Label == "" {
			bad("capability %q: label хоосон", c.Key)
		}
		caps[c.Key] = true
		seen := map[string]bool{}
		for _, r := range c.DefaultRoles {
			switch {
			case !roles[r]:
				bad("capability %q: анхдагч role %q манифестэд алга", c.Key, r)
			case seen[r]:
				bad("capability %q: анхдагч role %q давхардсан", c.Key, r)
			}
			seen[r] = true
		}
	}
	return errors.Join(errs...)
}

// DefaultCapabilitiesFor — role-ийн анхдагч capability-ууд (эрэмбэлсэн, давхардалгүй).
func (m Manifest) DefaultCapabilitiesFor(roleKey string) []string {
	out := []string{}
	for _, c := range m.Capabilities {
		for _, r := range c.DefaultRoles {
			if r == roleKey {
				out = append(out, c.Key)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

// clone — гүн хуулбар (Registry дотор ба гадагш өгөхдөө caller мутацлахгүйн тулд).
func (m Manifest) clone() Manifest {
	out := m
	out.Roles = append([]Role(nil), m.Roles...)
	out.Capabilities = make([]Capability, len(m.Capabilities))
	for i, c := range m.Capabilities {
		c.DefaultRoles = append([]string(nil), c.DefaultRoles...)
		out.Capabilities[i] = c
	}
	return out
}
