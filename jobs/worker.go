package jobs

import (
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"runtime/debug"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/khan-vision/khan/obs"
	"gorm.io/gorm"
)

// Handler — job боловсруулагч. Алдаа буцаавал (эсвэл panic хийвэл) дахин оролдоно
// (Permanent бол шууд dead). ctx нь per-job timeout-той бөгөөд lease алдагдах /
// worker зогсох / Cancel хийгдэх үед цуцлагдана — урт ажил ctx.Done()-г ЗААВАЛ
// хүндэтгэх ёстой (lease-ийг DB-д сунгаж чадахгүй бол ч ctx цуцлагдана). IDEMPOTENT байх ёстой.
type Handler func(ctx context.Context, j Record) error

// HandlerOption — Register-ийн нэмэлт тохиргоо.
type HandlerOption func(*handlerEntry)

// WithTimeout — энэ kind-ийн нэг оролдлогын хугацаа (Worker.JobTimeout-ийг дарна).
func WithTimeout(d time.Duration) HandlerOption {
	return func(e *handlerEntry) { e.timeout = d }
}

type handlerEntry struct {
	fn      Handler
	timeout time.Duration
}

// Анхдагч утгууд.
const (
	DefaultConcurrency     = 4
	DefaultPollInterval    = time.Second
	DefaultLease           = time.Minute
	DefaultJobTimeout      = 10 * time.Minute
	DefaultShutdownTimeout = 30 * time.Second

	// releaseGrace — ShutdownTimeout дууссаны дараа handler-уудыг цуцалж, буцаан
	// чөлөөлөх хүртэл хүлээх нэмэлт хугацаа.
	releaseGrace = 5 * time.Second
	finalizeTime = 10 * time.Second
	baseBackoff  = 5 * time.Second
	maxBackoff   = time.Hour
)

// DefaultBackoff — бүтэлгүйтсэн attempt (1-ээс эхэлсэн)-ийн дараах хүлээлт:
// 5с, 10с, 20с … 1 цагаар хязгаарлагдана, ±20% jitter-тэй (олон job нэгэн зэрэг
// дахин оролдож thundering herd үүсгэхгүй).
func DefaultBackoff(attempt int) time.Duration {
	d := backoffBase(attempt)
	// [0.8, 1.2)
	return time.Duration(float64(d) * (0.8 + 0.4*rand.Float64()))
}

// backoffBase — jitter-гүй экспоненциал хүлээлт.
func backoffBase(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 20 {
		return maxBackoff
	}
	d := baseBackoff << (attempt - 1)
	if d > maxBackoff || d <= 0 {
		d = maxBackoff
	}
	return d
}

// WorkerStats — Worker-ийн процесс доторх тоолуур (restart хүртэл).
type WorkerStats struct {
	Claimed   int64 // авсан нийт оролдлого
	Succeeded int64
	Retried   int64 // алдаад дахин оролдохоор товлогдсон
	Dead      int64 // dead болсон (Permanent, max_attempts, lease-ийн reap)
	Panics    int64
	Timeouts  int64
	Released  int64 // shutdown-д буцаан чөлөөлсөн
	LeaseLost int64 // lease алдагдаж, үр дүн хадгалагдаагүй
	Canceled  int64 // ажиллаж байхдаа Cancel хийгдэж `canceled` болсон
	InFlight  int64 // одоо ажиллаж буй
}

