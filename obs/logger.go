package obs

import (
	"context"
	"log/slog"

	"github.com/gofiber/fiber/v3"
	"github.com/khan-vision/khan/tenant"
)

// Middleware — base logger-т request_id нэмж c.Context()-д хадгална (RequestID()-ийн
// ДАРАА бүртгэнэ). request id байхгүй бол base-ийг хэвээр хадгална.
func Middleware(base *slog.Logger) fiber.Handler {
	if base == nil {
		base = slog.Default()
	}
	return func(c fiber.Ctx) error {
		l := base
		if id := RequestIDFrom(c.Context()); id != "" {
			l = base.With("request_id", id)
		}
		c.SetContext(context.WithValue(c.Context(), loggerKey, l))
		return c.Next()
	}
}

// From — ctx дахь request-scoped logger; байхгүй бол slog.Default().
func From(ctx context.Context) *slog.Logger {
	if l, ok := fromOK(ctx); ok {
		return l
	}
	return slog.Default()
}

func fromOK(ctx context.Context) (*slog.Logger, bool) {
	l, ok := ctx.Value(loggerKey).(*slog.Logger)
	return l, ok && l != nil
}

// With — From(c.Context()) дээр JWT claims-д байгаа org_id, user_id (claim "id")
// нэмнэ. Auth middleware-ийн ДАРАА дуудвал утга гарна.
func With(c fiber.Ctx) *slog.Logger {
	l := From(c.Context())
	if claims, ok := tenant.Claims(c); ok {
		if org, ok := claims[tenant.ClaimOrgID].(string); ok && org != "" {
			l = l.With("org_id", org)
		}
		switch id := claims["id"].(type) {
		case string:
			if id != "" {
				l = l.With("user_id", id)
			}
		case float64:
			l = l.With("user_id", int64(id))
		}
	}
	return l
}
