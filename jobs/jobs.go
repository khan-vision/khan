// Package jobs нь Postgres дээр суурилсан background job queue + cron scheduler.
//
// Модулиуд тус бүрдээ тарсан cron, `pg_try_advisory_lock`-оор гараар түгжсэн,
// instance бүр дээр ажилладаг, алдаа нь харагддаггүй ажлуудыг нэг хэлбэрт оруулна:
//
//   - Ажлыг бизнес өгөгдөлтэй ИЖИЛ транзакцад Enqueue хийнэ — commit/rollback хамт.
//   - Worker нь `FOR UPDATE SKIP LOCKED` + lease-ээр авна: олон instance нэг job-ийг
//     хоёр удаа ажиллуулахгүй; унасан worker-ийн job lease дуусмагц өөр worker-т очно.
//   - Алдвал экспоненциал backoff + jitter-ээр дахин оролдож, max_attempts хэтэрвэл
//     `dead` (dead-letter) болно; бүгд `khan.jobs`-д харагдана (List/Stats/Retry/Cancel).
//   - Scheduler нь cron хуваарийг tick бүрд тогтмол түлхүүрээр (нэр + tick-ийн цаг)
//     enqueue хийнэ — тусдаа leader сонгох шаардлагагүй, олон instance-аас яг нэг job.
//
// Хэрэглээ:
//
//	// Бүртгэл
//	w := &jobs.Worker{DB: gdb, Queues: []string{"default"}, Concurrency: 8}
//	w.Register("billing.send_invoice", func(ctx context.Context, j jobs.Record) error {
//		var p struct{ InvoiceID string }
//		if err := j.Decode(&p); err != nil {
//			return jobs.Permanent(err) // дахин оролдох утгагүй
//		}
//		return sendInvoice(ctx, j.OrgID, p.InvoiceID)
//	})
//	go w.Run(ctx)
//
//	// Бизнес өгөгдөлтэй атомар enqueue
//	err := gdb.Transaction(func(tx *gorm.DB) error {
//		if err := tx.Create(&inv).Error; err != nil { return err }
//		_, err := jobs.Enqueue(ctx, tx, jobs.Job{
//			Kind: "billing.send_invoice", OrgID: orgID, Payload: map[string]string{"InvoiceID": inv.ID},
//		})
//		return err
//	})
//
//	// Cron
//	s := &jobs.Scheduler{DB: gdb, Schedules: []jobs.Schedule{
//		{Name: "nightly-report", Spec: "0 2 * * *", Kind: "report.nightly"},
//	}}
//	go s.Run(ctx)
//
// Delivery нь AT-LEAST-ONCE: handler IDEMPOTENT байх ёстой (lease алдагдсан/унасан
// үед job дахин ажиллана; Record.ID, Record.Attempts-ыг ашиглаж болно).
// Хүснэгтийг SchemaSQL-ээр үүсгэнэ (app-ийн migration-д оруулна).
//
// events package-аас ялгаа: events = domain event (юу болсон, олон handler
// сонсож болно); jobs = нэг тодорхой ажил (хийх), цаг товлох, дахин оролдох, харах.
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

