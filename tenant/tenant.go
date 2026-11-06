// Package tenant нь нэвтэрсэн хэрэглэгчийн байгууллагыг (org_id) ЗӨВХӨН
// сервер баталгаажуулсан JWT claims-аас уншдаг multi-tenant суурь.
//
// АЮУЛГҮЙ БАЙДЛЫН ДҮРЭМ (fail-closed):
//   - org_id-г client-ийн `X-Org-ID` header эсвэл `?org_id=` query-гээс ХЭЗЭЭ Ч
//     уншихгүй — хуурамчаар бусад байгууллагын өгөгдөл харах боломжтой.
//   - JWT байхгүй, org_id хоосон / zero UUID / string биш бол алдаа.
//
// JWT middleware нь `c.Locals(LocalsKey)`-д *jwt.Token тавьсан байх ёстой
// (gofiber/contrib/jwt-ийн анхдагч "user" түлхүүр — khan/auth мөн адил).
//
//	orgID, ok := tenant.Require(c)
//	if !ok {
//	    return nil // 401 аль хэдийн бичигдсэн
//	}
//	db.Scopes(tenant.Scope("i", orgID)).Find(&rows)
package tenant

import (
	"errors"
	"regexp"

	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v5"
	"gorm.io/gorm"
)

// LocalsKey — JWT middleware-ийн *jwt.Token хадгалдаг c.Locals түлхүүр.
const LocalsKey = "user"

// ClaimOrgID — org_id-г агуулах JWT claim-ийн нэр.
const ClaimOrgID = "org_id"

// ZeroUUID — "байгууллага сонгоогүй" гэж тооцох утга.
const ZeroUUID = "00000000-0000-0000-0000-000000000000"

// ErrNoOrg — JWT-д хүчинтэй org_id олдсонгүй.
var ErrNoOrg = errors.New("байгууллагын мэдээлэл олдсонгүй")

// Claims — c.Locals(LocalsKey)-д байгаа JWT-ийн MapClaims (байхгүй бол ok=false).
func Claims(c *fiber.Ctx) (jwt.MapClaims, bool) {
	tok, ok := c.Locals(LocalsKey).(*jwt.Token)
	if !ok || tok == nil {
		return nil, false
	}
	claims, ok := tok.Claims.(jwt.MapClaims)
	return claims, ok
}

// OrgID — JWT claims доторх org_id. Client-ийн header/query-г огт хардаггүй.
func OrgID(c *fiber.Ctx) (string, error) {
	claims, ok := Claims(c)
	if !ok {
		return "", ErrNoOrg
	}
	if v, ok := claims[ClaimOrgID].(string); ok && v != "" && v != ZeroUUID {
		return v, nil
	}
	return "", ErrNoOrg
}

// Require — OrgID; алдаа гарвал 401 `{"error":"Зөвшөөрөлгүй"}` бичээд ok=false.
// Caller `if !ok { return nil }` гэж шууд буцаана.
func Require(c *fiber.Ctx) (string, bool) {
	orgID, err := OrgID(c)
	if err != nil {
		_ = c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "Зөвшөөрөлгүй"})
		return "", false
	}
	return orgID, true
}

// Optional — org_id эсвэл "" (алдаа биш). Org-оор заавал хязгаарлах
// шаардлагагүй цөөн тохиолдолд л; CRUD/жагсаалтад Require-г хэрэглэ.
func Optional(c *fiber.Ctx) string {
	orgID, err := OrgID(c)
	if err != nil {
		return ""
	}
	return orgID
}

// Middleware — org_id-гүй хүсэлтийг 401-ээр зогсоож, org_id-г
// c.Locals(LocalsOrgID)-д тавина. JWT middleware-ийн ДАРАА бүртгэнэ.
func Middleware() fiber.Handler {
	return func(c *fiber.Ctx) error {
		orgID, ok := Require(c)
		if !ok {
			return nil
		}
		c.Locals(LocalsOrgID, orgID)
		return c.Next()
	}
}

// LocalsOrgID — Middleware-ийн тавьсан org_id-ийн c.Locals түлхүүр.
const LocalsOrgID = "khan.org_id"

var identRe = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// Scope — `<alias>.org_id = ?` нөхцөл нэмэх GORM scope. alias хоосон бол
// баганыг qualify хийхгүй. orgID хоосон бол query-г АЛДААТАЙ болгоно
// (fail-closed: scope-гүй query бүх байгууллагын мөр буцаахаас сэргийлнэ).
func Scope(alias, orgID string) func(*gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		if orgID == "" || orgID == ZeroUUID {
			_ = db.AddError(ErrNoOrg)
			return db
		}
		col := ClaimOrgID
		if alias != "" {
			if !identRe.MatchString(alias) {
				_ = db.AddError(errors.New("tenant: alias буруу: " + alias))
				return db
			}
			col = alias + "." + ClaimOrgID
		}
		return db.Where(col+" = ?", orgID)
	}
}
