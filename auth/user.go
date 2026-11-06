package auth

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v3"
	"github.com/khan-vision/khan/tenant"
)

// Claim нэрс — хуучин lambda token-той ИЖИЛ.
const (
	ClaimID         = "id"
	ClaimLogin      = "login"
	ClaimRole       = "role"
	ClaimOrgID      = tenant.ClaimOrgID
	ClaimExtraRole  = "extra_role"
	ClaimPortalOnly = "portal_only"
	ClaimSessionID  = "sid"
)

// Principal — token-ий claims-ээс задалсан нэвтэрсэн хэрэглэгч. (Go-д `User`
// функц ба `User` төрөл нэг package-д зэрэг байж болдоггүй тул төрлийн нэр
// Principal.)
type Principal struct {
	ID         string
	Login      string
	Role       int
	OrgID      string
	ExtraRole  string
	PortalOnly bool
	SessionID  string
}

// User — c.Locals(tenant.LocalsKey) дахь token-оос хэрэглэгчийг задална; token
// байхгүй бол ok=false. Хуучин token-д төрөл холилдсон байдаг тул id/role/sid-г
// float64 / int / string / json.Number аль ч хэлбэрээр хүлээн авна.
func User(c fiber.Ctx) (Principal, bool) {
	claims, ok := tenant.Claims(c)
	if !ok {
		return Principal{}, false
	}
	p := Principal{
		ID:         claimString(claims[ClaimID]),
		Login:      claimString(claims[ClaimLogin]),
		Role:       claimInt(claims[ClaimRole]),
		OrgID:      claimString(claims[ClaimOrgID]),
		ExtraRole:  claimString(claims[ClaimExtraRole]),
		PortalOnly: claimBool(claims[ClaimPortalOnly]),
		SessionID:  claimString(claims[ClaimSessionID]),
	}
	return p, true
}

// UserMap — claims-ийн ХУУЛБАР (хуучин lambda-гийн AuthUserObject-тэй нийцтэй).
// Нэвтрээгүй бол хоосон (nil биш) map. Буцсан map-ийг өөрчилсөн ч token-д нөлөөлөхгүй.
func UserMap(c fiber.Ctx) map[string]any {
	claims, ok := tenant.Claims(c)
	out := make(map[string]any, len(claims))
	if !ok {
		return out
	}
	for k, v := range claims {
		out[k] = v
	}
	return out
}

func claimString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case json.Number:
		return t.String()
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case int32:
		return strconv.FormatInt(int64(t), 10)
	case uint:
		return strconv.FormatUint(uint64(t), 10)
	case uint64:
		return strconv.FormatUint(t, 10)
	}
	return ""
}

func claimInt(v any) int {
	switch t := v.(type) {
	case float64:
		return int(t)
	case int:
		return t
	case int64:
		return int(t)
	case int32:
		return int(t)
	case uint:
		return int(t)
	case uint64:
		return int(t)
	case json.Number:
		if n, err := t.Int64(); err == nil {
			return int(n)
		}
		if f, err := t.Float64(); err == nil {
			return int(f)
		}
	case string:
		s := strings.TrimSpace(t)
		if n, err := strconv.Atoi(s); err == nil {
			return n
		}
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return int(f)
		}
	}
	return 0
}

func claimBool(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		b, _ := strconv.ParseBool(strings.TrimSpace(t))
		return b
	case float64:
		return t != 0
	case int:
		return t != 0
	}
	return false
}

// RoleIs — JWT-ийн role нь өгсөн утгуудын аль нэг эсэхийг шалгах predicate (`crud.Resource.SystemPrincipal`).
// ERP: `SystemPrincipal: auth.RoleIs(4)` (sysadmin = org_id-гүй байж болох цорын ганц role).
func RoleIs(roles ...int) func(c fiber.Ctx) bool {
	return func(c fiber.Ctx) bool {
		u, ok := User(c)
		if !ok {
			return false
		}
		for _, r := range roles {
			if u.Role == r {
				return true
			}
		}
		return false
	}
}