// SchemaSQL — job queue болон schedule-ийн хүснэгтүүдийн идемпотент DDL.
//
// Зэрэг ажиллах migration-уудтай (CREATE SCHEMA IF NOT EXISTS уралдаан) хамт
// ажиллуулбал advisory lock-оор дарааллуулна (events тестийг үз).
//
// unique_key нь байгууллагаар тусгаарлагдсан: давхардлын түлхүүр =
// (queue, kind, org_id (NULL=систем), unique_key). Хуучин org-гүй индексийг
// шинийг үүсгэсний дараа устгана (идемпотент шилжилт).
const SchemaSQL = `
CREATE SCHEMA IF NOT EXISTS khan;
CREATE TABLE IF NOT EXISTS khan.jobs (
	id           bigserial PRIMARY KEY,
	queue        text NOT NULL DEFAULT 'default',
	kind         text NOT NULL,
	org_id       uuid,
	payload      jsonb NOT NULL DEFAULT 'null'::jsonb,
	priority     int NOT NULL DEFAULT 0,
	run_at       timestamptz NOT NULL DEFAULT now(),
	attempts     int NOT NULL DEFAULT 0,
	max_attempts int NOT NULL DEFAULT 5,
	last_error   text,
	locked_by    text,
	locked_until timestamptz,
	status       text NOT NULL DEFAULT 'pending'
		CHECK (status IN ('pending','running','succeeded','dead','canceled')),
	unique_key   text,
	schedule     text,
	created_at   timestamptz NOT NULL DEFAULT now(),
	updated_at   timestamptz NOT NULL DEFAULT now(),
	finished_at  timestamptz,
	cancel_requested boolean NOT NULL DEFAULT false
);
ALTER TABLE khan.jobs ADD COLUMN IF NOT EXISTS cancel_requested boolean NOT NULL DEFAULT false;
CREATE INDEX IF NOT EXISTS jobs_claim_idx
	ON khan.jobs (queue, priority DESC, run_at, id)
	WHERE status IN ('pending','running');
CREATE INDEX IF NOT EXISTS jobs_lease_idx
	ON khan.jobs (locked_until)
	WHERE status = 'running';
CREATE UNIQUE INDEX IF NOT EXISTS jobs_unique_key_org_idx
	ON khan.jobs (queue, kind, COALESCE(org_id, '00000000-0000-0000-0000-000000000000'::uuid), unique_key)
	WHERE unique_key IS NOT NULL AND status IN ('pending','running');
DROP INDEX IF EXISTS khan.jobs_unique_key_idx;
CREATE INDEX IF NOT EXISTS jobs_org_idx
	ON khan.jobs (org_id, created_at DESC)
	WHERE org_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS jobs_schedule_idx
	ON khan.jobs (schedule)
	WHERE schedule IS NOT NULL AND status IN ('pending','running');
CREATE TABLE IF NOT EXISTS khan.job_schedule_runs (
	name          text NOT NULL,
	scheduled_for timestamptz NOT NULL,
	job_id        bigint,
	note          text,
	created_at    timestamptz NOT NULL DEFAULT now(),
	PRIMARY KEY (name, scheduled_for)
);
`

const (
	table      = "khan.jobs"
	runsTable  = "khan.job_schedule_runs"
	maxErrLen  = 2000
	maxKindLen = 128
)

// Анхдагч утгууд.
const (
	DefaultQueue       = "default"
	DefaultMaxAttempts = 5
)

// Status — job-ийн төлөв.
type Status string

// Job-ийн төлвүүд.
const (
	StatusPending   Status = "pending"   // хүлээж буй (run_at болтол)
	StatusRunning   Status = "running"   // worker авсан (lease-тэй)
	StatusSucceeded Status = "succeeded" // амжилттай дууссан
	StatusDead      Status = "dead"      // оролдлого дууссан / Permanent — dead-letter
	StatusCanceled  Status = "canceled"  // Cancel хийсэн
)

func (s Status) valid() bool {
	switch s {
	case StatusPending, StatusRunning, StatusSucceeded, StatusDead, StatusCanceled:
		return true
	}
	return false
}