// Worker — queue-ээс job аваад Handler-т өгнө. Тэг утга + DB болон Register
// ашиглахад бэлэн. Олон процесс/instance зэрэг ажиллуулж болно. Worker-ийг
// хуулбарлаж болохгүй.
type Worker struct {
	DB *gorm.DB
	// Queues — авах queue-ууд; хоосон бол {DefaultQueue}.
	Queues []string
	// Concurrency — зэрэг ажиллах дээд job; 0 → DefaultConcurrency.
	Concurrency int
	// PollInterval — job байхгүй үед хүлээх; 0 → DefaultPollInterval.
	PollInterval time.Duration
	// Lease — job авсны дараах түгжээний хугацаа; 0 → DefaultLease. Heartbeat
	// Lease/3 тутамд сунгана; worker унавал Lease дуусмагц job өөр worker-т очно.
	// Lease-ийг Lease хугацаанд амжилттай сунгаж чадаагүй (DB хүрэхгүй, холболтын
	// pool дүүрсэн) бол handler-ийн ctx ErrLeaseLost-оор цуцлагдаж, үр дүн
	// хадгалагдахгүй. Heartbeat нь handler-ийн ТЭР pool-ыг ашигладаг тул pool-ыг
	// Concurrency-ээс том (handler-ийн DB холболт + heartbeat) тохируул.
	Lease time.Duration
	// JobTimeout — нэг оролдлогын дээд хугацаа; 0 → DefaultJobTimeout.
	JobTimeout time.Duration
	// ShutdownTimeout — Run-ийн ctx дуусахад ажиллаж буй job-ийг дуусгах хүлээлт;
	// 0 → DefaultShutdownTimeout. Хэтэрвэл handler-ийн ctx цуцлагдаж, job
	// чөлөөлөгдөнө (оролдлогод тооцогдохгүй).
	ShutdownTimeout time.Duration
	// Backoff — attempt-ийн дараах хүлээлт; nil → DefaultBackoff.
	Backoff func(attempt int) time.Duration
	// ID — энэ worker-ийн нэр (locked_by); хоосон бол host-pid-random.
	ID string
	// Logger — nil бол obs.From(ctx) (slog.Default).
	Logger *slog.Logger

	mu       sync.RWMutex
	handlers map[string]handlerEntry
	idOnce   sync.Once
	id       string

	claimed, succeeded, retried, dead, panics, timeouts, released, leaseLost, canceled, inflight atomic.Int64
}

