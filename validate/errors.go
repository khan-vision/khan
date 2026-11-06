package validate

import (
	"errors"
	"sort"
	"strings"

	"github.com/gofiber/fiber/v3"
)

// Errors — талбар → мессежүүд. front_end `serverErrors.ts` уншдаг `errors` хэлбэр.
// Хоосон map-д Add хийхээс өмнө NewErrors() (эсвэл make) ашиглана.
type Errors map[string][]string

// NewErrors — хоосон Errors.
func NewErrors() Errors { return Errors{} }

// Add — талбарт мессеж нэмнэ (ижил мессежийг давхардуулахгүй).
func (e Errors) Add(field, msg string) {
	for _, m := range e[field] {
		if m == msg {
			return
		}
	}
	e[field] = append(e[field], msg)
}

// Merge — өөр Errors-ийг нэгтгэнэ.
func (e Errors) Merge(o Errors) {
	for f, ms := range o {
		for _, m := range ms {
			e.Add(f, m)
		}
	}
}

// Len — алдаатай талбарын тоо.
func (e Errors) Len() int { return len(e) }

// Err — хоосон бол nil, үгүй бол e өөрөө.
func (e Errors) Err() error {
	if len(e) == 0 {
		return nil
	}
	return e
}

// Error — тогтвортой (талбараар эрэмблэсэн) нэг мөр тайлбар (лог/тест).
func (e Errors) Error() string {
	fields := make([]string, 0, len(e))
	for f := range e {
		fields = append(fields, f)
	}
	sort.Strings(fields)
	parts := make([]string, 0, len(fields))
	for _, f := range fields {
		parts = append(parts, f+": "+strings.Join(e[f], "; "))
	}
	return "Мэдээлэл буруу: " + strings.Join(parts, " | ")
}

// Message — 422 хариултын ерөнхий мессеж.
const Message = "Мэдээлэл буруу"

// Write — err нь (wrap хийгдсэн ч) Errors бол 422 envelope бичнэ:
//
//	{"error":"Мэдээлэл буруу","errors":{"field":["msg"]}}
//
// Бусад алдааг өөрчлөхгүй буцаана (caller өөрөө боловсруулна). nil → nil.
func Write(c fiber.Ctx, err error) error {
	if err == nil {
		return nil
	}
	var ve Errors
	if errors.As(err, &ve) {
		return c.Status(fiber.StatusUnprocessableEntity).JSON(fiber.Map{"error": Message, "errors": map[string][]string(ve)})
	}
	return err
}