// Алдаанууд.
var (
	ErrInvalidKind  = errors.New("jobs: kind буруу (жижиг үсэг, тоо, `_`; цэгээр тусгаарлана, жнь billing.send_invoice)")
	ErrInvalidQueue = errors.New("jobs: queue буруу (жижиг үсэг, тоо, `_`, `-`, `.`; <= 63 тэмдэгт)")
	ErrInvalidOrg   = errors.New("jobs: org_id UUID биш")
	ErrNotInTx      = errors.New("jobs: Enqueue нь бизнес өгөгдөлтэй ижил транзакцаар дуудагдах ёстой")
	ErrNoUniqueKey  = errors.New("jobs: EnqueueUnique-д UniqueKey шаардлагатай")
	ErrNotFound     = errors.New("jobs: job олдсонгүй")
	ErrState        = errors.New("jobs: job-ийн одоогийн төлөвт энэ үйлдэл боломжгүй")
	ErrDuplicate    = errors.New("jobs: ижил unique_key-тэй идэвхтэй job байна")
	ErrScope        = errors.New("jobs: Scope заавал (OrgScope(orgID) эсвэл SystemScope())")

	// ErrLeaseLost — handler-ийн ctx-ийн cause: lease алдагдсан (өөр worker авсан
	// эсвэл heartbeat-ээр lease-ийг Lease хугацаанд сунгаж чадаагүй). Handler
	// ctx.Done()-г хүндэтгэн зогсох ёстой.
	ErrLeaseLost = errors.New("jobs: lease алдагдсан (job цуцлагдсан эсвэл өөр worker-т шилжсэн)")
	// ErrCancelRequested — handler-ийн ctx-ийн cause: ажиллаж буй job-д Cancel
	// хийгдсэн. Worker handler зогсмогц job-ийг `canceled` болгоно.
	ErrCancelRequested = errors.New("jobs: job-д цуцлах хүсэлт ирсэн")
	// ErrJobTimeout — handler-ийн ctx-ийн cause: нэг оролдлогын хугацаа дууссан.
	ErrJobTimeout = errors.New("jobs: job-ийн хугацаа хэтэрсэн")
	// ErrShutdown — handler-ийн ctx-ийн cause: worker зогсож байна, job буцаан
	// чөлөөлөгдөнө (оролдлогод тооцогдохгүй).
	ErrShutdown = errors.New("jobs: worker зогсож байна")

	kindRe  = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)*$`)
	queueRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_.\-]{0,62}$`)
	uuidRe  = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

// permanentError — дахин оролдох ёсгүй алдаа.
type permanentError struct{ err error }

func (p permanentError) Error() string { return p.err.Error() }
func (p permanentError) Unwrap() error { return p.err }

// Permanent — err-ийг "дахин оролдохгүй" гэж тэмдэглэнэ: job шууд dead болно
// (буруу payload, байхгүй болсон бичлэг гэх мэт). err == nil бол nil.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return permanentError{err}
}

// IsPermanent — err нь Permanent-ээр ороогдсон эсэх.
func IsPermanent(err error) bool {
	var p permanentError
	return errors.As(err, &p)
}

// Job — enqueue хийх ажил.
type Job struct {
	// Queue — хоосон бол DefaultQueue. Worker зөвхөн өөрийн Queues-ийг авна.
	Queue string
	// Kind — заавал; Worker.Register-ийн түлхүүр (жнь "billing.send_invoice").
	Kind string
	// OrgID — хоосон бол системийн түвшний job (NULL). Org-scope-той admin helper-ууд үүгээр шүүнэ.
	OrgID string
	// Payload — JSON болгож хадгалагдана; nil → null.
	Payload any
	// Priority — их тоо түрүүлж авагдана (анхдагч 0).
	Priority int
	// RunAt — энэ цагаас өмнө авагдахгүй. Тэг утга + Delay==0 → яг одоо.
	RunAt time.Time
	// Delay — DB-ийн now()-аас хойш хойшлуулах (RunAt тавигдсан бол үл тооно).
	Delay time.Duration
	// MaxAttempts — 0 → DefaultMaxAttempts. Нийт оролдлогын тоо (анхны оролдлого орно).
	MaxAttempts int
	// UniqueKey — хоосон биш бол (queue, kind, unique_key)-ээр pending/running
	// байгаа ХООРОНД давхардал үүсгэхгүй. Дууссан (succeeded/dead/canceled) job
	// түлхүүрийг чөлөөлнө. Түлхүүр нь байгууллагаар (OrgID, хоосон=систем) тусгаарлагдсан тул өөр org ижил түлхүүр ашиглаж болно.
	UniqueKey string
}

// Record — хадгалагдсан job (handler-т өгөгдөх болон List-ийн мөр).
type Record struct {
	ID          int64
	Queue       string
	Kind        string
	OrgID       string // NULL бол ""
	Payload     json.RawMessage
	Priority    int
	RunAt       time.Time
	Attempts    int // handler-т: энэ оролдлогын дугаар (1 = анхны)
	MaxAttempts int
	LastError   string
	LockedBy    string
	LockedUntil *time.Time
	Status      Status
	UniqueKey   string
	Schedule    string // scheduler үүсгэсэн бол schedule-ийн нэр
	CreatedAt   time.Time
	UpdatedAt   time.Time
	FinishedAt  *time.Time
	// CancelRequested — running job-д Cancel хийгдсэн (worker зогсоож байна).
	CancelRequested bool
}