// Register — kind-д handler бүртгэнэ. Нэг kind-д нэг handler. Run эхэлсний дараа
// ч дуудаж болно. Worker зөвхөн бүртгэлтэй kind-уудыг авна.
func (w *Worker) Register(kind string, h Handler, opts ...HandlerOption) error {
	if h == nil {
		return errors.New("jobs: handler nil")
	}
	if len(kind) > maxKindLen || !kindRe.MatchString(kind) {
		return fmt.Errorf("%w: %q", ErrInvalidKind, kind)
	}
	e := handlerEntry{fn: h}
	for _, o := range opts {
		o(&e)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, dup := w.handlers[kind]; dup {
		return fmt.Errorf("jobs: %q handler аль хэдийн бүртгэлтэй", kind)
	}
	if w.handlers == nil {
		w.handlers = map[string]handlerEntry{}
	}
	w.handlers[kind] = e
	return nil
}

// Stats — процесс доторх тоолуурууд.
func (w *Worker) Stats() WorkerStats {
	return WorkerStats{
		Claimed: w.claimed.Load(), Succeeded: w.succeeded.Load(), Retried: w.retried.Load(),
		Dead: w.dead.Load(), Panics: w.panics.Load(), Timeouts: w.timeouts.Load(),
		Released: w.released.Load(), LeaseLost: w.leaseLost.Load(), Canceled: w.canceled.Load(), InFlight: w.inflight.Load(),
	}
}

func (w *Worker) workerID() string {
	w.idOnce.Do(func() {
		if w.ID != "" {
			w.id = w.ID
			return
		}
		host, _ := os.Hostname()
		if host == "" {
			host = "host"
		}
		var b [4]byte
		_, _ = crand.Read(b[:])
		w.id = fmt.Sprintf("%s-%d-%s", host, os.Getpid(), hex.EncodeToString(b[:]))
	})
	return w.id
}

func (w *Worker) queues() []string {
	if len(w.Queues) == 0 {
		return []string{DefaultQueue}
	}
	return w.Queues
}

func (w *Worker) concurrency() int {
	if w.Concurrency > 0 {
		return w.Concurrency
	}
	return DefaultConcurrency
}

func (w *Worker) poll() time.Duration {
	if w.PollInterval > 0 {
		return w.PollInterval
	}
	return DefaultPollInterval
}

func (w *Worker) lease() time.Duration {
	if w.Lease > 0 {
		return w.Lease
	}
	return DefaultLease
}

func (w *Worker) jobTimeout() time.Duration {
	if w.JobTimeout > 0 {
		return w.JobTimeout
	}
	return DefaultJobTimeout
}

func (w *Worker) shutdownTimeout() time.Duration {
	if w.ShutdownTimeout > 0 {
		return w.ShutdownTimeout
	}
	return DefaultShutdownTimeout
}

func (w *Worker) backoff(attempt int) time.Duration {
	if w.Backoff != nil {
		return w.Backoff(attempt)
	}
	return DefaultBackoff(attempt)
}

func (w *Worker) log(ctx context.Context) *slog.Logger {
	if w.Logger != nil {
		return w.Logger
	}
	return obs.From(ctx)
}

func (w *Worker) kinds() []string {
	w.mu.RLock()
	defer w.mu.RUnlock()
	ks := make([]string, 0, len(w.handlers))
	for k := range w.handlers {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func (w *Worker) handler(kind string) (handlerEntry, bool) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	e, ok := w.handlers[kind]
	return e, ok
}

func (w *Worker) check() error {
	if w.DB == nil {
		return errors.New("jobs: Worker.DB nil")
	}
	if len(w.kinds()) == 0 {
		return errors.New("jobs: ямар ч handler бүртгээгүй (Register)")
	}
	return nil
}

// reap — lease нь дууссан бөгөөд оролдлогын эрх нь дууссан (унаж байгаад л
// ажиллуулсан "poison") job-ийг dead болгоно; цуцлах хүсэлттэй (cancel_requested)
// бол canceled болгоно. claim нь эдгээрийг (attempts < max_attempts, NOT
// cancel_requested нөхцөлөөр) хэзээ ч дахин авдаггүй тул reap-ийн дараа ч
// хоорондын уралдаан гарахгүй.
func (w *Worker) reap(ctx context.Context, kinds []string) (int64, error) {
	res := w.DB.WithContext(ctx).Exec(`UPDATE `+table+`
		SET status = CASE WHEN cancel_requested THEN 'canceled' ELSE 'dead' END,
			finished_at = now(), updated_at = now(), locked_by = NULL, locked_until = NULL,
			last_error = CASE WHEN cancel_requested THEN last_error
				ELSE 'lease дууссан: worker унасан эсвэл гацсан, max_attempts дууссан' END
		WHERE status = 'running' AND locked_until < now() AND (attempts >= max_attempts OR cancel_requested)
			AND queue IN ? AND kind IN ?`, w.queues(), kinds)
	if res.Error != nil {
		return 0, fmt.Errorf("jobs: reap: %w", res.Error)
	}
	if res.RowsAffected > 0 {
		w.dead.Add(res.RowsAffected)
		w.log(ctx).Warn("jobs: lease дууссан job-ууд dead/canceled болов", "count", res.RowsAffected)
	}
	return res.RowsAffected, nil
}

// claim — бэлэн (эсвэл lease нь дууссан) job-уудаас limit хүртэлхийг авна.
// Хоёрдахь утга нь claim-ийн хүсэлт илгээсэн цаг (lease-ийн локал хугацааны
// эхлэл; серверийн locked_until үүнээс хойш тул консерватив).
func (w *Worker) claim(ctx context.Context, limit int) ([]Record, time.Time, error) {
	kinds := w.kinds()
	if len(kinds) == 0 || limit <= 0 {
		return nil, time.Time{}, nil
	}
	if _, err := w.reap(ctx, kinds); err != nil {
		return nil, time.Time{}, err
	}
	claimedAt := time.Now()
	var rows []row
	err := w.DB.WithContext(ctx).Raw(`
		WITH c AS (
			SELECT id FROM `+table+`
			WHERE queue IN ? AND kind IN ?
				AND ((status = 'pending' AND run_at <= now())
					OR (status = 'running' AND locked_until < now()
						AND attempts < max_attempts AND NOT cancel_requested))
			ORDER BY priority DESC, run_at, id
			LIMIT ? FOR UPDATE SKIP LOCKED)
		UPDATE `+table+` AS j
		SET status = 'running', locked_by = ?, locked_until = now() + make_interval(secs => ?),
			attempts = j.attempts + 1, updated_at = now()
		FROM c WHERE j.id = c.id
		RETURNING `+columns("j."),
		w.queues(), kinds, limit, w.workerID(), w.lease().Seconds()).Scan(&rows).Error
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("jobs: claim: %w", err)
	}
	recs := make([]Record, len(rows))
	for i, r := range rows {
		recs[i] = r.record()
	}
	sort.Slice(recs, func(a, b int) bool {
		if recs[a].Priority != recs[b].Priority {
			return recs[a].Priority > recs[b].Priority
		}
		return recs[a].ID < recs[b].ID
	})
	w.claimed.Add(int64(len(recs)))
	return recs, claimedAt, nil
}

// RunOnce — нэг багц (Concurrency хүртэл) бэлэн job-ийг аваад ДУУСТАЛ ажиллуулна.
// Боловсруулсан (амжилттай эсвэл алдаатай) job-ийн тоог буцаана. Тест болон
// гараар ажиллуулахад тохиромжтой. ctx цуцлагдвал ажиллаж буй job-ууд
// чөлөөлөгдөнө.
func (w *Worker) RunOnce(ctx context.Context) (int, error) {
	if err := w.check(); err != nil {
		return 0, err
	}
	recs, claimedAt, err := w.claim(ctx, w.concurrency())
	if err != nil {
		return 0, err
	}
	jobCtx, cancel := context.WithCancelCause(context.WithoutCancel(ctx))
	defer cancel(nil)
	stop := context.AfterFunc(ctx, func() { cancel(ErrShutdown) })
	defer stop()

	var wg sync.WaitGroup
	for _, rec := range recs {
		wg.Add(1)
		w.inflight.Add(1)
		go func() {
			defer wg.Done()
			defer w.inflight.Add(-1)
			w.execute(jobCtx, rec, claimedAt)
		}()
	}
	wg.Wait()
	return len(recs), nil
}

// Run — ctx дуустал job авч ажиллуулна. ctx дуусахад шинээр авахаа болиод
// ажиллаж буй job-уудыг ShutdownTimeout хүртэл хүлээнэ; хэтэрвэл тэдгээрийн ctx-ийг
// цуцалж job-ийг буцаан чөлөөлнө. Бүх job дууссан бол nil; handler ctx-ийг
// хүндэтгэхгүй гацсан бол алдаа (тэр job-ууд lease дуусмагц reclaim болно).
func (w *Worker) Run(ctx context.Context) error {
	if err := w.check(); err != nil {
		return err
	}
	jobCtx, cancelJobs := context.WithCancelCause(context.WithoutCancel(ctx))
	defer cancelJobs(nil)

	conc := w.concurrency()
	var wg sync.WaitGroup
	wake := make(chan struct{}, 1)
	log := w.log(ctx)
	log.Info("jobs: worker эхэллээ", "worker", w.workerID(), "queues", w.queues(), "concurrency", conc)

	for ctx.Err() == nil {
		if free := conc - int(w.inflight.Load()); free > 0 {
			recs, claimedAt, err := w.claim(ctx, free)
			if err != nil && ctx.Err() == nil {
				log.Error("jobs: claim алдаа", "error", err)
			}
			for _, rec := range recs {
				wg.Add(1)
				w.inflight.Add(1)
				go func() {
					defer wg.Done()
					w.execute(jobCtx, rec, claimedAt)
					w.inflight.Add(-1)
					select {
					case wake <- struct{}{}:
					default:
					}
				}()
			}
		}
		// Слот чөлөөлөгдөх (wake), poll хугацаа, эсвэл зогсоох хүртэл хүлээнэ.
		timer := time.NewTimer(w.poll())
		select {
		case <-ctx.Done():
		case <-wake:
		case <-timer.C:
		}
		timer.Stop()
	}

	log.Info("jobs: worker зогсож байна", "worker", w.workerID(), "in_flight", w.inflight.Load())
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-time.After(w.shutdownTimeout()):
	}
	cancelJobs(ErrShutdown)
	select {
	case <-done:
		return nil
	case <-time.After(releaseGrace):
		return fmt.Errorf("jobs: %d job зогссонгүй (handler ctx-ийг хүндэтгэхгүй байна); lease дуусмагц reclaim болно", w.inflight.Load())
	}
}

