package jobs

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// Scope — admin helper-ийн харагдах хүрээ. Тэг утга ХҮЧИНГҮЙ (ErrScope): HTTP
// handler-ээс хоосон org_id дамжиж бүх байгууллагын job харагдахаас сэргийлнэ.
type Scope struct {
	orgID  string
	system bool
}

// OrgScope — зөвхөн тухайн байгууллагын (org_id = orgID) job. Системийн
// (org_id NULL) болон бусад байгууллагын job харагдахгүй.
func OrgScope(orgID string) Scope { return Scope{orgID: orgID} }

// SystemScope — бүх job (бүх байгууллага + системийн). Зөвхөн оператор/системийн
// хэрэглээнд; HTTP хүсэлтээс дуудахгүй.
func SystemScope() Scope { return Scope{system: true} }

func (s Scope) apply(q *gorm.DB) (*gorm.DB, error) {
	switch {
	case s.system:
		return q, nil
	case s.orgID == "":
		return nil, ErrScope
	case !uuidRe.MatchString(s.orgID):
		return nil, fmt.Errorf("%w: %q", ErrInvalidOrg, s.orgID)
	}
	return q.Where("org_id = ?::uuid", s.orgID), nil
}

// ListFilter — List-ийн шүүлтүүр. Хоосон талбар үл тооно.
type ListFilter struct {
	Queue    string
	Kind     string
	Statuses []Status
	Schedule string
	// Limit — 0 → 50, дээд 1000.
	Limit  int
	Offset int
}

// List — шинэ нь түрүүлж (id DESC). Scope-оос гадуурх мөр харагдахгүй.
func List(ctx context.Context, db *gorm.DB, scope Scope, f ListFilter) ([]Record, error) {
	if db == nil {
		return nil, errors.New("jobs: *gorm.DB nil")
	}
	q, err := scope.apply(db.WithContext(ctx).Table(table))
	if err != nil {
		return nil, err
	}
	if f.Queue != "" {
		q = q.Where("queue = ?", f.Queue)
	}
	if f.Kind != "" {
		q = q.Where("kind = ?", f.Kind)
	}
	if f.Schedule != "" {
		q = q.Where("schedule = ?", f.Schedule)
	}
	if len(f.Statuses) > 0 {
		ss := make([]string, 0, len(f.Statuses))
		for _, s := range f.Statuses {
			if !s.valid() {
				return nil, fmt.Errorf("jobs: төлөв буруу: %q", s)
			}
			ss = append(ss, string(s))
		}
		q = q.Where("status IN ?", ss)
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 1000 {
		limit = 1000
	}
	if f.Offset < 0 {
		return nil, errors.New("jobs: Offset сөрөг")
	}
	var rows []row
	if err := q.Select(columns("")).Order("id DESC").Limit(limit).Offset(f.Offset).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("jobs: list: %w", err)
	}
	out := make([]Record, len(rows))
	for i, r := range rows {
		out[i] = r.record()
	}
	return out, nil
}

// Get — нэг job. Scope-оос гадуур бол ErrNotFound.
func Get(ctx context.Context, db *gorm.DB, scope Scope, id int64) (Record, error) {
	if db == nil {
		return Record{}, errors.New("jobs: *gorm.DB nil")
	}
	q, err := scope.apply(db.WithContext(ctx).Table(table))
	if err != nil {
		return Record{}, err
	}
	var rows []row
	if err := q.Select(columns("")).Where("id = ?", id).Scan(&rows).Error; err != nil {
		return Record{}, fmt.Errorf("jobs: get: %w", err)
	}
	if len(rows) == 0 {
		return Record{}, ErrNotFound
	}
	return rows[0].record(), nil
}