// Decode — payload-ийг v руу задлана.
func (r Record) Decode(v any) error {
	if err := json.Unmarshal(r.Payload, v); err != nil {
		return fmt.Errorf("jobs: payload задлах (id=%d): %w", r.ID, err)
	}
	return nil
}

// row — DB scan-ийн завсрын бүтэц (NULL-ийг pointer-аар).
type row struct {
	ID              int64
	Queue           string
	Kind            string
	OrgID           *string
	Payload         []byte
	Priority        int
	RunAt           time.Time
	Attempts        int
	MaxAttempts     int
	LastError       *string
	LockedBy        *string
	LockedUntil     *time.Time
	Status          string
	UniqueKey       *string
	Schedule        *string
	CreatedAt       time.Time
	UpdatedAt       time.Time
	FinishedAt      *time.Time
	CancelRequested bool
}

func (r row) record() Record {
	rec := Record{
		ID: r.ID, Queue: r.Queue, Kind: r.Kind, Payload: json.RawMessage(r.Payload),
		Priority: r.Priority, RunAt: r.RunAt, Attempts: r.Attempts, MaxAttempts: r.MaxAttempts,
		LockedUntil: r.LockedUntil, Status: Status(r.Status),
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, FinishedAt: r.FinishedAt,
		CancelRequested: r.CancelRequested,
	}
	if r.OrgID != nil {
		rec.OrgID = *r.OrgID
	}
	if r.LastError != nil {
		rec.LastError = *r.LastError
	}
	if r.LockedBy != nil {
		rec.LockedBy = *r.LockedBy
	}
	if r.UniqueKey != nil {
		rec.UniqueKey = *r.UniqueKey
	}
	if r.Schedule != nil {
		rec.Schedule = *r.Schedule
	}
	return rec
}

// columns — SELECT/RETURNING-ийн баганууд (prefix нь хүснэгтийн alias, "" эсвэл "j.").
func columns(prefix string) string {
	cols := []string{"id", "queue", "kind", "payload", "priority", "run_at", "attempts", "max_attempts",
		"last_error", "locked_by", "locked_until", "status", "unique_key", "schedule",
		"created_at", "updated_at", "finished_at", "cancel_requested"}
	for i, c := range cols {
		cols[i] = prefix + c
	}
	return prefix + "org_id::text AS org_id, " + strings.Join(cols, ", ")
}

func (j Job) validate() error {
	if len(j.Kind) > maxKindLen || !kindRe.MatchString(j.Kind) {
		return fmt.Errorf("%w: %q", ErrInvalidKind, j.Kind)
	}
	if j.Queue != "" && !queueRe.MatchString(j.Queue) {
		return fmt.Errorf("%w: %q", ErrInvalidQueue, j.Queue)
	}
	if j.OrgID != "" && !uuidRe.MatchString(j.OrgID) {
		return fmt.Errorf("%w: %q", ErrInvalidOrg, j.OrgID)
	}
	if j.MaxAttempts < 0 {
		return errors.New("jobs: MaxAttempts сөрөг байж болохгүй")
	}
	if j.Delay < 0 {
		return errors.New("jobs: Delay сөрөг байж болохгүй")
	}
	return nil
}

func inTx(tx *gorm.DB) bool {
	if tx == nil || tx.Statement == nil {
		return false
	}
	_, ok := tx.Statement.ConnPool.(gorm.TxCommitter)
	return ok
}

// Enqueue — job-ийг caller-ийн транзакцад бичиж id-г буцаана. tx нь Transaction /
// db.Tx доторх *gorm.DB байх ёстой (үгүй бол ErrNotInTx) — ингэснээр job
// бизнес өгөгдөлтэй атомар commit/rollback болно.
//
// UniqueKey тавигдсан бөгөөд ижил түлхүүртэй pending/running job байвал шинийг
// бичихгүй, байгаа job-ийн id-г буцаана (алдаа БИШ; үүнийг ялгах бол EnqueueUnique).
func Enqueue(ctx context.Context, tx *gorm.DB, j Job) (int64, error) {
	id, _, err := enqueue(ctx, tx, j, "")
	return id, err
}

