package crud

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gofiber/fiber/v3"
	"github.com/khan-vision/khan/cache"
	"github.com/khan-vision/khan/obs"
	"github.com/khan-vision/khan/query"
)

// CacheSpec — resource-ийн унших endpoint-уудын cache тохиргоо. Гэрээ: docs/CACHE.md §4.
//
// Cache-д орох хариу нь ЗӨВХӨН org + хүсэлт (+ `can`, хариунд хүсэлт бүрт тусад нь нэмэгдэнэ)-ээс
// хамаарах ёстой. ListQuery/DetailQuery хэрэглэгчээс хамаарсан өгөгдөл буцаадаг бол (ж: «зөвхөн
// миний мөр») энэ resource-д Cache тавьж БОЛОХГҮЙ — хэрэглэгч хоорондын алдагдал үүснэ.
type CacheSpec struct {
	Cache *cache.Cache
	TTL   time.Duration // 0 → Cache.DefaultTTL
	// DependsOn — жагсаалт/detail/options-д JOIN (эсвэл ref) хийгддэг өөр resource-ийн Name.
	// Тэдгээрийн бичилт (Bumps эсвэл cache.Bump-аар) энэ cache-ийг хүчингүй болгоно.
	DependsOn []string
	// Bumps — энэ resource-ийн бичилт өөр resource-ийн cache-ийг мөн хүчингүй болгоно
	// (Hook дотор өөр хүснэгт бичдэг бол).
	Bumps []string
	// NoETag — ETag/304-г унтраана (body cache л үлдэнэ).
	NoETag bool
	// BumpOnly — уншилтыг cache хийхгүй, ЗӨВХӨН бичилтийн дараа generation нэмнэ. Өөр resource
	// (`DependsOn`-оор) энэ resource-оос хамаардаг боловч энэ resource өөрөө cache хийх шаардлагагүй
	// үед заавал: эс бөгөөс энэ resource-ийн бичилт тэдгээрийн cache-ийг хүчингүй болгохгүй.
	BumpOnly bool
}

// cachePlan — compile хийгдсэн, өөрчлөгдөхгүй cache тохиргоо.
type cachePlan struct {
	bumpOnly  bool
	c         *cache.Cache
	ttl       time.Duration
	dependsOn []string
	bumpNames []string // Name + Bumps (давхцалгүй)
	noETag    bool
}

const hdrCache = "X-Khan-Cache"

// compileCache — CacheSpec-ийг шалгаж cachePlan болгоно (nil → nil).
func compileCache(name string, s *CacheSpec) (*cachePlan, error) {
	if s == nil {
		return nil, nil
	}
	if s.Cache == nil {
		return nil, errors.New("Cache.Cache nil")
	}
	if s.TTL < 0 {
		return nil, errors.New("Cache.TTL сөрөг байж болохгүй")
	}
	uniq := func(field string, in []string, self bool) ([]string, error) {
		seen := map[string]bool{}
		if self {
			seen[name] = true
		}
		var out []string
		for _, n := range in {
			if !nameRe.MatchString(n) {
				return nil, fmt.Errorf("Cache.%s: нэр буруу: %q", field, n)
			}
			if !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
		}
		sort.Strings(out)
		return out, nil
	}
	deps, err := uniq("DependsOn", s.DependsOn, true)
	if err != nil {
		return nil, err
	}
	bumps, err := uniq("Bumps", s.Bumps, true)
	if err != nil {
		return nil, err
	}
	return &cachePlan{
		c: s.Cache, ttl: s.TTL, dependsOn: deps, bumpNames: append([]string{name}, bumps...), noETag: s.NoETag, bumpOnly: s.BumpOnly,
	}, nil
}

// bumpCache — write транзакц амжилттай COMMIT болсны ДАРАА дуудагдана (commit-оос өмнө bump хийвэл
// зэрэгцээ уншигч хуучин өгөгдлийг шинэ generation-д хадгалж болно). Handler-ийн timeout дууссан ч
// bump хийгдэхийн тулд цуцлалтаас салгаж өөрийн богино хугацаатай ажиллана. Алдаа → лог (TTL хязгаарлана).
//
// ScopeGlobal: generation нь тогтмол `_global` namespace-д. ScopeShared-ийн системийн мөрийн бичилт нь
// өөрийн org-ийн generation-д нэмээд `_system` namespace-ийг bump хийнэ — Shared resource бүрийн
// унших түлхүүр/ETag энэ generation-ийг агуулдаг тул бүх org-ийн cache хүчингүй болно.
func (e *engine) bumpCache(c fiber.Ctx, o owner) {
	if e.cache == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Context()), 5*time.Second)
	defer cancel()
	orgs := []string{e.cacheOrg(o.org)}
	if o.system {
		orgs = append(orgs, systemCacheOrg)
	}
	for _, org := range orgs {
		if err := e.cache.c.Bump(ctx, org, e.cache.bumpNames...); err != nil {
			obs.With(c).Error("crud: cache bump амжилтгүй (хуучирсан өгөгдөл TTL хүртэл үлдэж болно)", "resource", e.name, "err", err)
		}
	}
}

