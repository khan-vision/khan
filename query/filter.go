// filter.go — жагсаалтын БАГАНА ТУС БҮРИЙН шүүлт (per-column filter).
//
// Wire формат: `filters` query параметр дотор нэг JSON объект. Түлхүүр нь API-ийн
// шүүлтийн нэр (Spec.Filters-д бүртгэсэн), утга нь нөхцөл:
//
//	{
//	  "party_name":   { "op": "contains", "v": "Гоовин" },
//	  "total_amount": { "op": "between",  "v": 100000, "v2": 500000 },
//	  "status":       { "op": "in",       "vs": ["posted", "draft"] },
//	  "description":  { "j": "or", "c": [ {"op":"contains","v":"түрээс"},
//	                                      {"op":"contains","v":"лизинг"} ] }
//	}
//
// ⚠️ АЮУЛГҮЙ БАЙДЛЫН ХИЛ: SQL мөрөнд ЗӨВХӨН `Spec.Filters`-д сервер талд бичсэн
// баганын нэр ордог. Хэрэглэгчийн ирүүлсэн бүх утга нь `?` bind параметр болно.
// Client-ээс ирсэн түлхүүр whitelist-д байхгүй бол 400 алдаа буцаана (чимээгүй
// алгасвал «шүүлт юу ч хийхгүй байна» гэсэн гомдол болно).
package query

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v3"
	"gorm.io/gorm"
)

// FilterKind — баганын төрөл. Ямар оператор зөвшөөрөгдөхийг тодорхойлно, мөн
// frontend-д ямар оролт (текст/тоо/огноо/сонголт/данс) зурахыг хэлж өгнө.
type FilterKind string

const (
	KindText   FilterKind = "text"
	KindNumber FilterKind = "number"
	KindDate   FilterKind = "date"
	// KindEnum — хаалттай жагсаалт (status/type). Frontend нь col.labelMap-аас
	// сонголтуудыг зурна — server-ээс distinct татах шаардлагагүй.
	KindEnum FilterKind = "enum"
	KindBool FilterKind = "bool"
	KindUUID FilterKind = "uuid"
	// KindAccount — SQL-ийн хувьд uuid-тэй яг ижил. Frontend-д ДАНС сонгогч
	// (server хайлттай) гаргана.
	KindAccount FilterKind = "account"
)

// FilterCol — API түлхүүр → SQL багана/илэрхийлэл + төрөл.
//
// ⚠️ ALIAS УРХИ: `Col` нь SELECT-ийн alias биш ЖИНХЭНЭ илэрхийлэл байх ёстой
// (`p.name`, `"party_name"` БИШ) — Postgres-ийн WHERE нь SELECT alias харахгүй.
// Энэ нь `Spec.Sort`-ийн адил дүрэм.
type FilterCol struct {
	Col  string
	Kind FilterKind
}

// Товч бүтээгчид — controller дээр `"status": query.Enum("i.status")` гэж бичнэ.
func Text(col string) FilterCol    { return FilterCol{col, KindText} }
func Number(col string) FilterCol  { return FilterCol{col, KindNumber} }
func Date(col string) FilterCol    { return FilterCol{col, KindDate} }
func Enum(col string) FilterCol    { return FilterCol{col, KindEnum} }
func Bool(col string) FilterCol    { return FilterCol{col, KindBool} }
func UUID(col string) FilterCol    { return FilterCol{col, KindUUID} }
func Account(col string) FilterCol { return FilterCol{col, KindAccount} }

// ─── Хязгаарууд (хэт том/төвөгтэй шүүлтээс хамгаална) ───
const (
	maxFilterBytes  = 8 << 10 // 8 KB
	maxFilterCols   = 20
	maxFilterConds  = 2
	maxFilterInVals = 100
	maxFilterValLen = 200
)

// FilterError — хэрэглэгчийн буруу шүүлт (HTTP 400). Дотоод алдаанаас (500) ялгана.
type FilterError struct{ Msg string }

func (e *FilterError) Error() string { return e.Msg }