// Retry — dead (эсвэл canceled) job-ийг шууд дахин оролдохоор pending болгоно
// (оролдлогын тоо 0-ээс; cancel_requested арилна). Бусад төлөвт ErrState; ижил unique_key-тэй идэвхтэй
// job байвал ErrDuplicate.
func Retry(ctx context.Context, db *gorm.DB, scope Scope, id int64) error {
	if db == nil {
		return errors.New("jobs: *gorm.DB nil")
	}
	q, err := scope.apply(db.WithContext(ctx).Table(table))
	if err != nil {
		return err
	}
	res := q.Where("id = ? AND status IN ('dead','canceled')", id).Updates(map[string]any{
		"status":           string(StatusPending),
		"attempts":         0,
		"run_at":           gorm.Expr("now()"),
		"locked_by":        nil,
		"locked_until":     nil,
		"finished_at":      nil,
		"updated_at":       gorm.Expr("now()"),
		"cancel_requested": false,
	})
	if res.Error != nil {
		if isUniqueViolation(res.Error) {
			return ErrDuplicate
		}
		return fmt.Errorf("jobs: retry: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return stateOrNotFound(ctx, db, scope, id)
	}
	return nil
}

// Cancel — pending job-ийг (мөн lease нь дууссан running-ийг) шууд canceled болгоно.
// Идэвхтэй running job-д `cancel_requested` тэмдэг тавина: төлөв running хэвээр,
// unique_key эзлэгдсэн хэвээр (хуучин handler ажиллаж байхад ижил түлхүүрээр шинэ
// job үүсэхгүй). Worker дараагийн heartbeat-д (≤ Lease/3) handler-ийн ctx-ийг
// ErrCancelRequested-оор цуцалж, зогсмогц job-ийг `canceled` болгоно. Дууссан
// (succeeded/dead/canceled) job-д ErrState; хүсэлт аль хэдийн тавигдсан бол nil.
func Cancel(ctx context.Context, db *gorm.DB, scope Scope, id int64) error {
	if db == nil {
		return errors.New("jobs: *gorm.DB nil")
	}
	q, err := scope.apply(db.WithContext(ctx).Table(table))
	if err != nil {
		return err
	}
	res := q.Where(`id = ? AND (status = 'pending' OR (status = 'running' AND locked_until < now()))`, id).Updates(map[string]any{
		"status":       string(StatusCanceled),
		"locked_by":    nil,
		"locked_until": nil,
		"finished_at":  gorm.Expr("now()"),
		"updated_at":   gorm.Expr("now()"),
	})
	if res.Error != nil {
		return fmt.Errorf("jobs: cancel: %w", res.Error)
	}
	if res.RowsAffected > 0 {
		return nil
	}
	// Идэвхтэй lease-тэй running: зөвхөн тэмдэг тавина.
	q, err = scope.apply(db.WithContext(ctx).Table(table))
	if err != nil {
		return err
	}
	res = q.Where("id = ? AND status = 'running'", id).Updates(map[string]any{
		"cancel_requested": true,
		"updated_at":       gorm.Expr("now()"),
	})
	if res.Error != nil {
		return fmt.Errorf("jobs: cancel: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return stateOrNotFound(ctx, db, scope, id)
	}
	return nil
}

func stateOrNotFound(ctx context.Context, db *gorm.DB, scope Scope, id int64) error {
	rec, err := Get(ctx, db, scope, id)
	if err != nil {
		return err // ErrNotFound орно
	}
	return fmt.Errorf("%w: #%d төлөв=%s", ErrState, id, rec.Status)
}

// QueueStat — нэг queue-ийн DB-ийн төлвийн тоо.
type QueueStat struct {
	Queue     string
	Pending   int64 // нийт pending (хойшлогдсон орно)
	Ready     int64 // pending бөгөөд run_at <= now (одоо авагдах боломжтой)
	Running   int64
	Succeeded int64
	Dead      int64
	Canceled  int64
	// OldestReadyAge — хамгийн удаан хүлээсэн бэлэн job-ийн нас (0 = байхгүй). Өсөөд
	// байвал worker дутуу/унасан гэсэн дохио.
	OldestReadyAge time.Duration
	// ExpiredLeases — lease нь дууссан running (worker унасан; reclaim хүлээж буй).
	ExpiredLeases int64
}

// QueueStats — queue тус бүрийн тоо (Scope-оор шүүгдэнэ), queue-ийн нэрээр эрэмбэлэгдсэн.
func QueueStats(ctx context.Context, db *gorm.DB, scope Scope) ([]QueueStat, error) {
	if db == nil {
		return nil, errors.New("jobs: *gorm.DB nil")
	}
	q, err := scope.apply(db.WithContext(ctx).Table(table))
	if err != nil {
		return nil, err
	}
	var rows []struct {
		Queue          string
		Pending, Ready int64
		Running        int64
		Succeeded      int64
		Dead           int64
		Canceled       int64
		OldestSecs     *float64
		ExpiredLeases  int64
	}
	err = q.Select(`queue,
		count(*) FILTER (WHERE status = 'pending') AS pending,
		count(*) FILTER (WHERE status = 'pending' AND run_at <= now()) AS ready,
		count(*) FILTER (WHERE status = 'running') AS running,
		count(*) FILTER (WHERE status = 'succeeded') AS succeeded,
		count(*) FILTER (WHERE status = 'dead') AS dead,
		count(*) FILTER (WHERE status = 'canceled') AS canceled,
		extract(epoch FROM now() - min(run_at) FILTER (WHERE status = 'pending' AND run_at <= now())) AS oldest_secs,
		count(*) FILTER (WHERE status = 'running' AND locked_until < now()) AS expired_leases`).
		Group("queue").Order("queue").Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("jobs: stats: %w", err)
	}
	out := make([]QueueStat, len(rows))
	for i, r := range rows {
		out[i] = QueueStat{Queue: r.Queue, Pending: r.Pending, Ready: r.Ready, Running: r.Running,
			Succeeded: r.Succeeded, Dead: r.Dead, Canceled: r.Canceled, ExpiredLeases: r.ExpiredLeases}
		if r.OldestSecs != nil {
			out[i].OldestReadyAge = time.Duration(*r.OldestSecs * float64(time.Second))
		}
	}
	return out, nil
}

// Purge — olderThan-аас өмнө дууссан (succeeded, canceled; statuses өгвөл тэдгээр)
// job-ийг устгана, мөн сүүлийн tick-ээс бусад хуучин schedule-run мөрийг цэвэрлэнэ
// (сүүлийн мөрийг үлдээнэ — scheduler түүгээр cursor сэргээдэг). dead-ийг
// АНХДАГЧААР хөдөлгөхгүй (шалгах хэрэгтэй). Устгасан job-ийн тоог буцаана.
// Системийн ажил: бүх байгууллагын мөрийг хамарна.
func Purge(ctx context.Context, db *gorm.DB, olderThan time.Duration, statuses ...Status) (int64, error) {
	if db == nil {
		return 0, errors.New("jobs: *gorm.DB nil")
	}
	if olderThan <= 0 {
		return 0, errors.New("jobs: olderThan эерэг байх ёстой")
	}
	if len(statuses) == 0 {
		statuses = []Status{StatusSucceeded, StatusCanceled}
	}
	ss := make([]string, 0, len(statuses))
	for _, s := range statuses {
		if !s.valid() || s == StatusPending || s == StatusRunning {
			return 0, fmt.Errorf("jobs: purge хийх боломжгүй төлөв: %q", s)
		}
		ss = append(ss, string(s))
	}
	secs := olderThan.Seconds()
	res := db.WithContext(ctx).Exec(`DELETE FROM `+table+`
		WHERE status IN ? AND finished_at < now() - make_interval(secs => ?)`, ss, secs)
	if res.Error != nil {
		return 0, fmt.Errorf("jobs: purge: %w", res.Error)
	}
	if err := db.WithContext(ctx).Exec(`DELETE FROM `+runsTable+` r
		WHERE r.scheduled_for < now() - make_interval(secs => ?)
		AND r.scheduled_for < (SELECT max(m.scheduled_for) FROM `+runsTable+` m WHERE m.name = r.name)`, secs).Error; err != nil {
		return res.RowsAffected, fmt.Errorf("jobs: purge schedule runs: %w", err)
	}
	return res.RowsAffected, nil
}
