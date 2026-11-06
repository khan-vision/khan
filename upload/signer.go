package upload

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/khan-vision/khan/config"
)

// Хувийн файлын гарын үсгийн алдаанууд. Клиентэд ХЭЗЭЭ Ч ялгаж харуулахгүй (бүгд
// ижил 403) — зөвхөн лог/тестэд ялгана.
var (
	// ErrBadSignature — гарын үсэг, kid, exp, зам, хэрэглэгч, disposition аль нэг нь буруу.
	ErrBadSignature = errors.New("upload: гарын үсэг буруу")
	// ErrExpired — гарын үсэг зөв боловч хугацаа дууссан.
	ErrExpired = errors.New("upload: холбоосын хугацаа дууссан")
	// ErrTTLTooLong — хүссэн ttl Signer-ийн MaxTTL-ээс их.
	ErrTTLTooLong = errors.New("upload: ttl дээд хязгаараас их байна")
)

// Signer-ийн анхдагч утгууд.
const (
	DefaultPrivatePrefix = "/files/private"
	DefaultSignMaxTTL    = time.Hour
	DefaultSignSkew      = 30 * time.Second
	maxSignSkew          = 5 * time.Minute
	maxUserIDLen         = 128
	signDomain           = "khan-upload-sign-v1"
	privateDir           = "private" // opaque замын эхний сегмент
)

// Disposition-ийн зөвшөөрөгдсөн утгууд (SignOptions.Disposition).
const (
	DispositionInline     = "inline"
	DispositionAttachment = "attachment"
)