// EnqueueUnique — Enqueue, гэхдээ UniqueKey заавал бөгөөд шинээр үүссэн (created=true)
// эсвэл давхардсан (created=false, id = байгаа идэвхтэй job)-ыг ялгаж буцаана.
func EnqueueUnique(ctx context.Context, tx *gorm.DB, j Job) (id int64, created bool, err error) {
	if j.UniqueKey == "" {
		return 0, false, ErrNoUniqueKey
	}
	return enqueue(ctx, tx, j, "")
}

func enqueue(ctx context.Context, tx *gorm.DB, j Job, schedule string) (int64, bool, error) {
	if tx == nil {
		return 0, false, errors.New("jobs: *gorm.DB nil")
	}
	if err := j.validate(); err != nil {
		return 0, false, err
	}
	if !inTx(tx) {
		return 0, false, ErrNotInTx
	}
	tx = tx.WithContext(ctx)
	payload, err := json.Marshal(j.Payload)
	if err != nil {
		return 0, false, fmt.Errorf("jobs: payload JSON болгож чадсангүй: %w", err)
	}
	queue := j.Queue
	if queue == "" {
		queue = DefaultQueue
	}
	maxAttempts := j.MaxAttempts
	if maxAttempts == 0 {
		maxAttempts = DefaultMaxAttempts
	}
	var runAt *time.Time
	if !j.RunAt.IsZero() {
		runAt = &j.RunAt
	}

	const ins = `INSERT INTO ` + table + `
		(queue, kind, org_id, payload, priority, run_at, max_attempts, unique_key, schedule)
		VALUES (?, ?, NULLIF(?, '')::uuid, ?::jsonb, ?,
			COALESCE(?::timestamptz, now() + make_interval(secs => ?)), ?, NULLIF(?, ''), NULLIF(?, ''))
		ON CONFLICT (queue, kind, COALESCE(org_id, '00000000-0000-0000-0000-000000000000'::uuid), unique_key) WHERE unique_key IS NOT NULL AND status IN ('pending','running')
		DO NOTHING
		RETURNING id`

	// Давхардал үед ON CONFLICT DO NOTHING нь tx-ийг abort хийхгүй. Байгаа job
	// SELECT-ийн хооронд дуусчих уралдаан гарвал INSERT-ийг дахин оролдоно.
	for attempt := 0; attempt < 3; attempt++ {
		var ids []int64
		err := tx.Raw(ins, queue, j.Kind, j.OrgID, string(payload), j.Priority,
			runAt, j.Delay.Seconds(), maxAttempts, j.UniqueKey, schedule).Scan(&ids).Error
		if err != nil {
			return 0, false, fmt.Errorf("jobs: %s бичиж чадсангүй: %w", j.Kind, err)
		}
		if len(ids) == 1 {
			return ids[0], true, nil
		}
		if j.UniqueKey == "" {
			return 0, false, fmt.Errorf("jobs: %s бичигдсэнгүй", j.Kind)
		}
		var existing []int64
		err = tx.Raw(`SELECT id FROM `+table+`
			WHERE queue = ? AND kind = ? AND unique_key = ? AND status IN ('pending','running')
				AND COALESCE(org_id, '00000000-0000-0000-0000-000000000000'::uuid)
					= COALESCE(NULLIF(?, '')::uuid, '00000000-0000-0000-0000-000000000000'::uuid)`,
			queue, j.Kind, j.UniqueKey, j.OrgID).Scan(&existing).Error
		if err != nil {
			return 0, false, fmt.Errorf("jobs: давхардсан job хайх: %w", err)
		}
		if len(existing) > 0 {
			return existing[0], false, nil
		}
	}
	return 0, false, fmt.Errorf("jobs: %s unique_key=%q — бичих/хайх уралдаан", j.Kind, j.UniqueKey)
}

// truncateErr — алдааны текстийг DB-д хадгалахаар бэлтгэнэ: NUL-ийг хасч, UTF-8-ыг
// таслахгүйгээр maxErrLen байтад багтаана.
func truncateErr(s string) string {
	s = strings.ReplaceAll(s, "\x00", "")
	s = strings.ToValidUTF8(s, "�")
	if len(s) <= maxErrLen {
		return s
	}
	cut := maxErrLen
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

func isUniqueViolation(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "23505"
}
