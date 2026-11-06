package auth

import (
	"strings"

	"github.com/gofiber/fiber/v3"
	"github.com/khan-vision/khan/tenant"
)

// CookieName — token-ийн анхдагч cookie нэр (хуучин lambda-тай ижил).
const CookieName = "token"

const unauthorizedMessage = "Зөвшөөрөлгүй"

// WithCookieName — token cookie-ийн нэр (анхдагч "token").
func WithCookieName(name string) Option {
	return func(o *options) { o.cookieName = name }
}

// WithUnauthorized — 401-ийг өөрөөр бичих (ж: RedirectToLogin).
func WithUnauthorized(fn func(c fiber.Ctx) error) Option {
	return func(o *options) { o.unauthorized = fn }
}

// RedirectToLogin — AJAX биш, HTML хүлээж буй GET хүсэлтийг loginURL руу 302-оор
// чиглүүлж, бусдад анхдагч JSON 401 өгнө. WithUnauthorized-д дамжуулна.
func RedirectToLogin(loginURL string) func(c fiber.Ctx) error {
	return func(c fiber.Ctx) error {
		if c.Method() == fiber.MethodGet &&
			!strings.EqualFold(c.Get("X-Requested-With"), "XMLHttpRequest") &&
			strings.Contains(c.Get(fiber.HeaderAccept), "text/html") {
			return c.Redirect().Status(fiber.StatusFound).To(loginURL)
		}
		return defaultUnauthorized(c)
	}
}

func defaultUnauthorized(c fiber.Ctx) error {
	return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": unauthorizedMessage})
}

// Required — `Authorization: Bearer <token>` header, байхгүй бол `token` cookie-оос
// token уншиж шалгана. Амжилттай бол c.Locals(tenant.LocalsKey)-д *jwt.Token
// (jwt.MapClaims) тавина — tenant.Require шууд ажиллана. Token байхгүй / буруу бол
// 401 `{"error":"Зөвшөөрөлгүй"}`.
func Required(kr *Keyring, opts ...Option) fiber.Handler {
	return handler(kr, true, opts)
}

// Optional — Required-тэй адил боловч token байхгүй / буруу бол хүсэлтийг
// нэргүй (locals-гүй) үргэлжлүүлнэ.
func Optional(kr *Keyring, opts ...Option) fiber.Handler {
	return handler(kr, false, opts)
}

func handler(kr *Keyring, required bool, opts []Option) fiber.Handler {
	o := newOptions(opts)
	deny := o.unauthorized
	if deny == nil {
		deny = defaultUnauthorized
	}
	return func(c fiber.Ctx) error {
		raw := tokenFromRequest(c, o.cookieName)
		if raw == "" {
			if required {
				return deny(c)
			}
			return c.Next()
		}
		tok, err := kr.Parse(raw, opts...)
		if err != nil {
			if required {
				return deny(c)
			}
			return c.Next()
		}
		c.Locals(tenant.LocalsKey, tok)
		return c.Next()
	}
}

// tokenFromRequest — Bearer header түрүүлнэ; header байвал cookie руу ШИЛЖИХГҮЙ.
func tokenFromRequest(c fiber.Ctx, cookie string) string {
	if h := strings.TrimSpace(c.Get(fiber.HeaderAuthorization)); h != "" {
		scheme, rest, ok := strings.Cut(h, " ")
		if ok && strings.EqualFold(scheme, "Bearer") {
			return strings.TrimSpace(rest)
		}
		return ""
	}
	return strings.TrimSpace(c.Cookies(cookie))
}
