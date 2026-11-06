package auth

import (
	"sync"
	"time"
)

// LockoutStep — Failures удаагийн дараалсан алдаанд Duration хугацаагаар түгжинэ.
type LockoutStep struct {
	Failures int
	Duration time.Duration
}

// LimiterConfig — Limiter-ийн тохиргоо. Тэг утгууд нь тохирох анхдагчтай.
type LimiterConfig struct {
	// Window + MaxRequests — гүйдэг цонхонд Allow-ын тоо. MaxRequests=0 бол
	// хүсэлтийн тоог хязгаарлахгүй (зөвхөн lockout).
	Window      time.Duration
	MaxRequests int
	// Steps — өсөн нэмэгдэх lockout (Failures өсөх дараалалтай). Хоосон бол lockout байхгүй.
	// Хуучин lambda: 3→10м, 6→1ц, 9+→24ц. Завсрын алдаанд (4, 5) шинэ түгжээ тавихгүй;
	// сүүлийн шатнаас хойш алдаа бүр түгжинэ.
	Steps []LockoutStep
	// FailureTTL — сүүлийн алдаанаас (эсвэл түгжээ дуусснаас) хойш алдааны тоолуур
	// мартагдах хугацаа (анхдагч 24ц).
	FailureTTL time.Duration
	// SweepEvery — Allow/Fail дотор хийгдэх автомат цэвэрлэгээний завсар (анхдагч 1мин).
	SweepEvery time.Duration
	// Now — тестэд цаг солих (анхдагч time.Now).
	Now func() time.Time
}

// DefaultLoginSteps — lambda login lockout-ийн дүрэм (3→10м, 6→1ц, 9→24ц).
func DefaultLoginSteps() []LockoutStep {
	return []LockoutStep{
		{Failures: 3, Duration: 10 * time.Minute},
		{Failures: 6, Duration: time.Hour},
		{Failures: 9, Duration: 24 * time.Hour},
	}
}

// LoginLimiterConfig — login-ийн нэрээр түлхүүрлэх: 10 минутад 20 оролдлого
// (lambdaguard loginLoginLimit) + DefaultLoginSteps lockout.
func LoginLimiterConfig() LimiterConfig {
	return LimiterConfig{Window: 10 * time.Minute, MaxRequests: 20, Steps: DefaultLoginSteps()}
}

// IPLimiterConfig — IP-ээр түлхүүрлэх: 10 минутад 120 хүсэлт, lockout-гүй
// (сургуулийн анги нэг NAT IP-аас зэрэг нэвтэрдэг тул өргөн; lambdaguard loginIPLimit).
func IPLimiterConfig() LimiterConfig {
	return LimiterConfig{Window: 10 * time.Minute, MaxRequests: 120}
}

// Limiter — түлхүүрээр (IP, login …) rate limit + өсөн нэмэгдэх lockout. Бүх
// метод mutex-аар хамгаалагдсан тул олон goroutine-оос аюулгүй (хуучин lambda-гийн
// failedAttempts/lockoutUntil map-ууд mutex-гүй, зэрэг бичилтээр "concurrent map
// writes" гарч процесс унадаг байв). Тусдаа goroutine ЭХЛҮҮЛДЭГГҮЙ: цэвэрлэгээ нь
// Allow/Fail дотор SweepEvery бүрт, эсвэл Sweep-ийг гараар дуудна. Процесс
// дахин асахад төлөв алдагдана (in-memory).
type Limiter struct {
	cfg LimiterConfig

	mu        sync.Mutex
	entries   map[string]*limiterEntry
	lastSweep time.Time
}

type limiterEntry struct {
	requests    []time.Time // цонх доторх зөвшөөрсөн хүсэлтүүд (≤ MaxRequests)
	failures    int
	lastFail    time.Time
	lockedUntil time.Time
}

