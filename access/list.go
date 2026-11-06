package access

import (
	"context"
	"errors"
	"fmt"
	"sort"
)

// UserGrants — нэг хэрэглэгчийн эрх ([Lister]).
type UserGrants struct {
	UserID string
	Grants Grants
}

// Lister — Source-ийн нэмэлт чадвар: байгууллагын бүх хэрэглэгчийн эрхийг нэг дор. Хэрэглэгч
// бүр ХОЁР удаа орохгүй (олон мөр нэгдсэн). [Policy.UsersWithCapability]-д хэрэгтэй.
// Гэрээ: orgID хоосон бол [ErrNoOrg]; org-оор заавал хязгаарлана.
type Lister interface {
	ListUsers(ctx context.Context, orgID string) ([]UserGrants, error)
}

// ErrNoLister — Source нь [Lister] биш.
var ErrNoLister = errors.New("access: Source Lister биш")

// UsersWithCapability — org-ийн module.cap-ийг хийж чадах хэрэглэгчдийн id (эрэмбэлсэн, давхардалгүй).
// Owner нь [OwnerPolicy]-ийн дагуу, deny хүндлэгдэнэ. Шилжилтийн горим ([WithTransition])
// ҮЛЧЛЭХГҮЙ: "хэн ч тохируулаагүй тул бүгдэд нээлттэй" гэсэн хуурмаг жагсаалт (мэдэгдэл бүх org-д
// очих) гарахгүйн тулд ЗӨВХӨН тодорхой олгосон эрхийг тоолно. Module/cap manifest-д байхгүй бол алдаа;
// Source [Lister] биш бол [ErrNoLister]. Cache хийхгүй (job/мэдэгдэлд нэг удаа дуудах зориулалттай).
func (p *Policy) UsersWithCapability(ctx context.Context, orgID, module, capability string) ([]string, error) {
	if orgID == "" {
		return nil, ErrNoOrg
	}
	if err := p.Verify(module, capability); err != nil {
		return nil, err
	}
	l, ok := p.src.(Lister)
	if !ok {
		return nil, ErrNoLister
	}
	ctx, cancel := context.WithTimeout(ctx, p.loadTimeout)
	defer cancel()
	users, err := l.ListUsers(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("access: хэрэглэгчдийн жагсаалт: %w", err)
	}
	seen := map[string]struct{}{}
	out := []string{}
	for _, u := range users {
		if u.UserID == "" {
			continue
		}
		if _, dup := seen[u.UserID]; dup {
			continue
		}
		d, err := p.decide(ctx, orgID, p.comp.resolve(u.Grants), true, module, []string{capability})
		if err != nil {
			return nil, err
		}
		if d.Allowed {
			seen[u.UserID] = struct{}{}
			out = append(out, u.UserID)
		}
	}
	sort.Strings(out)
	return out, nil
}
