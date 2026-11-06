// Package auth нь HS256 JWT (түлхүүр эргүүлэлттэй), Fiber v3 middleware,
// bcrypt нууц үг болон mutex-тэй login lockout / rate limiter-ийг агуулна.
//
// Яагаад түлхүүр эргүүлэлт: JWT_SECRET-ийг нэг удаа соливол бүх хэрэглэгч
// logout болдог байв (10/29). Keyring нь идэвхтэй түлхүүр + өмнөх түлхүүрүүдтэй:
// шинэ token идэвхтэй түлхүүрээр гарын үсэг зурагдаж `kid` header авна, харин
// өмнөх түлхүүрээр гарсан token (мөн `kid`-гүй хуучин lambda token) хүчинтэй
// хэвээр үлдэнэ.
//
// Claim нэрс хуучин системтэй ИЖИЛ: id, login, role, org_id, extra_role,
// portal_only, sid, exp. Алгоритм зөвхөн HS256 (none / RS* / бусад татгалзана).
//
//	kr, err := auth.KeyringFromConfig(cfg.JWT)
//	app.Use(auth.Required(kr))
//	app.Get("/me", func(c fiber.Ctx) error {
//	    u, _ := auth.User(c)
//	    return c.JSON(u)
//	})
package auth

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/golang-jwt/jwt/v5"
	"github.com/khan-vision/khan/config"
)

// Алдаанууд.
var (
	// ErrInvalidToken — token задрах, гарын үсэг, алгоритм, хугацаа буруу.
	ErrInvalidToken = errors.New("auth: token буруу")
	// ErrUnknownKey — token-ий `kid` keyring-д байхгүй.
	ErrUnknownKey = errors.New("auth: үл мэдэгдэх түлхүүр (kid)")
)

// MethodHS256 — зөвшөөрөгдсөн цорын ганц алгоритм.
const MethodHS256 = "HS256"

var keyIDRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// Key — нэг гарын үсгийн түлхүүр.
type Key struct {
	ID     string // token header `kid`
	Secret []byte
}

// Keyring — идэвхтэй түлхүүр (гарын үсэг зурна) + өмнөх түлхүүрүүд (зөвхөн шалгана).
// Үүсгэсний дараа өөрчлөгддөггүй тул олон goroutine-д аюулгүй.
type Keyring struct {
	active   Key
	previous []Key
}

// NewKeyring — түлхүүр бүрийн secret ≥ config.MinJWTSecretLen, ID нь хоосон биш
// `[A-Za-z0-9._-]{1,64}` бөгөөд давхцаагүй байх ёстой.
func NewKeyring(active Key, previous ...Key) (*Keyring, error) {
	all := append([]Key{active}, previous...)
	seen := make(map[string]struct{}, len(all))
	for _, k := range all {
		if !keyIDRe.MatchString(k.ID) {
			return nil, fmt.Errorf("auth: түлхүүрийн id %q буруу ([A-Za-z0-9._-], 1–64 тэмдэгт)", k.ID)
		}
		if len(k.Secret) < config.MinJWTSecretLen {
			return nil, fmt.Errorf("auth: %q түлхүүрийн secret хамгийн багадаа %d тэмдэгт байх ёстой", k.ID, config.MinJWTSecretLen)
		}
		if _, dup := seen[k.ID]; dup {
			return nil, fmt.Errorf("auth: түлхүүрийн id %q давхцаж байна", k.ID)
		}
		seen[k.ID] = struct{}{}
	}
	cp := make([]Key, len(previous))
	copy(cp, previous)
	return &Keyring{active: active, previous: cp}, nil
}

// KeyringFromConfig — JWT.Secret/KeyID идэвхтэй, JWT.PreviousSecrets өмнөх түлхүүр.
// Өмнөх түлхүүрийн элемент `kid:secret` (kid нь [A-Za-z0-9._-]{1,64}) эсвэл зөвхөн
// secret (id нь "prev1", "prev2" …). Зөвхөн secret бичихэд secret-д ':' байж
// БОЛОХГҮЙ (kid:secret гэж ташаа задрана) — тийм бол kid өгнө.
// Алдааны мессежид secret ОГТ орохгүй.
func KeyringFromConfig(c config.JWT) (*Keyring, error) {
	id := c.KeyID
	if id == "" {
		id = "k1"
	}
	prev := make([]Key, 0, len(c.PreviousSecrets))
	for i, entry := range c.PreviousSecrets {
		k := Key{ID: fmt.Sprintf("prev%d", i+1), Secret: []byte(entry)}
		if kid, secret, ok := strings.Cut(entry, ":"); ok && keyIDRe.MatchString(kid) {
			k = Key{ID: kid, Secret: []byte(secret)}
		}
		prev = append(prev, k)
	}
	return NewKeyring(Key{ID: id, Secret: []byte(c.Secret)}, prev...)
}

