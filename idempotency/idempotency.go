// Package idempotency нь `Idempotency-Key` толгойгоор давхар POST/PUT/PATCH/DELETE
// хүсэлтээс (төлбөр, цалин батлах, нэхэмжлэх үүсгэх, давхар товшилт) хамгаалах
// Fiber v3 middleware.
//
// Урсгал:
//   - Толгой байхгүй → шууд дамжина (Required=true бол 400).
//   - Түлхүүрийг (org_id, user_id, key)-ээр claim хийнэ. org_id болон user id нь
//     ЗӨВХӨН JWT claims-аас (tenant.Claims) — header-ээс хэзээ ч биш.
//   - Claim амжилттай бол handler ажиллаж, status < 500 хариуг хадгална. 5xx
//     (эсвэл алдаа/panic) бол мөрийг устгана — client дахин оролдож болно.
//   - Ижил түлхүүр + ижил хүсэлт, дууссан → хадгалсан хариуг `Idempotency-Replayed:
//     true` толгойтой буцаана. Ижил түлхүүр + өөр хүсэлт → 422. Ижил түлхүүр,
//     дуусаагүй (гүйж байгаа) → 409.
//
// Анхаар: процесс унасны улмаас "гүйж байгаа" хэвээр үлдсэн мөр TTL дуустал 409
// өгнө. Хүснэгтийг SchemaSQL-ээр үүсгэж, Purge-ийг үе үе дуудна.
//
//	app.Post("/api/finance/payments", jwtMW, idem, payHandler)
package idempotency

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/khan-vision/khan/tenant"
	"gorm.io/gorm"
)

// SchemaSQL — idempotency хүснэгтийн идемпотент DDL.
const SchemaSQL = `
CREATE SCHEMA IF NOT EXISTS khan;
CREATE TABLE IF NOT EXISTS khan.idempotency_keys (
	org_id        text NOT NULL DEFAULT '',
	user_id       text NOT NULL DEFAULT '',
	key           text NOT NULL,
	method        text NOT NULL,
	path          text NOT NULL,
	request_hash  text NOT NULL,
	status        int,
	response      bytea,
	content_type  text,
	created_at    timestamptz NOT NULL DEFAULT now(),
	completed_at  timestamptz,
	expires_at    timestamptz NOT NULL,
	PRIMARY KEY (org_id, user_id, key)
);
CREATE INDEX IF NOT EXISTS idempotency_keys_expires_idx ON khan.idempotency_keys (expires_at);
`

const (
	// Header — клиентийн илгээх толгойн нэр.
	Header = "Idempotency-Key"
	// ReplayedHeader — хадгалсан хариу давтагдсаныг заана.
	ReplayedHeader = "Idempotency-Replayed"
	// DefaultTTL — түлхүүрийн анхдагч хүчинтэй хугацаа.
	DefaultTTL = 24 * time.Hour

	table = "khan.idempotency_keys"
)

var keyRe = regexp.MustCompile(`^[A-Za-z0-9_:.\-]{1,128}$`)

// Config — middleware-ийн тохиргоо.
type Config struct {
	DB       *gorm.DB      // заавал
	TTL      time.Duration // 0 → 24 цаг
	Required bool          // true бол толгойгүй хүсэлт 400
	Methods  []string      // nil → POST, PUT, PATCH, DELETE
}

type record struct {
	RequestHash string
	CompletedAt *time.Time
	Status      *int
	Response    []byte
	ContentType *string
}

// New — middleware үүсгэнэ. JWT middleware-ийн ДАРАА бүртгэнэ.
func New(cfg Config) (fiber.Handler, error) {
	if cfg.DB == nil {
		return nil, errors.New("idempotency: Config.DB nil")
	}
	ttl := cfg.TTL
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	methods := cfg.Methods
	if len(methods) == 0 {
		methods = []string{fiber.MethodPost, fiber.MethodPut, fiber.MethodPatch, fiber.MethodDelete}
	}
	set := make(map[string]bool, len(methods))
	for _, m := range methods {
		set[m] = true
	}
	s := &store{db: cfg.DB, ttl: ttl}

	return func(c fiber.Ctx) error {
		if !set[c.Method()] {
			return c.Next()
		}
		key := c.Get(Header)
		if key == "" {
			if cfg.Required {
				return fail(c, fiber.StatusBadRequest, "Idempotency-Key толгой шаардлагатай")
			}
			return c.Next()
		}
		if !keyRe.MatchString(key) {
			return fail(c, fiber.StatusBadRequest, "Idempotency-Key буруу (1–128 тэмдэгт: үсэг, тоо, _ - : .)")
		}
		orgID, userID, ok := scope(c)
		if !ok {
			return fail(c, fiber.StatusUnauthorized, "Зөвшөөрөлгүй")
		}
		hash := hashRequest(c.Method(), c.OriginalURL(), c.Body())
		ctx := context.Background()

		for attempt := 0; attempt < 3; attempt++ {
			claimed, err := s.claim(ctx, orgID, userID, key, c.Method(), c.Path(), hash)
			if err != nil {
				return fail(c, fiber.StatusInternalServerError, "Idempotency түлхүүр хадгалах үед алдаа гарлаа")
			}
			if claimed {
				return s.run(c, orgID, userID, key)
			}
			rec, err := s.find(ctx, orgID, userID, key)
			if err != nil {
				return fail(c, fiber.StatusInternalServerError, "Idempotency түлхүүр унших үед алдаа гарлаа")
			}
			if rec == nil { // claim ба find хооронд устсан/хугацаа дууссан → дахин
				continue
			}
			switch {
			case rec.RequestHash != hash:
				return fail(c, fiber.StatusUnprocessableEntity, "Энэ Idempotency-Key өөр агуулгатай хүсэлтэд ашиглагдсан байна")
			case rec.CompletedAt == nil || rec.Status == nil:
				return fail(c, fiber.StatusConflict, "Ижил Idempotency-Key-тэй хүсэлт боловсруулагдаж байна")
			default:
				return replay(c, rec)
			}
		}
		return fail(c, fiber.StatusConflict, "Ижил Idempotency-Key-тэй хүсэлт боловсруулагдаж байна")
	}, nil
}