// IsFilterError — controller-т 400 vs 500 ялгахад.
//
//	if err != nil {
//	    if query.IsFilterError(err) { return ctx.Status(400).JSON(...) }
//	    return ctx.Status(500).JSON(...)
//	}
func IsFilterError(err error) bool {
	var fe *FilterError
	return errors.As(err, &fe)
}

// Fail — Run-ийн алдааг зөв HTTP статустай болгоно. Controller бүрд:
//
//	resp, err := query.Run(ctx, newQuery, &rows, spec)
//	if err != nil { return query.Fail(ctx, err) }
//	return ctx.JSON(resp)
func Fail(c fiber.Ctx, err error) error {
	if IsFilterError(err) {
		return c.Status(400).JSON(fiber.Map{"error": err.Error()})
	}
	return c.Status(500).JSON(fiber.Map{"error": err.Error()})
}

func ferr(format string, a ...interface{}) error {
	return &FilterError{Msg: fmt.Sprintf(format, a...)}
}

// rawCond — wire дээрх нэг баганын нөхцөл. `C` хоосон биш бол композит
// (`J` = "and"|"or", анхдагч "and").
type rawCond struct {
	Op string            `json:"op"`
	V  json.RawMessage   `json:"v"`
	V2 json.RawMessage   `json:"v2"`
	Vs []json.RawMessage `json:"vs"`
	J  string            `json:"j"`
	C  []rawCond         `json:"c"`
}

// condSQL — SQL-д шууд шингэх бэлэн хэсэг.
type condSQL struct {
	sql  string
	args []interface{}
}

// parseFilters — `filters` query-г задалж, whitelist + kind/op матрицаар шалгана.
func (s Spec) parseFilters(raw string) ([]condSQL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "{}" {
		return nil, nil
	}
	if len(s.Filters) == 0 {
		// Backend энэ endpoint-д баганын шүүлт зарлаагүй байна.
		return nil, ferr("энэ жагсаалт баганын шүүлт дэмждэггүй")
	}
	if len(raw) > maxFilterBytes {
		return nil, ferr("шүүлт хэт урт")
	}

	var m map[string]rawCond
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil, ferr("шүүлтийн JSON буруу")
	}
	if len(m) > maxFilterCols {
		return nil, ferr("шүүлтийн багана хэт олон (дээд тал нь %d)", maxFilterCols)
	}

	// Тогтмол дараалал — үүсэх SQL давтагдах (тест/plan cache) байхын тулд.
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out := make([]condSQL, 0, len(keys))
	for _, key := range keys {
		fc, ok := s.Filters[key]
		if !ok {
			return nil, ferr("зөвшөөрөгдөөгүй шүүлтийн багана: %s", key)
		}
		c, err := buildColFilter(fc, m[key])
		if err != nil {
			return nil, err
		}
		if c.sql != "" {
			out = append(out, c)
		}
	}
	return out, nil
}

// buildColFilter — нэг баганын нөхцөл (энгийн эсвэл композит).
func buildColFilter(fc FilterCol, rc rawCond) (condSQL, error) {
	if len(rc.C) == 0 {
		return buildOne(fc, rc)
	}
	if len(rc.C) > maxFilterConds {
		return condSQL{}, ferr("нэг баганад дээд тал нь %d нөхцөл", maxFilterConds)
	}
	join := " AND "
	if strings.EqualFold(rc.J, "or") {
		join = " OR "
	}
	parts := make([]string, 0, len(rc.C))
	args := []interface{}{}
	for _, sub := range rc.C {
		if len(sub.C) > 0 {
			return condSQL{}, ferr("шүүлтийн нөхцөл давхарлаж болохгүй")
		}
		c, err := buildOne(fc, sub)
		if err != nil {
			return condSQL{}, err
		}
		if c.sql == "" {
			continue
		}
		parts = append(parts, c.sql)
		args = append(args, c.args...)
	}
	if len(parts) == 0 {
		return condSQL{}, nil
	}
	return condSQL{"(" + strings.Join(parts, join) + ")", args}, nil
}

