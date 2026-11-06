// Package cache нь org-тусгаарлагдсан, generation-д суурилсан хариуны cache юм: давтан уншилтыг
// DB-гүйгээр хариулж, бичилтийн дараа хуучирсан өгөгдөл ХЭЗЭЭ Ч харуулахгүй. Гэрээ: docs/CACHE.md.
//
//	c, _ := cache.New(cache.Config{})                      // L1 (in-process) + in-process gens
//	body, hit, err := c.Remember(ctx, cache.Key{OrgID: org, Resource: "finance.invoice", Parts: []string{"list", q}},
//	    0, func(ctx context.Context) ([]byte, error) { return loadJSON(ctx) })
//	_ = c.Bump(ctx, org, "finance.invoice")                // бичилт commit болсны ДАРАА
package cache

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"
)

// Анхдагч утгууд.
const (
	DefaultTTL           = 5 * time.Minute
	DefaultMaxEntryBytes = 1 << 20
	DefaultPrefix        = "khan"
)

var prefixRe = regexp.MustCompile(`^[a-z0-9_.-]+$`)

// Store — L2 (ж: Redis) эсвэл L1 хадгалалт. Алдаа нь cache-ийг алгасах (fail-open) шалтгаан болно.
type Store interface {
	Get(ctx context.Context, key string) (val []byte, ok bool, err error)
	Set(ctx context.Context, key string, val []byte, ttl time.Duration) error
}

// GenStore — generation тоолуур. Олон instance-д ХУВААЛЦАГДАХ ёстой (Redis), эс бөгөөс instance-ууд
// бие биеийн бичилтийг харахгүй.
type GenStore interface {
	Gen(ctx context.Context, keys []string) ([]uint64, error) // байхгүй бол 0
	Bump(ctx context.Context, keys []string) error            // түлхүүр бүрийг атомаар +1
}

// Config — Cache-ийн тохиргоо.
type Config struct {
	L1   *Memory  // nil → NewMemory(MemoryConfig{Now: Now}) (10k entry, 64 MiB)
	L2   Store    // optional (Redis); nil → зөвхөн L1
	Gens GenStore // nil → L2 нь GenStore бол тэр, эс бөгөөс (L2 байхгүй) in-process MemoryGens
	// DefaultTTL — Remember-д ttl<=0 үед. 0 → 5 мин.
	DefaultTTL time.Duration
	// MaxEntryBytes — үүнээс том хариуг cache-д хийхгүй. 0 → 1 MiB.
	MaxEntryBytes int
	Prefix        string // анхдагч "khan"; `[a-z0-9_.-]+`
	// Logf — nil бол чимээгүй. Store/gen алдаа энд гарна.
	Logf func(format string, args ...any)
	// Now — цаг (тест); nil → time.Now.
	Now func() time.Time
	// Salt — build/deploy-ийн тэмдэг (ж: git commit). Version/түлхүүр/ETag-д орно: хариуны хэлбэр
	// өөрчлөгдсөн шинэ deploy хуучин cache, ETag-ийг ашиглахгүй. Process доторх generation
	// (MemoryGens) үед процесс бүрийн санамсаргүй boot nonce автоматаар нэмэгдэнэ — restart-ийн
	// дараа generation 0-ээс эхлэхэд хуучин ETag-аар 304 өгөхөөс сэргийлнэ.
	Salt string
	// AllowLocalGens — L2 (хуваалцсан store)-ийг process доторх MemoryGens-тэй хослуулахыг зөвшөөрнө.
	// Анхдагч false: instance бүр өөр generation-тэй тул хуваалцсан L2-оос бусдын хуучин entry-г
	// уншиж болно. Зөвхөн нэг instance-тэй орчинд (тест) true.
	AllowLocalGens bool
}

// Stats — тоолуурууд (атомаар цуглуулсан snapshot).
type Stats struct {
	Hits      uint64 // cache-ээс (L1/L2) хариулсан
	L2Hits    uint64 // үүнээс L2-оос
	Misses    uint64 // fn дуудагдсан (эсвэл өөр goroutine-ийн fn-ийн үр дүнг хуваалцсан)
	Errors    uint64 // store/gen алдаа (fail-open хийсэн)
	Sets      uint64 // хадгалсан
	TooLarge  uint64 // MaxEntryBytes-ээс том тул хадгалаагүй
	Coalesced uint64 // singleflight-ээр өөр дуудлагын үр дүнг хүлээж авсан
	Bumps     uint64 // амжилттай Bump дуудлага
}