var (
	signKeyIDRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	orgSegRe    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
	catSegRe    = regexp.MustCompile(`^[a-z]{1,32}$`)
	yearSegRe   = regexp.MustCompile(`^[0-9]{4}$`)
	monthSegRe  = regexp.MustCompile(`^[0-9]{2}$`)
	nameSegRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,254}$`)
)

// SignKey — гарын үсгийн нэг түлхүүр.
type SignKey struct {
	ID     string // URL-ийн `kid`: [A-Za-z0-9._-]{1,64}
	Secret []byte // ≥ config.MinJWTSecretLen байт; JWT_SECRET-ээс ӨӨР, зориулалтын түлхүүр
}

// SignOptions — SignURL-ийн нэмэлт хязгаарлалт.
type SignOptions struct {
	// UserID — хоосон биш бол холбоос зөвхөн энэ хэрэглэгчид (auth.Principal.ID)
	// ажиллана: ServePrivate нэвтэрсэн, ID таарсан principal шаардана. Гарын
	// үсэгт орсон тул query-ээс хасаад "зүгээр холбоос" болгох боломжгүй.
	UserID string
	// Disposition — "" (файлын төрлөөс), "inline" эсвэл "attachment". inline нь
	// зөвхөн хөтөч дотор аюулгүй харуулах төрөлд (зураг/pdf/медиа) хүчинтэй;
	// html/svg/docx г.м. үргэлж attachment (эсвэл CSP sandbox).
	Disposition string
}

// SignedURL — SignURL-ийн үр дүн.
type SignedURL struct {
	URL       string // `<prefix>/private/...?exp=..&kid=..&sig=..` (origin-гүй)
	ExpiresAt time.Time
}

// SignedParams — URL-ийн query-ээс уншсан, шалгагдаагүй утгууд.
type SignedParams struct {
	Exp, KID, Sig, UserID, Disposition string
}

// Verified — амжилттай шалгасан холбоосын мэдээлэл.
type Verified struct {
	OrgID       string
	UserID      string // хоосон бол хэрэглэгчид холбоогүй
	Disposition string
	ExpiresAt   time.Time
}

// SignerOption — NewSigner-ийн сонголт.
type SignerOption func(*Signer)

// WithSignPrefix — URL-ийн угтвар (анхдагч "/files/private").
func WithSignPrefix(p string) SignerOption { return func(s *Signer) { s.prefix = p } }

// WithSignMaxTTL — ttl-ийн дээд хязгаар (анхдагч 1 цаг).
func WithSignMaxTTL(d time.Duration) SignerOption { return func(s *Signer) { s.maxTTL = d } }

// WithSignSkew — серверүүдийн цаг зөрөх зөвшөөрөл (анхдагч 30с, дээд 5мин).
func WithSignSkew(d time.Duration) SignerOption { return func(s *Signer) { s.skew = d } }

// WithSignClock — цаг (тест).
func WithSignClock(now func() time.Time) SignerOption { return func(s *Signer) { s.now = now } }

// Signer — хувийн файлын холбоосыг HMAC-SHA256-аар гарын үсэг зурна/шалгана.
// Түлхүүр эргүүлэлт auth.Keyring-тэй ижил: идэвхтэй түлхүүр зурна, өмнөх
// түлхүүрүүд зөвхөн шалгана. Үүсгэсний дараа өөрчлөгддөггүй ⇒ goroutine-д аюулгүй.
type Signer struct {
	active SignKey
	keys   map[string][]byte
	prefix string
	maxTTL time.Duration
	skew   time.Duration
	now    func() time.Time
}

// NewSigner — түлхүүр бүрийн secret ≥ config.MinJWTSecretLen байт, id нь
// `[A-Za-z0-9._-]{1,64}` ба давхцаагүй байх ёстой. Алдааны текстэд secret орохгүй.
func NewSigner(active SignKey, previous []SignKey, opts ...SignerOption) (*Signer, error) {
	s := &Signer{
		prefix: DefaultPrivatePrefix, maxTTL: DefaultSignMaxTTL, skew: DefaultSignSkew,
		now: time.Now, keys: make(map[string][]byte, 1+len(previous)),
	}
	for _, o := range opts {
		o(s)
	}
	all := append([]SignKey{active}, previous...)
	for _, k := range all {
		if !signKeyIDRe.MatchString(k.ID) {
			return nil, fmt.Errorf("upload: гарын үсгийн түлхүүрийн id %q буруу ([A-Za-z0-9._-], 1–64)", k.ID)
		}
		if len(k.Secret) < config.MinJWTSecretLen {
			return nil, fmt.Errorf("upload: %q түлхүүрийн secret хамгийн багадаа %d байт байх ёстой", k.ID, config.MinJWTSecretLen)
		}
		if _, dup := s.keys[k.ID]; dup {
			return nil, fmt.Errorf("upload: гарын үсгийн түлхүүрийн id %q давхцаж байна", k.ID)
		}
		s.keys[k.ID] = append([]byte(nil), k.Secret...)
	}
	s.active = SignKey{ID: active.ID, Secret: s.keys[active.ID]}
	p, err := normalizePrefix(s.prefix)
	if err != nil {
		return nil, err
	}
	s.prefix = p
	if s.maxTTL <= 0 {
		return nil, errors.New("upload: MaxTTL эерэг байх ёстой")
	}
	if s.skew < 0 || s.skew > maxSignSkew {
		return nil, fmt.Errorf("upload: цагийн зөрүү 0…%s байх ёстой", maxSignSkew)
	}
	if s.now == nil {
		s.now = time.Now
	}
	return s, nil
}

// Prefix — холбоосын URL угтвар (ж: "/files/private").
func (s *Signer) Prefix() string { return s.prefix }

// MaxTTL — ttl-ийн дээд хязгаар.
func (s *Signer) MaxTTL() time.Duration { return s.maxTTL }

// ActiveID — гарын үсэг зурдаг түлхүүрийн id.
func (s *Signer) ActiveID() string { return s.active.ID }

// splitPrivatePath — opaque зам `private/<org>/<cat>/<yyyy>/<mm>/<name>` бүтэцтэй
// эсэхийг (сегмент бүрийг) шалгаж org-ийг буцаана. Бусад бүх зам (`..`, нэмэлт
// сегмент, том үсэгтэй ангилал …) татгалзагдана.
func splitPrivatePath(p string) (org string, segs []string, ok bool) {
	if !ValidRelPath(p) || len(p) > 512 {
		return "", nil, false
	}
	segs = strings.Split(p, "/")
	if len(segs) != 6 || segs[0] != privateDir {
		return "", nil, false
	}
	if !orgSegRe.MatchString(segs[1]) || !catSegRe.MatchString(segs[2]) ||
		!yearSegRe.MatchString(segs[3]) || !monthSegRe.MatchString(segs[4]) ||
		!nameSegRe.MatchString(segs[5]) || strings.Contains(segs[5], "..") {
		return "", nil, false
	}
	return segs[1], segs, true
}

// PrivateOrg — opaque замаас org_id-г гаргана (бүтэц буруу бол ok=false).
func PrivateOrg(p string) (orgID string, ok bool) {
	org, _, ok := splitPrivatePath(p)
	return org, ok
}

func validDisposition(d string) bool {
	return d == "" || d == DispositionInline || d == DispositionAttachment
}

func validUserID(id string) bool {
	if len(id) > maxUserIDLen {
		return false
	}
	for _, r := range id {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// mac — HMAC-SHA256 (домэйн тусгаарлалт + урттай талбарууд ⇒ талбар хоорондын
// давхцал/холилт боломжгүй).
func mac(secret []byte, kid, path, org string, exp int64, uid, disp string) []byte {
	h := hmac.New(sha256.New, secret)
	var lb [binary.MaxVarintLen64]byte
	for _, f := range []string{signDomain, kid, path, org, strconv.FormatInt(exp, 10), uid, disp} {
		n := binary.PutUvarint(lb[:], uint64(len(f)))
		h.Write(lb[:n])
		h.Write([]byte(f))
	}
	return h.Sum(nil)
}

// Sign — path-д (opaque `private/<org>/...`) ttl хугацаатай холбоос үүсгэнэ.
// 0 < ttl ≤ MaxTTL; opts.Disposition allowlist-д байх ёстой.
func (s *Signer) Sign(p string, ttl time.Duration, opts SignOptions) (SignedURL, error) {
	org, segs, ok := splitPrivatePath(p)
	if !ok {
		return SignedURL{}, errors.New("upload: хувийн файлын зам буруу")
	}
	if ttl <= 0 {
		return SignedURL{}, errors.New("upload: ttl эерэг байх ёстой")
	}
	if ttl > s.maxTTL {
		return SignedURL{}, ErrTTLTooLong
	}
	if !validDisposition(opts.Disposition) {
		return SignedURL{}, errors.New("upload: disposition буруу")
	}
	if !validUserID(opts.UserID) {
		return SignedURL{}, errors.New("upload: user id буруу")
	}
	expAt := s.now().Add(ttl)
	if expAt.Nanosecond() != 0 {
		expAt = expAt.Truncate(time.Second).Add(time.Second) // дээш бөөрөнхийлж ttl-ээс богино болохгүй
	}
	exp := expAt.Unix()
	sig := mac(s.active.Secret, s.active.ID, p, org, exp, opts.UserID, opts.Disposition)

	q := url.Values{}
	q.Set("exp", strconv.FormatInt(exp, 10))
	q.Set("kid", s.active.ID)
	if opts.UserID != "" {
		q.Set("uid", opts.UserID)
	}
	if opts.Disposition != "" {
		q.Set("disp", opts.Disposition)
	}
	q.Set("sig", base64.RawURLEncoding.EncodeToString(sig))
	u := url.URL{Path: s.prefix + "/" + strings.Join(segs, "/")}
	return SignedURL{URL: u.EscapedPath() + "?" + q.Encode(), ExpiresAt: expAt}, nil
}

// SignURL — Sign-ийн богино хувилбар (зөвхөн URL).
func (s *Signer) SignURL(p string, ttl time.Duration, opts SignOptions) (string, error) {
	r, err := s.Sign(p, ttl, opts)
	return r.URL, err
}

// Verify — холбоосын гарын үсэг, kid, хугацаа, disposition-ийг шалгана. Алдаа
// ErrBadSignature (бүх буруу талбар) эсвэл ErrExpired (гарын үсэг ЗӨВ, хугацаа
// дууссан). Хэрэглэгчид холбосон эсэхийг Verified.UserID-аар дуудагч шалгана.
// Гарын үсгийг тогтмол хугацаатай (hmac.Equal) харьцуулна.
func (s *Signer) Verify(p string, sp SignedParams) (Verified, error) {
	org, _, ok := splitPrivatePath(p)
	if !ok || !validDisposition(sp.Disposition) || !validUserID(sp.UserID) {
		return Verified{}, ErrBadSignature
	}
	secret, ok := s.keys[sp.KID]
	if !ok || sp.Exp == "" || len(sp.Exp) > 19 {
		return Verified{}, ErrBadSignature
	}
	exp, err := strconv.ParseInt(sp.Exp, 10, 64)
	if err != nil || exp <= 0 || strconv.FormatInt(exp, 10) != sp.Exp {
		return Verified{}, ErrBadSignature
	}
	got, err := base64.RawURLEncoding.Strict().DecodeString(sp.Sig)
	if err != nil || len(got) != sha256.Size {
		return Verified{}, ErrBadSignature
	}
	want := mac(secret, sp.KID, p, org, exp, sp.UserID, sp.Disposition)
	if !hmac.Equal(got, want) {
		return Verified{}, ErrBadSignature
	}
	now := s.now().Unix()
	if now > exp+int64(s.skew/time.Second) {
		return Verified{}, ErrExpired
	}
	// MaxTTL хойно багасгасан эсвэл түлхүүр алдагдаж урт хугацаатай холбоос
	// гаргасан бол хүлээн зөвшөөрөхгүй.
	if exp > now+int64((s.maxTTL+s.skew)/time.Second)+1 {
		return Verified{}, ErrBadSignature
	}
	return Verified{OrgID: org, UserID: sp.UserID, Disposition: sp.Disposition, ExpiresAt: time.Unix(exp, 0)}, nil
}