// execute — нэг job-ийг ажиллуулж, үр дүнг fencing-тэй тэмдэглэнэ.
func (w *Worker) execute(jobCtx context.Context, rec Record, claimedAt time.Time) {
	log := w.log(jobCtx).With("job_id", rec.ID, "kind", rec.Kind, "queue", rec.Queue, "attempt", rec.Attempts)
	if rec.OrgID != "" {
		log = log.With("org_id", rec.OrgID)
	}
	entry, ok := w.handler(rec.Kind)
	if !ok { // Register-ийг хэзээ ч устгахгүй тул ердөө тохиолдохгүй; хамгаалалт.
		w.finalizeRelease(jobCtx, rec, log)
		return
	}
	timeout := entry.timeout
	if timeout <= 0 {
		timeout = w.jobTimeout()
	}

	hctx, cancel := context.WithCancelCause(jobCtx)
	defer cancel(nil)
	tctx, tcancel := context.WithTimeoutCause(hctx, timeout, ErrJobTimeout)
	defer tcancel()

	// Heartbeat: lease сунгана; алдагдвал / сунгаж чадахгүй бол / Cancel ирвэл handler-ийг цуцална.
	var st hbState
	hbDone := make(chan struct{})
	hbStop := make(chan struct{})
	go func() {
		defer close(hbDone)
		w.heartbeat(hctx, hbStop, rec, log, claimedAt, &st, cancel)
	}()

	start := time.Now()
	err := w.safeCall(tctx, entry.fn, rec, log)
	close(hbStop)
	<-hbDone
	elapsed := time.Since(start)

	// Lease-ийг локалаар алдсан (сунгаж чадаагүй): өөр worker аль хэдийн авсан байж
	// болзошгүй тул үр дүн БИЧИХГҮЙ — job lease дуусмагц (DB-ийн төлвөөр) reclaim/reap болно.
	if st.lost.Load() {
		w.leaseLost.Add(1)
		log.Warn("jobs: lease сунгаж чадаагүй тул үр дүн хадгалагдсангүй", "error", err)
		return
	}
	// Ажиллаж байхдаа Cancel хийгдсэн: handler алдаатай (ctx цуцлагдсан) бол canceled.
	if err != nil && st.canceled.Load() {
		fctx, fcancel := context.WithTimeout(context.WithoutCancel(jobCtx), finalizeTime)
		defer fcancel()
		n, ferr := w.markCanceled(fctx, rec)
		w.afterFinalize(n, ferr, log, "canceled", &w.canceled, elapsed, nil)
		return
	}

	// Handler nil буцаасан бол (хугацаа хэтэрсэн ч) амжилт гэж үзнэ.
	if err != nil && tctx.Err() != nil {
		switch cause := context.Cause(tctx); {
		case errors.Is(cause, ErrShutdown):
			w.finalizeRelease(jobCtx, rec, log)
			return
		case errors.Is(cause, ErrJobTimeout):
			w.timeouts.Add(1)
			err = fmt.Errorf("%w (%s): %w", ErrJobTimeout, timeout, err)
		}
	}

	fctx, fcancel := context.WithTimeout(context.WithoutCancel(jobCtx), finalizeTime)
	defer fcancel()
	if err == nil {
		n, ferr := w.markSucceeded(fctx, rec)
		w.afterFinalize(n, ferr, log, "succeeded", &w.succeeded, elapsed, nil)
		return
	}
	w.fail(fctx, rec, err, log, elapsed)
}

