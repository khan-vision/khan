package lookup

import (
	"fmt"
	"strings"

	"github.com/gofiber/fiber/v3"
	"github.com/khan-vision/khan/obs"
	"gorm.io/gorm"
)

// Handler — `GET .../options/:field`:
//
//	?search=  Label/Search баганаар ILIKE (% _ \ escape хийгдсэн)
//	?parent=  Parent баганаар шүүнэ (Parent тохируулсан үед л)
//	?ids=a,b  зөвхөн эдгээр id-г (дээд 200) буцаана — form-ийн анхны label-д; search/parent үл тооно
//	?limit=   1..200 (анхдагч Spec.Limit/50)
//
// Хариу: {"data":[{"value","label","parent_value"?}]}. org нь org-оор (JWT) заавал
// тодорхойлогдоно (401), Tenant:false үед ч нэвтрэлт шаардана.
func (s Spec) Handler(db func() *gorm.DB, org func(fiber.Ctx) (string, bool)) fiber.Handler {
	if err := s.Validate(); err != nil {
		// Тохиргооны алдаа: хүсэлт бүр 500 болгохын оронд Mount-д илрүүлэх ёстой
		// (crud.Mount Validate дууддаг). Энд panic хийхгүй, request-д 500 өгнө.
		return func(c fiber.Ctx) error {
			obs.With(c).Error("lookup: буруу тохиргоо", "err", err)
			return internalError(c)
		}
	}
	return func(c fiber.Ctx) error {
		orgID, ok := org(c)
		if !ok {
			return nil
		}
		p, msg := s.parseParams(c)
		if msg != "" {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": msg})
		}
		gdb := db()
		if gdb == nil {
			obs.With(c).Error("lookup: DB nil")
			return internalError(c)
		}
		q, err := s.query(gdb.WithContext(c.Context()), orgID, p)
		if err != nil {
			obs.With(c).Error("lookup: query", "err", err)
			return internalError(c)
		}
		rows, err := q.Rows()
		if err != nil {
			obs.With(c).Error("lookup: query", "err", err)
			return internalError(c)
		}
		defer rows.Close()
		out := make([]Option, 0, p.limit)
		for rows.Next() {
			var o Option
			var val, label, parent any
			if s.Parent != "" {
				err = rows.Scan(&val, &label, &parent)
			} else {
				err = rows.Scan(&val, &label)
			}
			if err != nil {
				obs.With(c).Error("lookup: scan", "err", err)
				return internalError(c)
			}
			o.Value = normalize(val)
			o.Label, _ = normalize(label).(string)
			if parent != nil {
				o.ParentValue = normalize(parent)
			}
			out = append(out, o)
		}
		if err := rows.Err(); err != nil {
			obs.With(c).Error("lookup: rows", "err", err)
			return internalError(c)
		}
		return c.JSON(fiber.Map{"data": out})
	}
}

func internalError(c fiber.Ctx) error {
	return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Дотоод алдаа гарлаа"})
}

// parseParams — query string → params; msg != "" бол 400.
func (s Spec) parseParams(c fiber.Ctx) (params, string) {
	p := params{limit: clampLimit(fiber.Query[int](c, "limit", 0), s.defaultLimit())}
	// NUL (\x00)-ийг Postgres text хүлээж авдаггүй (22021 → 500) — хайлтаас хасна.
	p.search = truncateRunes(strings.TrimSpace(stripNUL(c.Query("search"))), maxSearchLen)
	p.parent = strings.TrimSpace(stripNUL(c.Query("parent")))
	if p.parent != "" && s.Parent == "" {
		return p, "parent шүүлт энэ сонголтод дэмжигдэхгүй"
	}
	if raw := c.Query("ids"); raw != "" {
		// url.QueryUnescape аль хэдийн хийгдсэн; зөвхөн таслалаар хуваана.
		seen := map[string]bool{}
		var parts []string
		for _, part := range strings.Split(raw, ",") {
			part = strings.TrimSpace(part)
			if part == "" || seen[part] {
				continue
			}
			seen[part] = true
			parts = append(parts, part)
		}
		if len(parts) > MaxIDs {
			return p, fmt.Sprintf("ids хамгийн ихдээ %d байх ёстой", MaxIDs)
		}
		p.byIDs = true
		for _, part := range parts {
			if v, ok := s.coerce(part); ok {
				p.ids = append(p.ids, v)
			}
		}
		if p.limit < len(p.ids) {
			p.limit = len(p.ids)
		}
	}
	return p, ""
}

// stripNUL — NUL тэмдэгтийг хасна (Postgres text-д зөвшөөрөгдөхгүй).
func stripNUL(s string) string { return strings.ReplaceAll(s, "\x00", "") }
