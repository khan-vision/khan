package upload

import (
	"encoding/json"
	"errors"
	"io/fs"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/khan-vision/khan/auth"
	"github.com/khan-vision/khan/obs"
	"github.com/khan-vision/khan/tenant"
)

// AuthzFunc — ServePrivateAuth / SignHandler-ийн нэмэлт эрхийн шалгалт. orgID нь
// JWT-ээс (зам дахь org-той тэнцүү нь аль хэдийн баталгаажсан); opaquePath нь
// `private/<org>/...`. false буцаавал 403. nil бол "ижил байгууллагын гишүүн" л хангалттай.
type AuthzFunc func(c fiber.Ctx, orgID, opaquePath string) bool

const (
	errForbiddenMsg = "Хандах эрхгүй"
	maxSignBodySize = 4 << 10
)

func forbidden(c fiber.Ctx) error {
	return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": errForbiddenMsg})
}

// privateBase — хувийн хариу бүрийн аюулгүй байдлын header (алдаанд ч).
func privateBase(c fiber.Ctx) {
	c.Set(fiber.HeaderXContentTypeOptions, "nosniff")
	c.Set(fiber.HeaderReferrerPolicy, "no-referrer")
	c.Set(fiber.HeaderCacheControl, "private, no-store")
}

// privateOpaque — route-ийн `*` хэсгээс (эсвэл угтварыг хасч c.Path()-аас) opaque
// замыг гаргана: НЭГ удаа unescape, дараа нь хатуу бүтцийн шалгалт (сегмент бүр
// regexp-д таарах ёстой ⇒ `..`, `%`, `.`-эхлэл, нэмэлт сегмент боломжгүй).
func (u *Uploader) privateOpaque(c fiber.Ctx) (string, bool) {
	rest := c.Params("*")
	if rest == "" {
		p := c.Path()
		if !strings.HasPrefix(p, u.privPrefix+"/") {
			return "", false
		}
		rest = p[len(u.privPrefix)+1:]
	}
	rel, ok := unescapePath(rest)
	if !ok {
		return "", false
	}
	rel = strings.TrimPrefix(rel, "/")
	if _, _, ok := splitPrivatePath(rel); !ok {
		return "", false
	}
	if _, ok := u.rules[strings.Split(rel, "/")[2]]; !ok {
		return "", false
	}
	return rel, true
}

// storageRel — opaque замын `private/` угтварыг хасна.
func storageRel(opaque string) string { return strings.TrimPrefix(opaque, privateDir+"/") }

func methodOK(c fiber.Ctx) bool {
	m := c.Method()
	if m == fiber.MethodGet || m == fiber.MethodHead {
		return true
	}
	c.Set(fiber.HeaderAllow, "GET, HEAD")
	return false
}