// Cache — org-scoped, generation-based cache. Concurrent-safe.
type Cache struct {
	l1       *Memory
	l2       Store
	gens     GenStore
	ttl      time.Duration
	maxEntry int
	prefix   string
	logfn    func(string, ...any)
	now      func() time.Time
	sf       singleflight.Group
	salt     string // "s<8hex>." эсвэл "" (Version-ийн угтвар)

	hits, l2hits, misses, errs, sets, tooLarge, coalesced, bumps atomic.Uint64
}

// New — Cache үүсгэнэ. L2 өгөгдсөн боловч хуваалцсан GenStore байхгүй (Gens nil ба L2 нь GenStore биш)
// бол алдаа: процесс бүрийн тусдаа generation хуваалцсан L2-той хамт хуучирсан өгөгдөл өгнө.
func New(cfg Config) (*Cache, error) {
	c := &Cache{
		l1: cfg.L1, l2: cfg.L2, gens: cfg.Gens, ttl: cfg.DefaultTTL, maxEntry: cfg.MaxEntryBytes,
		prefix: cfg.Prefix, logfn: cfg.Logf, now: cfg.Now,
	}
	if c.now == nil {
		c.now = time.Now
	}
	if c.ttl <= 0 {
		c.ttl = DefaultTTL
	}
	if c.maxEntry <= 0 {
		c.maxEntry = DefaultMaxEntryBytes
	}
	if c.prefix == "" {
		c.prefix = DefaultPrefix
	}
	if !prefixRe.MatchString(c.prefix) {
		return nil, fmt.Errorf("khan/cache: Prefix буруу: %q (`[a-z0-9_.-]+`)", c.prefix)
	}
	if c.l1 == nil {
		c.l1 = NewMemory(MemoryConfig{Now: c.now})
	}
	if c.gens == nil {
		if gs, ok := c.l2.(GenStore); ok {
			c.gens = gs
		} else if c.l2 != nil {
			return nil, errors.New("khan/cache: L2 өгсөн бол хуваалцсан GenStore (ж: rediscache) заавал")
		} else {
			c.gens = NewMemoryGens()
		}
	}
	_, localGens := c.gens.(*MemoryGens)
	if localGens && c.l2 != nil && !cfg.AllowLocalGens {
		return nil, errors.New("khan/cache: хуваалцсан L2-ийг process доторх MemoryGens-тэй хослуулж болохгүй (AllowLocalGens)")
	}
	salt := cfg.Salt
	if localGens {
		var nonce [8]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return nil, fmt.Errorf("khan/cache: boot nonce: %w", err)
		}
		salt += ":" + hex.EncodeToString(nonce[:])
	}
	if salt != "" {
		sum := sha256.Sum256([]byte(salt))
		c.salt = "s" + hex.EncodeToString(sum[:4]) + "."
	}
	return c, nil
}

func (c *Cache) logf(format string, args ...any) {
	if c.logfn != nil {
		c.logfn(format, args...)
	}
}

// Stats — тоолуурын snapshot.
func (c *Cache) Stats() Stats {
	return Stats{
		Hits: c.hits.Load(), L2Hits: c.l2hits.Load(), Misses: c.misses.Load(), Errors: c.errs.Load(),
		Sets: c.sets.Load(), TooLarge: c.tooLarge.Load(), Coalesced: c.coalesced.Load(), Bumps: c.bumps.Load(),
	}
}

// DefaultTTL — ttl<=0 үед хэрэглэгдэх хугацаа.
func (c *Cache) DefaultTTL() time.Duration { return c.ttl }

func (c *Cache) genKey(org, resource string) string {
	return c.prefix + ":gen:" + escape(org) + ":" + escape(resource)
}

func (c *Cache) genKeys(org string, resources []string) []string {
	keys := make([]string, len(resources))
	for i, r := range resources {
		keys[i] = c.genKey(org, r)
	}
	return keys
}