// ActiveID — гарын үсэг зурдаг түлхүүрийн id.
func (kr *Keyring) ActiveID() string { return kr.active.ID }

// Sign — идэвхтэй түлхүүрээр HS256 гарын үсэг зурж, header-д `kid` тавина.
func (kr *Keyring) Sign(claims jwt.Claims) (string, error) {
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tok.Header["kid"] = kr.active.ID
	return tok.SignedString(kr.active.Secret)
}

// Option — Parse болон middleware-ийн сонголт.
type Option func(*options)

type options struct {
	leeway       time.Duration
	cookieName   string
	unauthorized func(fiber.Ctx) error
}

// WithLeeway — exp шалгахад зөвшөөрөх цагийн зөрүү (серверүүдийн цаг зөрөх).
func WithLeeway(d time.Duration) Option {
	return func(o *options) { o.leeway = d }
}

func newOptions(opts []Option) options {
	o := options{cookieName: "token"}
	for _, fn := range opts {
		fn(&o)
	}
	return o
}

// Parse — token-ийг шалгана: зөвхөн HS256, `exp` заавал. `kid` байвал ЗӨВХӨН тэр
// түлхүүрээр (үл мэдэгдэх kid → ErrUnknownKey); `kid`-гүй (хуучин lambda) token-д
// идэвхтэй, дараа нь өмнөх түлхүүрүүдийг дарааллаар оролдоно.
func (kr *Keyring) Parse(raw string, opts ...Option) (*jwt.Token, error) {
	o := newOptions(opts)
	parserOpts := []jwt.ParserOption{
		jwt.WithValidMethods([]string{MethodHS256}),
		jwt.WithExpirationRequired(),
	}
	if o.leeway > 0 {
		parserOpts = append(parserOpts, jwt.WithLeeway(o.leeway))
	}
	parser := jwt.NewParser(parserOpts...)

	// Header-ийг шалгаж kid-ийг нэг удаа тодорхойлно (гарын үсэг шалгахаас өмнө).
	unverified, _, err := jwt.NewParser(jwt.WithValidMethods([]string{MethodHS256})).ParseUnverified(raw, jwt.MapClaims{})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidToken, err)
	}
	if unverified.Method == nil || unverified.Method.Alg() != MethodHS256 {
		return nil, fmt.Errorf("%w: алгоритм зөвхөн %s", ErrInvalidToken, MethodHS256)
	}
	if rawKid, present := unverified.Header["kid"]; present {
		kid, ok := rawKid.(string)
		if !ok {
			return nil, fmt.Errorf("%w: kid string биш", ErrInvalidToken)
		}
		key, found := kr.lookup(kid)
		if !found {
			return nil, fmt.Errorf("%w: %q", ErrUnknownKey, kid)
		}
		tok, err := parser.Parse(raw, staticKey(key))
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidToken, err)
		}
		return tok, nil
	}

	var firstErr error
	for _, key := range append([]Key{kr.active}, kr.previous...) {
		tok, err := parser.Parse(raw, staticKey(key))
		if err == nil {
			return tok, nil
		}
		// Гарын үсэг таарсан боловч өөр шалтгаанаар (ж: хугацаа дууссан) унасан
		// алдааг илүүд үзнэ — "signature invalid"-аас илүү мэдээлэлтэй.
		if firstErr == nil || !errors.Is(err, jwt.ErrTokenSignatureInvalid) {
			firstErr = err
		}
	}
	return nil, fmt.Errorf("%w: %w", ErrInvalidToken, firstErr)
}

func (kr *Keyring) lookup(kid string) (Key, bool) {
	if kr.active.ID == kid {
		return kr.active, true
	}
	for _, k := range kr.previous {
		if k.ID == kid {
			return k, true
		}
	}
	return Key{}, false
}

func staticKey(k Key) jwt.Keyfunc {
	return func(*jwt.Token) (any, error) { return k.Secret, nil }
}

// Issue — claims-ийн хуулбарт iat/exp (now, now+ttl) тавьж идэвхтэй түлхүүрээр
// гарын үсэг зурна. Оролтын claims-ийг өөрчлөхгүй. ttl > 0 байх ёстой.
func Issue(kr *Keyring, claims jwt.MapClaims, ttl time.Duration) (string, error) {
	if kr == nil {
		return "", errors.New("auth: keyring хоосон")
	}
	if ttl <= 0 {
		return "", errors.New("auth: ttl эерэг байх ёстой")
	}
	cp := make(jwt.MapClaims, len(claims)+2)
	for k, v := range claims {
		cp[k] = v
	}
	now := time.Now()
	cp["iat"] = now.Unix()
	cp["exp"] = now.Add(ttl).Unix()
	return kr.Sign(cp)
}