// NewLimiter — тохиргоотой Limiter.
func NewLimiter(cfg LimiterConfig) *Limiter {
	if cfg.Window <= 0 {
		cfg.Window = 10 * time.Minute
	}
	if cfg.FailureTTL <= 0 {
		cfg.FailureTTL = 24 * time.Hour
	}
	if cfg.SweepEvery <= 0 {
		cfg.SweepEvery = time.Minute
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	cfg.Steps = append([]LockoutStep(nil), cfg.Steps...)
	return &Limiter{cfg: cfg, entries: make(map[string]*limiterEntry), lastSweep: cfg.Now()}
}

// Allow — хүсэлтийг зөвшөөрөх эсэх. Түгжээтэй эсвэл цонхны хязгаарт хүрсэн бол
// ok=false ба retryAfter (хэзээ дахин оролдох). Зөвшөөрсөн хүсэлт цонхонд тоологдоно;
// татгалзсан нь тоологдохгүй (санах ой хязгаарлагдмал).
func (l *Limiter) Allow(key string) (ok bool, retryAfter time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.cfg.Now()
	l.maybeSweep(now)

	e := l.entries[key]
	if e == nil {
		e = &limiterEntry{}
		l.entries[key] = e
	}
	if now.Before(e.lockedUntil) {
		return false, e.lockedUntil.Sub(now)
	}
	if l.cfg.MaxRequests > 0 {
		e.requests = pruneBefore(e.requests, now.Add(-l.cfg.Window))
		if len(e.requests) >= l.cfg.MaxRequests {
			return false, e.requests[0].Add(l.cfg.Window).Sub(now)
		}
		e.requests = append(e.requests, now)
	}
	return true, 0
}

// Fail — амжилтгүй оролдлогыг бүртгэнэ; шинэ түгжээ тавигдсан бол түүний хугацааг
// (үгүй бол 0) буцаана.
func (l *Limiter) Fail(key string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.cfg.Now()
	l.maybeSweep(now)

	e := l.entries[key]
	if e == nil {
		e = &limiterEntry{}
		l.entries[key] = e
	}
	if e.failures > 0 && now.Sub(e.lastFail) > l.cfg.FailureTTL && !now.Before(e.lockedUntil) {
		e.failures = 0 // хуучин алдаа мартагдсан
	}
	e.failures++
	e.lastFail = now
	for i := len(l.cfg.Steps) - 1; i >= 0; i-- {
		s := l.cfg.Steps[i]
		if e.failures < s.Failures {
			continue
		}
		if e.failures == s.Failures || i == len(l.cfg.Steps)-1 {
			e.lockedUntil = now.Add(s.Duration)
			return s.Duration
		}
		break
	}
	return 0
}

// Reset — түлхүүрийн бүх төлөвийг (алдаа, түгжээ, цонх) арилгана (амжилттай нэвтрэлт).
func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, key)
}

// Len — одоо хадгалж буй түлхүүрийн тоо (тест / метрик).
func (l *Limiter) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.entries)
}

// Sweep — хугацаа нь дууссан түлхүүрүүдийг устгана. Хэрэглэгч өөрийн ticker-ээс
// дуудаж болно; Allow/Fail мөн SweepEvery бүрт өөрөө дуудна.
func (l *Limiter) Sweep() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweep(l.cfg.Now())
}

func (l *Limiter) maybeSweep(now time.Time) {
	if now.Sub(l.lastSweep) >= l.cfg.SweepEvery {
		l.sweep(now)
	}
}

func (l *Limiter) sweep(now time.Time) {
	l.lastSweep = now
	for k, e := range l.entries {
		if now.Before(e.lockedUntil) {
			continue
		}
		e.requests = pruneBefore(e.requests, now.Add(-l.cfg.Window))
		if len(e.requests) > 0 {
			continue
		}
		if e.failures > 0 && now.Sub(e.lastFail) <= l.cfg.FailureTTL {
			continue
		}
		delete(l.entries, k)
	}
}

// pruneBefore — cutoff-оос өмнөх (эрэмбэлэгдсэн) цагуудыг хасна.
func pruneBefore(ts []time.Time, cutoff time.Time) []time.Time {
	i := 0
	for i < len(ts) && !ts[i].After(cutoff) {
		i++
	}
	if i == 0 {
		return ts
	}
	return append(ts[:0], ts[i:]...)
}