func (w *Worker) afterFinalize(n int64, ferr error, log *slog.Logger, what string, counter *atomic.Int64, elapsed time.Duration, cause error) {
	switch {
	case ferr != nil:
		log.Error("jobs: төлөв тэмдэглэж чадсангүй", "state", what, "error", ferr)
	case n == 0:
		w.leaseLost.Add(1)
		log.Warn("jobs: lease алдагдсан тул үр дүн хадгалагдсангүй", "state", what)
	default:
		counter.Add(1)
		if cause != nil {
			log.Info("jobs: job "+what, "elapsed", elapsed, "error", cause)
		} else {
			log.Info("jobs: job "+what, "elapsed", elapsed)
		}
	}
}

// fail — алдааг retry эсвэл dead болгон тэмдэглэнэ.
func (w *Worker) fail(ctx context.Context, rec Record, herr error, log *slog.Logger, elapsed time.Duration) {
	msg := truncateErr(herr.Error())
	if IsPermanent(herr) || rec.Attempts >= rec.MaxAttempts {
		n, err := w.markDead(ctx, rec, msg)
		w.afterFinalize(n, err, log, "dead", &w.dead, elapsed, herr)
		return
	}
	wait := w.backoff(rec.Attempts)
	if wait < 0 {
		wait = 0
	}
	n, err := w.markRetry(ctx, rec, msg, wait)
	w.afterFinalize(n, err, log, "retry", &w.retried, elapsed, herr)
}