// systemPart — ScopeShared resource-ийн cache түлхүүрийн нэмэлт хэсэг: `_system` namespace-ийн
// generation (resource + DependsOn). Системийн мөр өөрчлөгдөхөд бүх org-ийн түлхүүр өөрчлөгдөнө.
// Tenant/Global-д хоосон (түлхүүр өөрчлөгдөхгүй).
func (e *engine) systemPart(ctx context.Context) (string, error) {
	if e.scope != ScopeShared {
		return "", nil
	}
	names := append([]string{e.name}, e.cache.dependsOn...)
	v, err := e.cache.c.Version(ctx, systemCacheOrg, names...)
	if err != nil {
		return "", err
	}
	return "sys=" + v, nil
}

// relayErr — аль хэдийн бичигдсэн (200 биш) хариуг cache-д хийхгүйгээр дамжуулах алдаа
// (options: lookup handler өөрөө хариугаа бичдэг).
type relayErr struct {
	status int
	ctype  string
	body   []byte
}

func (r *relayErr) Error() string { return "khan/crud: relay " + strconv.Itoa(r.status) }

func writeRelay(c fiber.Ctx, r *relayErr) error {
	c.Status(r.status)
	c.Set(fiber.HeaderContentType, r.ctype)
	c.Response().SetBodyRaw(bytes.Clone(r.body))
	return nil
}

// canBits — `can` map-ийн хэв (read,create,update,delete). ETag-д орно: эрх өөрчлөгдвөл ETag өөрчлөгдөнө.
func canBits(can map[string]bool) string {
	b := make([]byte, 0, 4)
	for _, k := range [...]string{"read", "create", "update", "delete"} {
		if can[k] {
			b = append(b, '1')
		} else {
			b = append(b, '0')
		}
	}
	return string(b)
}

// canonFilters — filters JSON-ыг тогтвортой хэлбэрт (түлхүүр эрэмбэлсэн, зайгүй, тоо хэвээр) оруулна.
// Задлагдахгүй бол түүхий мөр (query 400 өгнө). Хоосон/`{}` → "".
func canonFilters(raw string) string {
	t := strings.TrimSpace(raw)
	if t == "" || t == "{}" {
		return ""
	}
	dec := json.NewDecoder(strings.NewReader(t))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil {
		return raw
	}
	if _, err := dec.Token(); err != io.EOF { // араас нь илүү өгөгдөл
		return raw
	}
	out, err := json.Marshal(v)
	if err != nil || string(out) == "{}" {
		if err == nil {
			return ""
		}
		return raw
	}
	return string(out)
}

// maxCachedSearch / maxCachedPage — үүнээс урт хайлт, хол хуудсыг cache хийхгүй (DB-ээс шууд):
// эрхтэй хэрэглэгч хязгааргүй олон түлхүүр үүсгэж LRU-г шахахаас сэргийлнэ.
const (
	maxCachedSearch = 200
	maxCachedPage   = 1000
)

// listParts — жагсаалтын cache түлхүүрийн хэсгүүд. query-ийн ӨӨРИЙН хэвшүүлэлтээр (Spec.Normalized):
// limit=99999 ба limit=500 гэх мэт ижил үр дүнтэй хүсэлт нэг түлхүүртэй, өөр үр дүнтэй нь ялгаатай.
// ok=false → энэ хүсэлтийг cache хийхгүй.
func (e *engine) listParts(req query.Request) ([]string, bool) {
	page, limit, search, order := e.listSpec.Normalized(req)
	if utf8.RuneCountInString(search) > maxCachedSearch || page > maxCachedPage {
		return nil, false
	}
	return []string{"list", strconv.Itoa(page), strconv.Itoa(limit), search, order, canonFilters(req.Filters)}, true
}

// cacheReads — уншилтыг cache хийх эсэх (BumpOnly үед үгүй).
func (e *engine) cacheReads() bool { return e.cache != nil && !e.cache.bumpOnly }

// ambiguousWrite — write алдааны үед транзакц commit болсон эсэх ТОДОРХОЙГҮЙ эсэх. Зөвхөн
// COMMIT-ийн алдаа (*commitError) тодорхойгүй: fn доторх ямар ч алдаа (validation, hook, statement,
// context timeout) COMMIT-оос өмнө тул rollback баталгаатай. Тодорхойгүй үед bump хийнэ — илүү bump
// хор хөнөөлгүй, commit болсон өөрчлөлтийг bump-гүй орхивол TTL хүртэл хуучирна.
func ambiguousWrite(err error) bool {
	var ce *commitError
	return errors.As(err, &ce)
}

