package audit

import (
	"strconv"

	"github.com/gofiber/fiber/v3"
	"github.com/khan-vision/khan/auth"
	"github.com/khan-vision/khan/obs"
)

// Meta — өөрчлөлтийг хэн, хаанаас хийснийг тодорхойлно.
type Meta struct {
	ActorID   string // auth.Principal.ID ("" = систем/үл мэдэгдэх)
	ActorRole string // role (текст; ж: "1")
	IP        string
	UserAgent string // Record хадгалахдаа 255 тэмдэгтээр таслана
	RequestID string // obs.RequestIDFrom
}

// MetaFrom — Fiber хүсэлтээс хэн/хаанаас/request id-г авна. Хэрэглэгч нэвтрээгүй бол
// ActorID хоосон. IP нь Fiber-ийн c.IP() (proxy итгэх тохиргоог app өөрөө хийнэ: ProxyHeader,
// TrustProxy) — клиентийн header-ийг өөрөө бүү итгэ.
func MetaFrom(c fiber.Ctx) Meta {
	m := Meta{
		IP:        c.IP(),
		UserAgent: c.Get(fiber.HeaderUserAgent),
		RequestID: obs.RequestIDFrom(c.Context()),
	}
	if p, ok := auth.User(c); ok {
		m.ActorID = p.ID
		m.ActorRole = strconv.Itoa(p.Role)
	}
	return m
}