func (w *Worker) markSucceeded(ctx context.Context, rec Record) (int64, error) {
	res := w.DB.WithContext(ctx).Exec(`UPDATE `+table+`
		SET status = 'succeeded', finished_at = now(), updated_at = now(), locked_by = NULL, locked_until = NULL
		WHERE id = ? AND status = 'running' AND locked_by = ? AND attempts = ?`,
		rec.ID, w.workerID(), rec.Attempts)
	return res.RowsAffected, res.Error
}

func (w *Worker) markCanceled(ctx context.Context, rec Record) (int64, error) {
	res := w.DB.WithContext(ctx).Exec(`UPDATE `+table+`
		SET status = 'canceled', finished_at = now(), updated_at = now(), locked_by = NULL, locked_until = NULL
		WHERE id = ? AND status = 'running' AND locked_by = ? AND attempts = ?`,
		rec.ID, w.workerID(), rec.Attempts)
	return res.RowsAffected, res.Error
}

func (w *Worker) markDead(ctx context.Context, rec Record, msg string) (int64, error) {
	res := w.DB.WithContext(ctx).Exec(`UPDATE `+table+`
		SET status = 'dead', last_error = ?, finished_at = now(), updated_at = now(), locked_by = NULL, locked_until = NULL
		WHERE id = ? AND status = 'running' AND locked_by = ? AND attempts = ?`,
		msg, rec.ID, w.workerID(), rec.Attempts)
	return res.RowsAffected, res.Error
}

func (w *Worker) markRetry(ctx context.Context, rec Record, msg string, wait time.Duration) (int64, error) {
	res := w.DB.WithContext(ctx).Exec(`UPDATE `+table+`
		SET status = CASE WHEN cancel_requested THEN 'canceled' ELSE 'pending' END,
			finished_at = CASE WHEN cancel_requested THEN now() ELSE finished_at END,
			last_error = ?, run_at = now() + make_interval(secs => ?),
			updated_at = now(), locked_by = NULL, locked_until = NULL
		WHERE id = ? AND status = 'running' AND locked_by = ? AND attempts = ?`,
		msg, wait.Seconds(), rec.ID, w.workerID(), rec.Attempts)
	return res.RowsAffected, res.Error
}

// finalizeRelease — shutdown-д job-ийг оролдлогод тооцохгүйгээр шууд авагдахаар буцаана.
func (w *Worker) finalizeRelease(jobCtx context.Context, rec Record, log *slog.Logger) {
	fctx, cancel := context.WithTimeout(context.WithoutCancel(jobCtx), finalizeTime)
	defer cancel()
	res := w.DB.WithContext(fctx).Exec(`UPDATE `+table+`
		SET status = CASE WHEN cancel_requested THEN 'canceled' ELSE 'pending' END,
			finished_at = CASE WHEN cancel_requested THEN now() ELSE finished_at END,
			attempts = attempts - 1, run_at = now(), updated_at = now(),
			locked_by = NULL, locked_until = NULL
		WHERE id = ? AND status = 'running' AND locked_by = ? AND attempts = ?`,
		rec.ID, w.workerID(), rec.Attempts)
	switch {
	case res.Error != nil:
		log.Error("jobs: job чөлөөлж чадсангүй (lease дуусмагц reclaim болно)", "error", res.Error)
	case res.RowsAffected == 0:
		w.leaseLost.Add(1)
	default:
		w.released.Add(1)
		log.Info("jobs: shutdown — job буцаан чөлөөлөгдлөө")
	}
}