type store struct {
	db  *gorm.DB
	ttl time.Duration
}

// claim — хугацаа дууссан мөрийг цэвэрлээд INSERT ... ON CONFLICT DO NOTHING.
func (s *store) claim(ctx context.Context, org, user, key, method, path, hash string) (bool, error) {
	if err := s.db.WithContext(ctx).Exec(`DELETE FROM `+table+`
		WHERE org_id = ? AND user_id = ? AND key = ? AND expires_at <= now()`, org, user, key).Error; err != nil {
		return false, err
	}
	res := s.db.WithContext(ctx).Exec(`INSERT INTO `+table+`
		(org_id, user_id, key, method, path, request_hash, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, now() + make_interval(secs => ?))
		ON CONFLICT DO NOTHING`, org, user, key, method, path, hash, s.ttl.Seconds())
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}

func (s *store) find(ctx context.Context, org, user, key string) (*record, error) {
	var recs []record
	err := s.db.WithContext(ctx).Raw(`SELECT request_hash, completed_at, status, response, content_type
		FROM `+table+` WHERE org_id = ? AND user_id = ? AND key = ? AND expires_at > now()`,
		org, user, key).Scan(&recs).Error
	if err != nil || len(recs) == 0 {
		return nil, err
	}
	return &recs[0], nil
}

func (s *store) release(org, user, key string) {
	// Хүсэлтийн ctx цуцлагдсан байсан ч цэвэрлэнэ.
	_ = s.db.WithContext(context.Background()).Exec(`DELETE FROM `+table+`
		WHERE org_id = ? AND user_id = ? AND key = ?`, org, user, key).Error
}

// run — claim хийсэн хүсэлтийн handler-ийг ажиллуулж, хариуг хадгална.
func (s *store) run(c fiber.Ctx, org, user, key string) (err error) {
	stored := false
	defer func() {
		if !stored { // алдаа, panic, 5xx, хадгалж чадаагүй
			s.release(org, user, key)
		}
	}()
	if err = c.Next(); err != nil {
		return err
	}
	status := c.Response().StatusCode()
	if status >= 500 {
		return nil
	}
	body := append([]byte(nil), c.Response().Body()...)
	ctype := string(c.Response().Header.ContentType())
	res := s.db.WithContext(context.Background()).Exec(`UPDATE `+table+`
		SET status = ?, response = ?, content_type = ?, completed_at = now()
		WHERE org_id = ? AND user_id = ? AND key = ?`, status, body, ctype, org, user, key)
	if res.Error == nil {
		stored = true
	}
	return nil // handler амжилттай; хадгалж чадаагүй ч хариуг client-д өгнө
}

func replay(c fiber.Ctx, rec *record) error {
	c.Set(ReplayedHeader, "true")
	if rec.ContentType != nil && *rec.ContentType != "" {
		c.Set(fiber.HeaderContentType, *rec.ContentType)
	}
	return c.Status(*rec.Status).Send(rec.Response)
}

func fail(c fiber.Ctx, status int, msg string) error {
	return c.Status(status).JSON(fiber.Map{"error": msg})
}

// scope — JWT claims-аас (org_id, user id). Claims байхгүй эсвэл id хоосон бол
// ok=false (fail-closed: бүгдэд нийтлэг scope үүсгэхгүй). org_id хоосон байж болно
// (org сонгоогүй login token).
func scope(c fiber.Ctx) (org, user string, ok bool) {
	claims, has := tenant.Claims(c)
	if !has {
		return "", "", false
	}
	if v, isStr := claims[tenant.ClaimOrgID].(string); isStr {
		org = v
	}
	switch v := claims["id"].(type) {
	case string:
		user = v
	case float64:
		user = strconv.FormatFloat(v, 'f', -1, 64)
	case nil:
	default:
		user = fmt.Sprint(v)
	}
	return org, user, user != ""
}

// hashRequest — sha256(method + path + body); талбаруудыг NUL-ээр тусгаарлана.
func hashRequest(method, path string, body []byte) string {
	h := sha256.New()
	h.Write([]byte(method))
	h.Write([]byte{0})
	h.Write([]byte(path))
	h.Write([]byte{0})
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

// Purge — хугацаа дууссан түлхүүрүүдийг устгана; устгасан тоог буцаана.
func Purge(ctx context.Context, gdb *gorm.DB) (int64, error) {
	if gdb == nil {
		return 0, errors.New("idempotency: *gorm.DB nil")
	}
	res := gdb.WithContext(ctx).Exec(`DELETE FROM ` + table + ` WHERE expires_at <= now()`)
	if res.Error != nil {
		return 0, fmt.Errorf("idempotency: purge: %w", res.Error)
	}
	return res.RowsAffected, nil
}