// buildOne — kind × op матриц. ЭНЭ БОЛ АЮУЛГҮЙ БАЙДЛЫН ХИЛ:
// `fc.Col` нь сервер талд бичигдсэн, хэрэглэгчийн утга бүр `?` bind.
func buildOne(fc FilterCol, c rawCond) (condSQL, error) {
	col := fc.Col
	op := strings.TrimSpace(c.Op)

	// blank/notBlank — бүх төрөлд нийтлэг.
	switch op {
	case "blank":
		if fc.Kind == KindText || fc.Kind == KindEnum {
			return condSQL{"(" + col + " IS NULL OR " + col + " = '')", nil}, nil
		}
		return condSQL{col + " IS NULL", nil}, nil
	case "notBlank":
		if fc.Kind == KindText || fc.Kind == KindEnum {
			return condSQL{"(" + col + " IS NOT NULL AND " + col + " <> '')", nil}, nil
		}
		return condSQL{col + " IS NOT NULL", nil}, nil
	}

	switch fc.Kind {

	case KindText:
		if op == "in" {
			vs, err := strList(c.Vs)
			if err != nil || len(vs) == 0 {
				return condSQL{}, err
			}
			return condSQL{col + " IN (?)", []interface{}{vs}}, nil
		}
		s, err := str(c.V)
		if err != nil {
			return condSQL{}, err
		}
		if s == "" {
			return condSQL{}, nil // хоосон утга = шүүлтгүй (алдаа биш)
		}
		lit := escapeLike(s)
		switch op {
		case "contains":
			return like(col, "%"+lit+"%", false), nil
		case "notContains":
			return like(col, "%"+lit+"%", true), nil
		case "startsWith":
			return like(col, lit+"%", false), nil
		case "endsWith":
			return like(col, "%"+lit, false), nil
		case "eq":
			return like(col, lit, false), nil // текстэд ЖИЖИГ/ТОМ ялгаагүй тэнцүү
		case "neq":
			return like(col, lit, true), nil
		}

	case KindNumber:
		switch op {
		case "eq", "neq", "gt", "gte", "lt", "lte":
			n, err := num(c.V)
			if err != nil {
				return condSQL{}, err
			}
			if n == nil {
				return condSQL{}, nil
			}
			return condSQL{col + " " + cmpOp(op) + " ?", []interface{}{*n}}, nil
		case "between":
			a, err1 := num(c.V)
			b, err2 := num(c.V2)
			if err1 != nil || err2 != nil {
				return condSQL{}, ferr("хооронд шүүлтэд хоёр тоо шаардлагатай")
			}
			if a == nil || b == nil {
				return condSQL{}, nil
			}
			return condSQL{"(" + col + " >= ? AND " + col + " <= ?)", []interface{}{*a, *b}}, nil
		}

	case KindDate:
		// Инклюзив ӨДРИЙН семантик — `date` ба `timestamptz` хоёуланд зөв ажиллана
		// (timestamp баганад `<= '2026-07-31'` нь тэр өдрийн 00:00-оос хойшхыг алддаг).
		d, err := day(c.V)
		if err != nil {
			return condSQL{}, err
		}
		if d == "" && op != "between" {
			return condSQL{}, nil
		}
		switch op {
		case "eq":
			return condSQL{"(" + col + " >= ?::date AND " + col + " < ?::date + INTERVAL '1 day')",
				[]interface{}{d, d}}, nil
		case "gte":
			return condSQL{col + " >= ?::date", []interface{}{d}}, nil
		case "gt":
			return condSQL{col + " >= ?::date + INTERVAL '1 day'", []interface{}{d}}, nil
		case "lte":
			return condSQL{col + " < ?::date + INTERVAL '1 day'", []interface{}{d}}, nil
		case "lt":
			return condSQL{col + " < ?::date", []interface{}{d}}, nil
		case "between":
			d2, err2 := day(c.V2)
			if err2 != nil {
				return condSQL{}, err2
			}
			if d == "" || d2 == "" {
				return condSQL{}, ferr("хооронд шүүлтэд хоёр огноо шаардлагатай")
			}
			return condSQL{"(" + col + " >= ?::date AND " + col + " < ?::date + INTERVAL '1 day')",
				[]interface{}{d, d2}}, nil
		}

	case KindEnum, KindBool:
		switch op {
		case "eq", "neq":
			v, err := str(c.V)
			if err != nil {
				return condSQL{}, err
			}
			if v == "" {
				return condSQL{}, nil
			}
			if op == "eq" {
				return condSQL{col + " = ?", []interface{}{v}}, nil
			}
			return condSQL{"(" + col + " IS NULL OR " + col + " <> ?)", []interface{}{v}}, nil
		case "in":
			vs, err := strList(c.Vs)
			if err != nil || len(vs) == 0 {
				return condSQL{}, err
			}
			return condSQL{col + " IN (?)", []interface{}{vs}}, nil
		}

	case KindUUID, KindAccount:
		switch op {
		case "eq", "neq":
			id, err := uuidStr(c.V)
			if err != nil {
				return condSQL{}, err
			}
			if id == "" {
				return condSQL{}, nil
			}
			if op == "eq" {
				return condSQL{col + " = ?", []interface{}{id}}, nil
			}
			return condSQL{"(" + col + " IS NULL OR " + col + " <> ?)", []interface{}{id}}, nil
		case "in":
			ids, err := uuidList(c.Vs)
			if err != nil || len(ids) == 0 {
				return condSQL{}, err
			}
			return condSQL{col + " IN (?)", []interface{}{ids}}, nil
		}
	}

	return condSQL{}, ferr("«%s» төрөлд «%s» оператор тохирохгүй", fc.Kind, op)
}

