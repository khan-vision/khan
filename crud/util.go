package crud

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/khan-vision/khan/auth"
	"github.com/khan-vision/khan/validate"
)

// newUUID — crypto/rand UUIDv4.
func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("khan/crud: crypto/rand: " + err.Error()) // OS CSPRNG доголдох нь сэргээгдэхгүй
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// parseID — path параметрийг id-ийн төрлөөр шалгана. val нь DB-д дамжих утга, str нь каноник мөр.
func (e *engine) parseID(raw string) (val any, str string, ok bool) {
	switch e.idKind {
	case IDInt:
		if raw == "" || len(raw) > 19 {
			return nil, "", false
		}
		for i := 0; i < len(raw); i++ {
			if raw[i] < '0' || raw[i] > '9' {
				return nil, "", false
			}
		}
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n <= 0 {
			return nil, "", false
		}
		return n, strconv.FormatInt(n, 10), true
	default:
		if !validate.IsUUID(raw) {
			return nil, "", false
		}
		s := strings.ToLower(raw)
		return s, s, true
	}
}

// dbValue — parse хийсэн утгыг драйверт тохирох хэлбэрт оруулна.
func dbValue(v any) any {
	if m, ok := v.(json.RawMessage); ok {
		return string(m)
	}
	return v
}

// asInt64 — драйверийн бүхэл тоог (int32/int64/…) int64 болгоно.
func asInt64(v any) (int64, bool) {
	switch t := v.(type) {
	case int:
		return int64(t), true
	case int8:
		return int64(t), true
	case int16:
		return int64(t), true
	case int32:
		return int64(t), true
	case int64:
		return t, true
	case uint:
		return int64(t), true
	case uint32:
		return int64(t), true
	case float64:
		return int64(t), t == float64(int64(t))
	case string:
		n, err := strconv.ParseInt(t, 10, 64)
		return n, err == nil
	case []byte:
		n, err := strconv.ParseInt(string(t), 10, 64)
		return n, err == nil
	}
	return 0, false
}

// userID — JWT-ийн хэрэглэгчийн id ("" = байхгүй).
func userID(c fiber.Ctx) string {
	if p, ok := auth.User(c); ok {
		return p.ID
	}
	return ""
}

// shapeRow — DB-ээс уншсан мөрийг API хариуны хэлбэрт оруулна:
// Password баганыг хасна, талбарын Column→Name болгоно, Date → "YYYY-MM-DD",
// JSON → түүхий JSON, []byte → string.
func shapeRow(row map[string]any, fields []fieldPlan, pw map[string]bool) map[string]any {
	for col := range pw {
		delete(row, col)
	}
	for k, v := range row {
		if b, ok := v.([]byte); ok {
			row[k] = string(b)
		}
	}
	for i := range fields {
		f := &fields[i]
		v, ok := row[f.col]
		if !ok {
			continue
		}
		switch {
		case f.Kind == validate.Date:
			if t, isTime := v.(time.Time); isTime {
				v = t.Format("2006-01-02")
			}
		case f.Kind == validate.JSON:
			if s, isStr := v.(string); isStr && json.Valid([]byte(s)) {
				v = json.RawMessage(s)
			}
		}
		if f.col != f.Name {
			delete(row, f.col)
		}
		row[f.Name] = v
	}
	return row
}

// idParam — id-тай харьцуулах placeholder. IDInt үед `CAST(? AS bigint)`: int4 баганад
// int4-өөс их id (ж: 2147483648) өгвөл pgx encode алдаа (500) биш, мөр олдохгүй (404) болно.
func (e *engine) idParam() string {
	if e.idKind == IDInt {
		return "CAST(? AS bigint)"
	}
	return "?"
}