// Bump — (org, resource) бүрийн generation-ийг +1: хуучин түлхүүрүүд хэзээ ч дахин уншигдахгүй.
// Write транзакц commit болсны ДАРАА дууд. Алдаа буцвал TTL нь хуучирлын дээд хязгаар болно.
func (c *Cache) Bump(ctx context.Context, orgID string, resources ...string) error {
	if len(resources) == 0 {
		return nil
	}
	if orgID == "" {
		return ErrInvalidKey
	}
	seen := make(map[string]bool, len(resources))
	uniq := make([]string, 0, len(resources))
	for _, r := range resources {
		if r == "" {
			return ErrInvalidKey
		}
		if !seen[r] {
			seen[r] = true
			uniq = append(uniq, r)
		}
	}
	if err := c.gens.Bump(ctx, c.genKeys(orgID, uniq)); err != nil {
		c.errs.Add(1)
		return fmt.Errorf("khan/cache: bump: %w", err)
	}
	c.bumps.Add(1)
	return nil
}

// Version — resource-уудын generation: "g12.g3" (өгсөн дарааллаар). ETag/түлхүүрт. Уншиж чадахгүй бол алдаа.
func (c *Cache) Version(ctx context.Context, orgID string, resources ...string) (string, error) {
	if orgID == "" || len(resources) == 0 {
		return "", ErrInvalidKey
	}
	for _, r := range resources {
		if r == "" {
			return "", ErrInvalidKey
		}
	}
	gens, err := c.gens.Gen(ctx, c.genKeys(orgID, resources))
	if err != nil {
		c.errs.Add(1)
		return "", fmt.Errorf("khan/cache: gen: %w", err)
	}
	if len(gens) != len(resources) {
		c.errs.Add(1)
		return "", fmt.Errorf("khan/cache: gen: %d утга хүлээсэн, %d ирсэн", len(resources), len(gens))
	}
	return c.salt + versionString(gens), nil
}

// Epoch — цаг ttl-ийн дарааллаар хуваагдсан дугаар. ETag-д нэмбэл `If-None-Match`-ийн 304 нь
// generation өөрчлөгдөөгүй ч хамгийн ихдээ ~ttl-ээс удаан хүчинтэй байхгүй (crud-ийн гадуурх,
// bump хийгдээгүй өөрчлөлт TTL-ээр хязгаарлагдах гэрээг 304-д ч биелүүлнэ). ttl<=0 → DefaultTTL.
func (c *Cache) Epoch(ttl time.Duration) string {
	if ttl <= 0 {
		ttl = c.ttl
	}
	return strconv.FormatInt(c.now().UnixNano()/int64(ttl), 10)
}

// Resolved — generation уншигдсан Key: бүрэн түлхүүр + Version. Resolve-оос л үүснэ.
type Resolved struct {
	Version string // ETag-д
	key     string
}

// Resolve — Key-ийн generation-уудыг НЭГ удаа уншиж бүрэн түлхүүр болгоно. Алдаа (буруу Key эсвэл
// generation уншигдсангүй) бол дуудагч cache-ийг алгасах ёстой (crud: X-Khan-Cache: bypass).
func (c *Cache) Resolve(ctx context.Context, k Key) (Resolved, error) {
	res, err := k.resources()
	if err != nil {
		return Resolved{}, err
	}
	ver, err := c.Version(ctx, k.OrgID, res...)
	if err != nil {
		return Resolved{}, err
	}
	return Resolved{
		Version: ver,
		key:     c.prefix + ":v1:" + escape(k.OrgID) + ":" + escape(k.Resource) + ":" + ver + ":" + k.hash(res),
	}, nil
}

