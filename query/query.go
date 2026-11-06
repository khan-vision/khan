// Package query нь жагсаалтын (list/index) endpoint бүрд НЭГ ижил
// контракт өгөх нэгдсэн helper:
//
//   - page / limit       — хуудаслалт
//   - search             — олон баганад ILIKE OR хайлт
//   - sort / dir          — whitelist хийсэн баганаар эрэмбэлэх (default created_at DESC)
//   - totals             — бүх шүүсэн мөрийн SUM (footer/хөл дүн)
//
// Хариултын стандарт хэлбэр:
//
//	{ "data": [...], "total": 1234, "page": 1, "limit": 25, "totals": { "total_amount": 999.99 } }
//
// АЖИЛЛАХ ЗАРЧИМ: caller нь `newQuery func() *gorm.DB`-г өгнө. Энэ нь Table/Select/
// Joins/Where-г шингээсэн ШИНЭ query-г дуудал бүрт буцаах ёстой. Helper нь count,
// totals, data гэсэн 3 тусдаа query-г фреш үүсгэж ажиллуулдаг тул GORM-ийн statement
// mutation (Count нь SELECT-ийг дарж бичих г.м.) асуудал гарахгүй.
package query

import (
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
)

// Spec — жагсаалтын тохиргоо.
type Spec struct {
	// Search — ILIKE OR хайх бүрэн нэршилтэй баганууд (ж: "i.invoice_number", "p.name").
	Search []string
	// Sort — API sort түлхүүр → SQL багана. Зөвхөн энд байгаа түлхүүрийг хүлээж авна.
	//
	// ⚠️ ALIAS УРХИ: утга нь SELECT-ийн alias биш ЖИНХЭНЭ илэрхийлэл байх ёстой
	// (`p.name`, `"party_name"` БИШ) — Postgres-ийн WHERE/ORDER нь alias-ыг үргэлж
	// харахгүй. Filters-д ч мөн адил.
	Sort map[string]string
	// Filters — API шүүлтийн түлхүүр → SQL багана + төрөл. Sort-той ижил whitelist
	// зарчим: багана хэзээ ч client-ээс ирэхгүй. Зарлаагүй бол баганын шүүлт хаалттай
	// (хариултын `filterable` хоосон → frontend шүүлтийн мөр зурахгүй).
	// Дэлгэрэнгүй → filter.go
	Filters map[string]FilterCol
	// DefaultOrder — sort param өгөгдөөгүй үеийн ORDER BY (ж: "i.created_at DESC, i.id DESC").
	DefaultOrder string
	// Totals — хариултын түлхүүр → SQL aggregate илэрхийлэл
	// (ж: "total_amount" → "COALESCE(SUM(i.total_amount),0)"). Footer/хөл дүнд.
	Totals map[string]string
	// DefaultLimit — limit param өгөгдөөгүй үеийн хэмжээ (0 бол 25).
	DefaultLimit int
	// MaxLimit — зөвшөөрөх дээд limit (0 бол 500).
	MaxLimit int
}

// Run — стандарт жагсаалтын хариулт бэлдэнэ. dest нь *[]Row байх ёстой.
// Жишээ:
//
//	resp, err := query.Run(c, newQuery, &rows, query.Spec{...})
//	if err != nil { return c.Status(500).JSON(fiber.Map{"error": err.Error()}) }
//	return c.JSON(resp)
func Run(c *fiber.Ctx, newQuery func() *gorm.DB, dest interface{}, spec Spec) (fiber.Map, error) {
	return RunRequest(FromQuery(c), newQuery, dest, spec)
}

// RunRequest — Run-тэй ижил, гэхдээ параметрүүдийг Request-ээс авна (GET query
// эсвэл POST body — RequestFrom). Run == RunRequest(FromQuery(c), ...).
func RunRequest(req Request, newQuery func() *gorm.DB, dest interface{}, spec Spec) (fiber.Map, error) {
	p, ferr := spec.prepare(req)
	if ferr != nil {
		// Хэрэглэгчийн буруу шүүлт — caller `IsFilterError`-оор 400 буцаана.
		return nil, ferr
	}

	// base — count / totals / data ГУРВУУЛАА яг ижил предикат авна. Үүнгүй бол
	// хөлийн нийт дүн харагдаж буй мөрүүдээс өөр зүйл ярина.
	base := spec.baseFunc(newQuery, p)

	// ─── Count ───
	var total int64
	if err := base().Count(&total).Error; err != nil {
		return nil, err
	}

	// ─── Totals (бүх шүүсэн мөрийн SUM) ───
	totals := map[string]float64{}
	if len(spec.Totals) > 0 && total > 0 {
		sels := make([]string, 0, len(spec.Totals))
		for key, expr := range spec.Totals {
			sels = append(sels, expr+" AS "+key)
		}
		row := map[string]interface{}{}
		if err := base().Select(strings.Join(sels, ", ")).Scan(&row).Error; err == nil {
			for key := range spec.Totals {
				totals[key] = toFloat(row[key])
			}
		}
	} else {
		for key := range spec.Totals {
			totals[key] = 0
		}
	}

	// ─── Data (тухайн хуудас) ───
	dq := base()
	if p.order != "" {
		dq = dq.Order(p.order)
	}
	if err := dq.Offset((p.page - 1) * p.limit).Limit(p.limit).Scan(dest).Error; err != nil {
		return nil, err
	}

	return fiber.Map{
		"data":   dest,
		"total":  total,
		"page":   p.page,
		"limit":  p.limit,
		"totals": totals,
		// filterable — frontend энэ мэдээллээр баганын шүүлтийн мөрийг зурна.
		// Spec.Filters зарлаагүй endpoint хоосон объект буцаана → шүүлт харагдахгүй.
		"filterable": spec.filterable(),
	}, nil
}

// baseFunc — хайлт + шүүлттэй ФРЕШ query буцаах closure (Run, ExportXLSX нийтлэг).
func (s Spec) baseFunc(newQuery func() *gorm.DB, p prepared) func() *gorm.DB {
	return func() *gorm.DB {
		return applyFilters(s.applySearch(newQuery(), p.search), p.conds)
	}
}

// applySearch — өгөгдсөн query дээр ILIKE OR хайлтыг нэмж шинэ query буцаана.
func (s Spec) applySearch(q *gorm.DB, val string) *gorm.DB {
	if val == "" || len(s.Search) == 0 {
		return q
	}
	// Хэрэглэгчийн бичсэн `%` `_` нь wildcard болохоос сэргийлж escape хийнэ
	// (өмнө нь `search=%` бүх мөрийг олдог байсан).
	like := "%" + escapeLike(val) + "%"
	conds := make([]string, len(s.Search))
	args := make([]interface{}, len(s.Search))
	for i, col := range s.Search {
		conds[i] = col + ` ILIKE ? ESCAPE '\'`
		args[i] = like
	}
	return q.Where("("+strings.Join(conds, " OR ")+")", args...)
}

// toFloat — numeric(18,2) нь драйвераас float64/int64/string/[]byte янз бүрээр ирдэг
// тул бүгдийг найдвартай float64 болгоно.
func toFloat(v interface{}) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case float32:
		return float64(t)
	case int64:
		return float64(t)
	case int:
		return float64(t)
	case []byte:
		f, _ := strconv.ParseFloat(string(t), 64)
		return f
	case string:
		f, _ := strconv.ParseFloat(t, 64)
		return f
	default:
		return 0
	}
}