// ServePrivate — GET/HEAD `<PrivatePrefix>/<private/...>?exp&kid&sig[&uid][&disp]`.
// Дараалал: зам бүтэц → гарын үсэг+хугацаа (бүх алдаа ижил 403, файл байгаа эсэхийг
// илтгэхгүй) → (uid-тэй бол) principal таарах → 404 зөвхөн ДАРАА нь. Header нь
// Serve-тэй ижил (nosniff, идэвхтэй төрөлд CSP sandbox, медиагүй бол attachment) ба
// `Cache-Control: private, max-age=<үлдсэн ttl>`.
//
// uid-тэй холбоос ажиллахын тулд route-ийн өмнө auth middleware (токен БАЙВАЛ
// уншиж, байхгүй бол үргэлжлүүлдэг — ж: optional) тавина; байхгүй бол 401.
func (u *Uploader) ServePrivate() fiber.Handler {
	return func(c fiber.Ctx) error {
		privateBase(c)
		if !methodOK(c) {
			return c.Status(fiber.StatusMethodNotAllowed).JSON(fiber.Map{"error": "Буруу method"})
		}
		log := obs.With(c)
		opaque, ok := u.privateOpaque(c)
		if !ok || u.signer == nil || u.privStorage == nil {
			return forbidden(c)
		}
		ver, err := u.signer.Verify(opaque, SignedParams{
			Exp: c.Query("exp"), KID: c.Query("kid"), Sig: c.Query("sig"),
			UserID: c.Query("uid"), Disposition: c.Query("disp"),
		})
		if err != nil {
			reason := "bad_signature"
			if errors.Is(err, ErrExpired) {
				reason = "expired"
			}
			log.Info("upload: хувийн холбоос татгалзсан", "reason", reason, "org", pathOrg(opaque))
			return forbidden(c)
		}
		if ver.UserID != "" {
			pr, ok := auth.User(c)
			if !ok {
				return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "Зөвшөөрөлгүй"})
			}
			if pr.ID != ver.UserID {
				log.Info("upload: хувийн холбоос өөр хэрэглэгчид", "org", ver.OrgID)
				return forbidden(c)
			}
		}
		maxAge := int64(ver.ExpiresAt.Sub(u.signer.now()).Seconds())
		if maxAge < 0 {
			maxAge = 0 // зөвхөн skew-ийн дотор
		}
		return u.sendPrivate(c, opaque, respondOpts{
			cacheControl: "private, max-age=" + strconv.FormatInt(maxAge, 10),
			disposition:  ver.Disposition, private: true,
		})
	}
}

func pathOrg(opaque string) string {
	o, _ := PrivateOrg(opaque)
	return o
}

func (u *Uploader) sendPrivate(c fiber.Ctx, opaque string, o respondOpts) error {
	f, fi, err := u.privStorage.Open(c.Context(), storageRel(opaque))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return notFound(c)
		}
		obs.From(c.Context()).Error("upload: хувийн файл нээх алдаа", "path", opaque, "error", err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Серверийн алдаа"})
	}
	return respondFile(c, f, fi, path.Base(opaque), o)
}

// ServePrivateAuth — гарын үсэггүй, нэвтэрсэн хэрэглэгчид: JWT-ийн org_id нь замын
// org-той ТЭНЦҮҮ байх ёстой (өөр org → 404, ямар ч оракулгүй), дараа нь authz
// (nil бол зөвхөн org-ын гишүүн). Cache-Control нь "private, max-age=0,
// must-revalidate" (ETag-ээр шалгана). `?disposition=attachment|inline` сонгож болно.
// Route нь auth middleware-ийн ард, `<ямар ч угтвар>/*` дээр байж болно.
func (u *Uploader) ServePrivateAuth(authz AuthzFunc) fiber.Handler {
	return func(c fiber.Ctx) error {
		privateBase(c)
		if !methodOK(c) {
			return c.Status(fiber.StatusMethodNotAllowed).JSON(fiber.Map{"error": "Буруу method"})
		}
		if _, ok := auth.User(c); !ok {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "Зөвшөөрөлгүй"})
		}
		orgID, err := tenant.OrgID(c)
		if err != nil {
			return forbidden(c)
		}
		opaque, ok := u.privateOpaque(c)
		if !ok || u.privStorage == nil || pathOrg(opaque) != orgID {
			return notFound(c)
		}
		if authz != nil && !authz(c, orgID, opaque) {
			return forbidden(c)
		}
		disp := c.Query("disposition")
		if !validDisposition(disp) {
			disp = ""
		}
		return u.sendPrivate(c, opaque, respondOpts{
			cacheControl: "private, max-age=0, must-revalidate", disposition: disp, private: true,
		})
	}
}

type signRequest struct {
	Path        string `json:"path"`
	TTLSeconds  int64  `json:"ttl_seconds"`
	Disposition string `json:"disposition"`
	BindUser    bool   `json:"bind_user"`
}

