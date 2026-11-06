// Package obs нь khan app-ийн observability багц: request id, request-scoped
// slog, access log, нууц далдлалт, health/readiness, GORM-ийн slog logger.
//
// Middleware-ийн дараалал (гаднаас дотогш):
//
//	app.Use(obs.RequestID())          // X-Request-ID
//	app.Use(obs.Middleware(logger))   // ctx-д request_id-тай slog
//	app.Use(obs.AccessLog())          // 1 хүсэлт = 1 мөр
//	app.Use(auth.Optional(kr))        // дараа нь auth, tenant …
//	obs.Health(app, map[string]obs.Check{"db": obs.DBCheck(gdb)})
//
// GORM-д ctx дамжуулахыг март: `gdb.WithContext(c.Context())`.
package obs

import (
	"context"
	"crypto/rand"
	"encoding/hex"

	"github.com/gofiber/fiber/v3"
)

// HeaderRequestID — request id-ийн header.
const HeaderRequestID = "X-Request-ID"

// LocalsRequestID — request id хадгалагдах c.Locals түлхүүр.
const LocalsRequestID = "khan.request_id"

type ctxKey int

const (
	requestIDKey ctxKey = iota
	loggerKey
)

// ValidRequestID — 1–64 тэмдэгт, зөвхөн [A-Za-z0-9._-] (log injection / header
// урсгалаас сэргийлнэ).
func ValidRequestID(id string) bool {
	if len(id) == 0 || len(id) > 64 {
		return false
	}
	for i := 0; i < len(id); i++ {
		ch := id[i]
		switch {
		case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z', ch >= '0' && ch <= '9',
			ch == '.', ch == '_', ch == '-':
		default:
			return false
		}
	}
	return true
}

// NewRequestID — crypto/rand-аас 16 байт, hex (32 тэмдэгт).
func NewRequestID() string {
	var b [16]byte
	_, _ = rand.Read(b[:]) // Go 1.24+: алдаа буцаахгүй (боломжгүй бол процессыг зогсооно)
	return hex.EncodeToString(b[:])
}

// RequestID — орж ирсэн X-Request-ID хүчинтэй бол хэвээр, үгүй бол шинээр
// үүсгэж response header, c.Locals болон c.Context()-д хадгална.
func RequestID() fiber.Handler {
	return func(c fiber.Ctx) error {
		id := c.Get(HeaderRequestID)
		if !ValidRequestID(id) {
			id = NewRequestID()
		}
		c.Set(HeaderRequestID, id)
		c.Locals(LocalsRequestID, id)
		c.SetContext(context.WithValue(c.Context(), requestIDKey, id))
		return c.Next()
	}
}

// RequestIDFrom — ctx дахь request id ("" бол байхгүй).
func RequestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}