// like — ILIKE нөхцөл. `\` escape тул ESCAPE '\' зааж өгнө (Postgres-д анхдагч ч
// гэсэн тодорхой бичих нь standard_conforming_strings-ээс хамаарахгүй болгоно).
func like(col, pattern string, negate bool) condSQL {
	if negate {
		return condSQL{"(" + col + ` IS NULL OR ` + col + ` NOT ILIKE ? ESCAPE '\')`, []interface{}{pattern}}
	}
	return condSQL{col + ` ILIKE ? ESCAPE '\'`, []interface{}{pattern}}
}

func cmpOp(op string) string {
	switch op {
	case "eq":
		return "="
	case "neq":
		return "<>"
	case "gt":
		return ">"
	case "gte":
		return ">="
	case "lt":
		return "<"
	case "lte":
		return "<="
	}
	return "="
}

// escapeLike — хэрэглэгчийн бичсэн `%` `_` `\`-г ЛИТЕРАЛ болгоно. Үүнгүй бол
// бичсэн `%` нь wildcard, `_` нь дурын нэг тэмдэгт болж, хайлт төөрөгдүүлнэ.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// ─── Утга задлагчид ───

// str — JSON утгыг мөр болгоно (тоо/bool ирвэл ч хүлээж авна).
func str(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		s = strings.TrimSpace(s)
		if len(s) > maxFilterValLen {
			return "", ferr("шүүлтийн утга хэт урт")
		}
		return s, nil
	}
	var n json.Number
	if err := json.Unmarshal(raw, &n); err == nil {
		return n.String(), nil
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err == nil {
		return strconv.FormatBool(b), nil
	}
	return "", ferr("шүүлтийн утга буруу")
}

func strList(raws []json.RawMessage) ([]string, error) {
	if len(raws) == 0 {
		return nil, nil
	}
	if len(raws) > maxFilterInVals {
		return nil, ferr("сонголтын утга хэт олон (дээд тал нь %d)", maxFilterInVals)
	}
	out := make([]string, 0, len(raws))
	for _, r := range raws {
		s, err := str(r)
		if err != nil {
			return nil, err
		}
		if s != "" {
			out = append(out, s)
		}
	}
	return out, nil
}

// num — тоон утга. Хоосон/null бол nil (шүүлтгүй).
func num(raw json.RawMessage) (*float64, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err == nil {
		return &f, nil
	}
	// "1200.50" гэх мэт мөр хэлбэрээр ирж болно (оролтын талбар).
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		s = strings.TrimSpace(s)
		if s == "" {
			return nil, nil
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return nil, ferr("тоон утга буруу: %s", s)
		}
		return &f, nil
	}
	return nil, ferr("тоон утга буруу")
}

