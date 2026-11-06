package obs

import (
	"errors"
	"log/slog"
	"time"

	"github.com/gofiber/fiber/v3"
)

// AccessLogOption — AccessLog-ийн сонголт.
type AccessLogOption func(*accessLogOptions)

type accessLogOptions struct {
	skip map[string]struct{}
}

// WithSkipPaths — эдгээр яг path-ийг лог хийхгүй (ж: "/healthz", "/readyz").
func WithSkipPaths(paths ...string) AccessLogOption {
	return func(o *accessLogOptions) {
		for _, p := range paths {
			o.skip[p] = struct{}{}
		}
	}
}

// AccessLog — хүсэлт бүрт нэг мөр: method, path (route-ын загвар; query string
// ОРОХГҮЙ — token агуулж болно), status, duration_ms, bytes, ip. Түвшин: 2xx/3xx
// Info, 4xx Warn, 5xx Error. Logger = With(c) (request_id, org_id, user_id).
func AccessLog(opts ...AccessLogOption) fiber.Handler {
	o := accessLogOptions{skip: map[string]struct{}{}}
	for _, fn := range opts {
		fn(&o)
	}
	return func(c fiber.Ctx) error {
		if _, skip := o.skip[c.Path()]; skip {
			return c.Next()
		}
		start := time.Now()
		err := c.Next()

		status := c.Response().StatusCode()
		if err != nil {
			var fe *fiber.Error
			if errors.As(err, &fe) {
				status = fe.Code
			} else {
				status = fiber.StatusInternalServerError
			}
		}
		path := c.Path() // query string-гүй
		if r := c.Route(); r != nil && r.Path != "" && r.Path != "/" {
			path = r.Path
		}
		level := slog.LevelInfo
		switch {
		case status >= 500:
			level = slog.LevelError
		case status >= 400:
			level = slog.LevelWarn
		}
		attrs := []slog.Attr{
			slog.String("method", c.Method()),
			slog.String("path", path),
			slog.Int("status", status),
			slog.Float64("duration_ms", float64(time.Since(start).Microseconds())/1000),
			slog.Int("bytes", len(c.Response().Body())),
			slog.String("ip", c.IP()),
		}
		if err != nil {
			attrs = append(attrs, slog.String("error", RedactString(err.Error())))
		}
		With(c).LogAttrs(c.Context(), level, "http", attrs...)
		return err
	}
}