// hbState — heartbeat-ийн execute-д дамжуулах төлөв.
type hbState struct {
	lost     atomic.Bool // lease-ийг Lease хугацаанд сунгаж чадаагүй (үр дүн бичихгүй)
	canceled atomic.Bool // Cancel хүсэлт ажиглагдсан
}

// heartbeat — Lease/3 тутам lease сунгана.
//
//   - Мөр fence-ээр олдохгүй (өөр worker авсан) бол ErrLeaseLost-оор цуцлаад зогсоно.
//   - Сунгалт амжилттай бүрд локал хугацаа = хүсэлт илгээсэн цаг + Lease (серверийн
//     locked_until-аас үргэлж эрт). DB-ийн түр алдаа/pool дүүрэлтээс болж энэ хугацаа
//     дуусвал өөр worker reclaim хийсэн байж болзошгүй тул handler-ийн ctx
//     ErrLeaseLost-оор цуцлагдаж, st.lost тавигдана (үр дүн бичигдэхгүй).
//   - cancel_requested ажиглагдвал ErrCancelRequested-оор цуцалж, lease-ийг үргэлжлүүлэн
//     сунгана (handler зогсох хүртэл job түлхүүрээ эзэлнэ).
func (w *Worker) heartbeat(ctx context.Context, stop <-chan struct{}, rec Record, log *slog.Logger,
	leaseStart time.Time, st *hbState, cancel context.CancelCauseFunc) {
	lease := w.lease()
	every := lease / 3
	if every < time.Millisecond {
		every = time.Millisecond
	}
	deadline := leaseStart.Add(lease)
	lost := func() {
		st.lost.Store(true)
		log.Error("jobs: lease-ийг хугацаандаа сунгаж чадсангүй — handler-ийг цуцалж байна")
		cancel(ErrLeaseLost)
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ctx.Done():
			return
		case <-t.C:
		}
		now := time.Now()
		if !now.Before(deadline) {
			lost()
			return
		}
		hctx, hcancel := context.WithTimeout(context.WithoutCancel(ctx), deadline.Sub(now))
		var cr []bool
		err := w.DB.WithContext(hctx).Raw(`UPDATE `+table+`
			SET locked_until = now() + make_interval(secs => ?), updated_at = now()
			WHERE id = ? AND status = 'running' AND locked_by = ? AND attempts = ?
			RETURNING cancel_requested`,
			lease.Seconds(), rec.ID, w.workerID(), rec.Attempts).Scan(&cr).Error
		hcancel()
		switch {
		case err != nil:
			log.Warn("jobs: heartbeat алдаа", "error", err)
			if !time.Now().Before(deadline) {
				lost()
				return
			}
		case len(cr) == 0:
			log.Warn("jobs: lease алдагдсан — handler-ийг цуцалж байна")
			cancel(ErrLeaseLost)
			return
		default:
			deadline = now.Add(lease)
			if cr[0] && !st.canceled.Swap(true) {
				log.Info("jobs: Cancel хүсэлт — handler-ийг цуцалж байна")
				cancel(ErrCancelRequested)
			}
		}
	}
}

// safeCall — handler-ийн panic-ийг алдаа болгоно.
func (w *Worker) safeCall(ctx context.Context, h Handler, rec Record, log *slog.Logger) (err error) {
	defer func() {
		if r := recover(); r != nil {
			w.panics.Add(1)
			stack := debug.Stack()
			log.Error("jobs: handler panic", "panic", fmt.Sprint(r), "stack", string(stack))
			err = fmt.Errorf("jobs: handler panic: %v", r)
		}
	}()
	return h(ctx, rec)
}
