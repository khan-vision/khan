package access

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"
)

// Generations — олон instance-ийн cache invalidation-д зориулсан хуваалцсан тоолуур.
// `khan/cache.GenStore` (cache.MemoryGens, rediscache) ЯГ энэ интерфейсийг хангана —
// access нь cache багцаас хамааралгүй, Redis-ийг заавал шаардахгүй.
//
// Гэрээ: Gen байхгүй түлхүүрт 0 буцаана; Bump түлхүүр бүрийг атомаар +1 хийнэ.
type Generations interface {
	Gen(ctx context.Context, keys []string) ([]uint64, error)
	Bump(ctx context.Context, keys []string) error
}

// loader — TTL + singleflight + invalidation-тэй, төрлөөр параметржсэн санах ойн cache.
// Concurrent-safe. Дараах нарийн зүйлсийг зөв барина:
//
//   - invalidation ба ачаалалтын уралдаан: ачаалж байх хооронд invalidate болсон бол үр дүнг
//     cache-д хийхгүй (хуучин утга буцаж орохгүй), мөн шинэ дуудагч хуучин flight-д нэгдэхгүй
//     (singleflight түлхүүрт epoch орно).
//   - дуудагчийн ctx цуцлагдсан ч ачаалалт бусдад зориулж үргэлжилнэ (WithoutCancel + timeout).
//   - Source panic → алдаа (DoChan-ийн goroutine процессыг унагаахгүй).
type loader[V any] struct {
	now         func() time.Time
	ttl         time.Duration
	errTTL      time.Duration
	max         int
	timeout     time.Duration
	gens        Generations
	genInterval time.Duration

	mu    sync.Mutex
	items map[string]*item[V]
	sf    singleflight.Group
	epoch atomic.Uint64
}

type item[V any] struct {
	org       string
	val       V
	err       error
	expires   time.Time
	gens      []uint64
	genKeys   []string
	checkedAt time.Time
}

type loaded[V any] struct {
	val V
	err error
}

func newLoader[V any](cfg *options) *loader[V] {
	return &loader[V]{
		now: cfg.now, ttl: cfg.ttl, errTTL: cfg.errTTL, max: cfg.maxEntries,
		timeout: cfg.loadTimeout, gens: cfg.gens, genInterval: cfg.genInterval,
		items: map[string]*item[V]{},
	}
}

func sliceEq(a, b []uint64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// get — cache-ээс, эсвэл (singleflight-аар) load-оос. genKeys нь хуваалцсан generation-ийн
// түлхүүрүүд (gens тохируулаагүй бол үл тооно).
func (l *loader[V]) get(ctx context.Context, org, key string, genKeys []string, load func(context.Context) (V, error)) (V, error) {
	now := l.now()
	l.mu.Lock()
	it := l.items[key]
	var hit item[V]
	if it != nil && now.Before(it.expires) {
		hit = *it
	} else {
		it = nil
	}
	l.mu.Unlock()

	if it != nil {
		if l.gens == nil || len(hit.gens) == 0 || now.Sub(hit.checkedAt) < l.genInterval {
			return hit.val, hit.err
		}
		cur, err := l.gens.Gen(ctx, hit.genKeys)
		if err == nil && sliceEq(cur, hit.gens) {
			l.mu.Lock()
			if p := l.items[key]; p == it {
				p.checkedAt = now
			}
			l.mu.Unlock()
			return hit.val, hit.err
		}
		// generation өөрчлөгдсөн (эсвэл уншиж чадсангүй) → дахин ачаална.
	}

	ep := l.epoch.Load()
	ch := l.sf.DoChan(key+"\x01"+strconv.FormatUint(ep, 10), func() (r any, _ error) {
		lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), l.timeout)
		defer cancel()
		cacheable := true
		var gens []uint64
		if l.gens != nil && len(genKeys) > 0 {
			// generation-ийг ачаалахаас ӨМНӨ уншина: хойно bump хийгдвэл дараагийн шалгалт зөрнө.
			g, err := l.gens.Gen(lctx, genKeys)
			if err != nil || len(g) != len(genKeys) {
				cacheable = false
			}
			gens = g
		}
		var res loaded[V]
		func() {
			defer func() {
				if rec := recover(); rec != nil {
					res = loaded[V]{err: fmt.Errorf("access: source panic: %v", rec)}
				}
			}()
			res.val, res.err = load(lctx)
		}()
		if cacheable && l.epoch.Load() == ep {
			l.store(org, key, genKeys, gens, res)
		}
		return res, nil
	})
	select {
	case r := <-ch:
		res := r.Val.(loaded[V])
		return res.val, res.err
	case <-ctx.Done():
		var zero V
		return zero, ctx.Err()
	}
}

func (l *loader[V]) store(org, key string, genKeys []string, gens []uint64, r loaded[V]) {
	ttl := l.ttl
	if r.err != nil {
		ttl = l.errTTL
	}
	if ttl <= 0 {
		return
	}
	now := l.now()
	it := &item[V]{org: org, val: r.val, err: r.err, expires: now.Add(ttl), checkedAt: now}
	if l.gens != nil && len(gens) > 0 {
		it.gens, it.genKeys = gens, genKeys
	}
	l.mu.Lock()
	l.items[key] = it
	if l.max > 0 && len(l.items) > l.max {
		l.evictLocked(now)
	}
	l.mu.Unlock()
}

// evictLocked — эхлээд хугацаа дууссаныг, тэгээд хэрэгтэй бол санамсаргүй 10%-ийг хасна.
func (l *loader[V]) evictLocked(now time.Time) {
	for k, it := range l.items {
		if !now.Before(it.expires) {
			delete(l.items, k)
		}
	}
	target := l.max * 9 / 10
	for k := range l.items {
		if len(l.items) <= target {
			break
		}
		delete(l.items, k)
	}
}

func (l *loader[V]) invalidate(key string) {
	l.epoch.Add(1)
	l.mu.Lock()
	delete(l.items, key)
	l.mu.Unlock()
}

func (l *loader[V]) invalidateOrg(org string) {
	l.epoch.Add(1)
	l.mu.Lock()
	for k, it := range l.items {
		if it.org == org {
			delete(l.items, k)
		}
	}
	l.mu.Unlock()
}

func (l *loader[V]) size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.items)
}