// SignHandler — нэвтэрсэн хэрэглэгчид хувийн файлын ШИНЭ гарын үсэгтэй URL өгнө.
// `GET ?path=&ttl_seconds=&disposition=&bind_user=` эсвэл `POST` JSON ижил талбартай.
// Шалгалт: principal, JWT-ийн org_id, path-ийн org тэнцүү (өөр org → 404), authz,
// файл байгаа. ttl нь Signer.MaxTTL-ээр хязгаарлагдана (хэтэрвэл тайрна).
// Хариу: `{"path","url","expires_at"}`. Signer тохируулаагүй бол 501.
func (u *Uploader) SignHandler(authz AuthzFunc) fiber.Handler {
	return func(c fiber.Ctx) error {
		privateBase(c)
		if u.signer == nil || u.privStorage == nil {
			return c.Status(fiber.StatusNotImplemented).JSON(fiber.Map{"error": "Гарын үсэгтэй URL тохируулагдаагүй"})
		}
		pr, ok := auth.User(c)
		if !ok {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "Зөвшөөрөлгүй"})
		}
		orgID, err := tenant.OrgID(c)
		if err != nil {
			return forbidden(c)
		}
		var req signRequest
		switch c.Method() {
		case fiber.MethodGet:
			req.Path = c.Query("path")
			req.TTLSeconds, _ = strconv.ParseInt(c.Query("ttl_seconds"), 10, 64)
			req.Disposition = c.Query("disposition")
			req.BindUser = c.Query("bind_user") == "true"
		case fiber.MethodPost:
			body := c.Body()
			if len(body) > maxSignBodySize || json.Unmarshal(body, &req) != nil {
				return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Хүсэлт буруу"})
			}
		default:
			c.Set(fiber.HeaderAllow, "GET, POST")
			return c.Status(fiber.StatusMethodNotAllowed).JSON(fiber.Map{"error": "Буруу method"})
		}
		opaque := req.Path
		if _, _, ok := splitPrivatePath(opaque); !ok || pathOrg(opaque) != orgID {
			return notFound(c) // бүтэц буруу / өөр org: "байхгүй"
		}
		if _, ok := u.rules[strings.Split(opaque, "/")[2]]; !ok {
			return notFound(c)
		}
		if !validDisposition(req.Disposition) {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Хүсэлт буруу"})
		}
		if authz != nil && !authz(c, orgID, opaque) {
			return forbidden(c)
		}
		if ok, err := u.privStorage.Exists(c.Context(), storageRel(opaque)); err != nil || !ok {
			return notFound(c)
		}
		ttl := u.privTTL
		if req.TTLSeconds > 0 {
			ttl = time.Duration(min(req.TTLSeconds, int64(u.signer.MaxTTL()/time.Second))) * time.Second
		}
		opts := SignOptions{Disposition: req.Disposition}
		if req.BindUser {
			opts.UserID = pr.ID
		}
		sr, err := u.signer.Sign(opaque, ttl, opts)
		if err != nil {
			obs.With(c).Error("upload: URL гарын үсэг зурах алдаа", "error", err)
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Серверийн алдаа"})
		}
		return c.JSON(fiber.Map{"path": opaque, "url": u.cfg.BaseURL + sr.URL, "expires_at": sr.ExpiresAt.Unix()})
	}
}

// MountPrivateServe — GET/HEAD `<PrivatePrefix>/*`-ийг ServePrivate-д (auth-гүй;
// uid-тэй холбоост optional auth middleware өмнө нь тавь).
func (u *Uploader) MountPrivateServe(r fiber.Router, mw ...fiber.Handler) {
	rest := make([]any, 0, len(mw))
	for _, m := range mw {
		rest = append(rest, m)
	}
	rest = append(rest, u.ServePrivate())
	r.Get(u.privPrefix+"/*", rest[0], rest[1:]...)
	r.Head(u.privPrefix+"/*", rest[0], rest[1:]...)
}