// serveCached — унших endpoint-ийн нийтлэг cache урсгал (read эрх аль хэдийн шалгагдсан байх ёстой).
//
//	Resolve → (ETag таарвал 304) → Remember(compute) → толгойнууд + body.
//
// compute нь 4xx/5xx-ийг алдаа болгож буцаана (cache-д ХЭЗЭЭ Ч орохгүй); буцсан алдааг дуудагч хариу болгоно.
// decorate (nil байж болно) нь cache-д орсон body-д хүсэлт бүрт өөрчлөгддөг хэсгийг (`can`) нэмнэ.
func (e *engine) serveCached(c fiber.Ctx, orgID string, parts []string, can map[string]bool,
	compute func(ctx context.Context) ([]byte, error), decorate func(body []byte) ([]byte, error)) error {
	cp := e.cache
	ctx := c.Context()
	orgID = e.cacheOrg(orgID) // Global: бүх org-д хуваалцсан namespace
	sys, err := e.systemPart(ctx)
	var res cache.Resolved
	if err == nil {
		if sys != "" {
			parts = append(parts[:len(parts):len(parts)], sys)
		}
		res, err = cp.c.Resolve(ctx, cache.Key{OrgID: orgID, Resource: e.name, DependsOn: cp.dependsOn, Parts: parts})
	}
	if err != nil {
		// Generation уншигдсангүй → cache-гүй (fail-open), ETag тавихгүй.
		obs.With(c).Warn("crud: cache алгасав", "resource", e.name, "err", err)
		body, err := compute(ctx)
		if err != nil {
			return err
		}
		e.validators(c, "")
		c.Set(hdrCache, "bypass")
		return e.sendCached(c, body, decorate)
	}
	etag := ""
	if !cp.noETag {
		// ETag-д org, resource, эрхийн хэв, TTL epoch орно: өөр org/эрх/хугацаанд ижил ETag гарахгүй.
		etag = cache.ETag(res.Version, append([]string{orgID, e.name, canBits(can), cp.c.Epoch(cp.ttl)}, parts...)...)
		// 304 нь зөвхөн GET/HEAD-д (RFC 9110: бусад method-д If-None-Match таарвал 412, 304 биш).
		if m := c.Method(); (m == fiber.MethodGet || m == fiber.MethodHead) && cache.ETagMatch(c.Get(fiber.HeaderIfNoneMatch), etag) {
			e.validators(c, etag)
			c.Set(hdrCache, "hit")
			c.Status(fiber.StatusNotModified)
			c.Response().ResetBody()
			return nil
		}
	}
	body, hit, err := cp.c.RememberResolved(ctx, res, cp.ttl, compute)
	if err != nil {
		return err
	}
	e.validators(c, etag)
	if hit {
		c.Set(hdrCache, "hit")
	} else {
		c.Set(hdrCache, "miss")
	}
	return e.sendCached(c, body, decorate)
}

// validators — ETag (байвал) + Cache-Control + Vary. `private, no-cache`: browser хадгална, гэхдээ ҮРГЭЛЖ revalidate.
func (e *engine) validators(c fiber.Ctx, etag string) {
	if etag != "" {
		c.Set(fiber.HeaderETag, etag)
	}
	c.Set(fiber.HeaderCacheControl, "private, no-cache")
	c.Vary(fiber.HeaderAuthorization, fiber.HeaderCookie)
}

// sendCached — body (cache-ийн хуваалцсан слайс) → хариу. Cache-ийн санах ойг хариутай хуваалцахгүйн
// тулд хуулна (дунд нь middleware байт засвал cache эвдэрдэггүй).
func (e *engine) sendCached(c fiber.Ctx, body []byte, decorate func([]byte) ([]byte, error)) error {
	var err error
	if decorate != nil {
		body, err = decorate(body)
	} else {
		body = bytes.Clone(body)
	}
	if err != nil {
		return err
	}
	c.Status(fiber.StatusOK)
	c.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSONCharsetUTF8)
	c.Response().SetBodyRaw(body)
	return nil
}

// withCan — жагсаалтын cache-д орсон `{...}` body-ийн эхэнд `"can"` нэмнэ. `can` нь бусад түлхүүрээс
// (data,filterable,limit,page,total,totals) цагаан толгойн дарааллаар өмнө тул encoding/json-ийн
// эрэмбэлсэн map гаралттай байт-байтаар ижил.
func (e *engine) withCan(c fiber.Ctx, can map[string]bool) func([]byte) ([]byte, error) {
	return func(body []byte) ([]byte, error) {
		cj, err := c.App().Config().JSONEncoder(can)
		if err != nil {
			return nil, err
		}
		if len(body) < 2 || body[0] != '{' || body[len(body)-1] != '}' {
			return nil, errors.New("khan/crud: cache body объект биш")
		}
		out := make([]byte, 0, len(body)+len(cj)+10)
		out = append(out, `{"can":`...)
		out = append(out, cj...)
		if len(body) > 2 {
			out = append(out, ',')
		}
		return append(out, body[1:]...), nil
	}
}