// day — «YYYY-MM-DD» огноо. ISO timestamp ирвэл өдрийг нь тайрна.
func day(raw json.RawMessage) (string, error) {
	s, err := str(raw)
	if err != nil || s == "" {
		return "", err
	}
	if len(s) > 10 {
		s = s[:10]
	}
	if len(s) != 10 || s[4] != '-' || s[7] != '-' {
		return "", ferr("огнооны утга буруу: %s", s)
	}
	for i, ch := range s {
		if i == 4 || i == 7 {
			continue
		}
		if ch < '0' || ch > '9' {
			return "", ferr("огнооны утга буруу: %s", s)
		}
	}
	return s, nil
}

// uuidStr — uuid хэлбэрийг шалгана (Postgres-ийн cast алдаанаас сэргийлнэ).
func uuidStr(raw json.RawMessage) (string, error) {
	s, err := str(raw)
	if err != nil || s == "" {
		return "", err
	}
	if !isUUID(s) {
		return "", ferr("uuid утга буруу: %s", s)
	}
	return s, nil
}

func uuidList(raws []json.RawMessage) ([]string, error) {
	vs, err := strList(raws)
	if err != nil {
		return nil, err
	}
	for _, v := range vs {
		if !isUUID(v) {
			return nil, ferr("uuid утга буруу: %s", v)
		}
	}
	return vs, nil
}

func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := 0; i < 36; i++ {
		c := s[i]
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
			continue
		}
		isHex := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
		if !isHex {
			return false
		}
	}
	return true
}

// applyFilters — багана бүр нэг бүлэг; бүлгүүд AND-аар нийлнэ.
func applyFilters(q *gorm.DB, conds []condSQL) *gorm.DB {
	for _, c := range conds {
		q = q.Where(c.sql, c.args...)
	}
	return q
}

// filterable — «энэ endpoint аль баганыг шүүж чадах вэ» гэдгийг frontend-д мэдэгдэнэ.
// Хариултын `filterable` талбар болж явна. Spec.Filters зарлаагүй бол хоосон объект
// буцаах тул frontend шүүлтийн мөрийг ОГТ зурахгүй — фаз хооронд flag-day хэрэггүй.
func (s Spec) filterable() map[string]FilterKind {
	out := make(map[string]FilterKind, len(s.Filters))
	for k, v := range s.Filters {
		out[k] = v.Kind
	}
	return out
}

// ─── `Run` ашиглаагүй handler-т зориулсан гарц ─────────────────────────────
//
// Зарим жагсаалтын endpoint нь `Run`-ий гэрээнд багтахгүй нарийн логиктой байдаг
// (GORM Preload, нэхэмжлэхийн нийлбэр, өөрийн `footer` түлхүүр г.м.). Тэднийг
// бүхэлд нь дахин бичихгүйгээр баганын шүүлтийг НЭЭХ хоёр гарц:
//
//	cols := map[string]query.FilterCol{"status": query.Enum("e.status")}
//	q, err := query.ApplyFilters(c, q, cols)
//	if err != nil { return query.Fail(c, err) }          // буруу шүүлт = 400
//	…
//	return c.JSON(fiber.Map{…, "filterable": query.Filterable(cols)})
//
// Шүүлтийн задлан шинжлэл/SQL бүтээлт нь `Run`-тэй ЯГ ижил код тул зан төлөв зөрөхгүй.

// ApplyFilters — `?filters=` JSON-ыг зарласан баганууд дээр query-д тавина.
// cols хоосон/nil бол query хэвээр буцна (шүүлт хаалттай).
func ApplyFilters(c fiber.Ctx, q *gorm.DB, cols map[string]FilterCol) (*gorm.DB, error) {
	conds, err := Spec{Filters: cols}.parseFilters(c.Query("filters"))
	if err != nil {
		return nil, err
	}
	return applyFilters(q, conds), nil
}

// Filterable — хариултын `filterable` талбар ({ түлхүүр: төрөл }). KvGrid үүгээр
// шүүлтийн мөрөө зурна; хоосон бол ОГТ зурахгүй.
func Filterable(cols map[string]FilterCol) map[string]FilterKind {
	return Spec{Filters: cols}.filterable()
}