// Remember — түлхүүрээр cache-ээс уншина; байхгүй бол fn-ийг дуудаж хадгална. Ижил түлхүүрийг зэрэг
// асуувал singleflight-ээр fn НЭГ л удаа ажиллана.
//
// Fail-open: store/generation алдаа, буруу Key зэрэг cache-ийн ямар ч алдаа fn-ийг дуудан хариу өгөхөд
// саад болохгүй (логлоно). Тиймээс буцсан err нь ЗӨВХӨН fn-ийн алдаа. fn-ийн алдаа хэзээ ч cache-д орохгүй.
// Хэмжээ нь MaxEntryBytes-ээс их хариу хадгалагдахгүй. Буцсан слайсыг бүү өөрчил (хуваалцсан).
// hit=true бол fn дуудагдаагүй.
func (c *Cache) Remember(ctx context.Context, k Key, ttl time.Duration, fn func(ctx context.Context) ([]byte, error)) ([]byte, bool, error) {
	r, err := c.Resolve(ctx, k)
	if err != nil {
		c.logf("cache: Resolve алдаа, cache алгасав: %v", err)
		c.misses.Add(1)
		v, ferr := fn(ctx)
		return v, false, ferr
	}
	return c.RememberResolved(ctx, r, ttl, fn)
}

type flight struct {
	val []byte
	hit bool
}

// RememberResolved — Remember, гэхдээ generation-ийг Resolve-оор аль хэдийн уншсан (ETag/304-тэй
// нэг дор ашиглахад generation-ийг хоёр удаа уншихгүй). r нь тэг утга байж БОЛОХГҮЙ.
func (c *Cache) RememberResolved(ctx context.Context, r Resolved, ttl time.Duration, fn func(ctx context.Context) ([]byte, error)) ([]byte, bool, error) {
	if r.key == "" {
		c.misses.Add(1)
		v, err := fn(ctx)
		return v, false, err
	}
	if ttl <= 0 {
		ttl = c.ttl
	}
	if v, ok := c.lookup(ctx, r.key, ttl); ok {
		c.hits.Add(1)
		return v, true, nil
	}
	led := false
	res, err, shared := c.sf.Do(r.key, func() (any, error) {
		led = true
		return c.fill(ctx, r.key, ttl, fn)
	})
	if err != nil {
		// Удирдагчийн ctx цуцлагдсан/хугацаа дууссан боловч манайх хэвийн бол өөрөө (дахин нэгтгэгдэн) оролдоно.
		if shared && !led && ctx.Err() == nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
			res, err, _ = c.sf.Do(r.key+"\x00retry", func() (any, error) { return c.fill(ctx, r.key, ttl, fn) })
		}
		if err != nil {
			return nil, false, err
		}
	} else if shared && !led {
		c.coalesced.Add(1)
	}
	f := res.(flight)
	if f.hit {
		c.hits.Add(1)
	} else {
		c.misses.Add(1)
	}
	return f.val, f.hit, nil
}

// fill — singleflight доторх ажил: дахин шалгаад (өөр flight дөнгөж дууссан байж болно) fn дуудаж хадгална.
func (c *Cache) fill(ctx context.Context, key string, ttl time.Duration, fn func(context.Context) ([]byte, error)) (any, error) {
	if v, ok := c.lookup(ctx, key, ttl); ok {
		return flight{val: v, hit: true}, nil
	}
	v, err := fn(ctx)
	if err != nil {
		return nil, err
	}
	c.store(ctx, key, v, ttl)
	return flight{val: v}, nil
}

// lookup — L1, дараа нь L2 (олдвол L1-д дэвшүүлнэ). L2 алдаа = олдсонгүй.
func (c *Cache) lookup(ctx context.Context, key string, ttl time.Duration) ([]byte, bool) {
	if v, ok, _ := c.l1.Get(ctx, key); ok {
		return v, true
	}
	if c.l2 == nil {
		return nil, false
	}
	v, ok, err := c.l2.Get(ctx, key)
	if err != nil {
		c.errs.Add(1)
		c.logf("cache: L2 Get алдаа: %v", err)
		return nil, false
	}
	if !ok {
		return nil, false
	}
	_ = c.l1.Set(ctx, key, v, ttl)
	c.l2hits.Add(1)
	return v, true
}

func (c *Cache) store(ctx context.Context, key string, v []byte, ttl time.Duration) {
	if len(v) > c.maxEntry {
		c.tooLarge.Add(1)
		return
	}
	_ = c.l1.Set(ctx, key, v, ttl)
	if c.l2 != nil {
		if err := c.l2.Set(ctx, key, v, ttl); err != nil {
			c.errs.Add(1)
			c.logf("cache: L2 Set алдаа: %v", err)
			return
		}
	}
	c.sets.Add(1)
}
