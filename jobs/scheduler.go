package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sync"
	"time"

	"github.com/khan-vision/khan/obs"
	"gorm.io/gorm"
)

// MissedPolicy — scheduler унтарсан/удаан гацсаны улмаас өнгөрсөн tick-үүдийг яах.
type MissedPolicy int

const (
	// MissedSkip — Grace-ээс хуучирсан tick-ийг алгасна (анхдагч). Жишээ нь өдөр
	// бүрийн 03:00 job-ийг 09:00 болтол сервер унтарсан бол тэр өдөр ажиллахгүй.
	MissedSkip MissedPolicy = iota
	// MissedCatchUp — өнгөрсөн tick-үүдээс хамгийн сүүлийн MaxCatchUp-ийг нөхөж
	// enqueue хийнэ (хуучин нь алгасагдана).
	MissedCatchUp
)

// Анхдагч утгууд.
const (
	DefaultCheckInterval = 5 * time.Second
	DefaultGrace         = time.Minute
	DefaultLookback      = 24 * time.Hour
	DefaultMaxCatchUp    = 5
)

var scheduleNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_.:\-]{0,127}$`)

// Schedule — нэг cron хуваарь.
type Schedule struct {
	// Name — давтагдахгүй, тогтвортой нэр (tick бүрийн dedupe түлхүүр). Нэрийг
	// өөрчилбөл шинэ хуваарь болно.
	Name string
	// Spec — "*/5 * * * *", "0 9 * * MON-FRI", "@hourly", "@every 10m" (ParseSpec-ийг үз).
	Spec string
	// TZ — цагийн бүс; хоосон бол DefaultTZ (Asia/Ulaanbaatar). @every-д нөлөөгүй.
	TZ string

	// Kind, Payload, Queue, Priority, MaxAttempts, OrgID — үүсэх job-ийн талбарууд (Job-ийг үз).
	Kind        string
	Payload     any
	Queue       string
	OrgID       string
	Priority    int
	MaxAttempts int

	// Missed — өнгөрсөн tick-ийн бодлого (анхдагч MissedSkip).
	Missed MissedPolicy
	// MaxCatchUp — MissedCatchUp үед нөхөх дээд tick (0 → DefaultMaxCatchUp).
	MaxCatchUp int
	// SkipIfRunning — өмнөх tick-ийн job pending/running хэвээр байвал энэ tick-ийн
	// job үүсгэхгүй (давхцан ажиллахаас сэргийлнэ).
	SkipIfRunning bool
}

type parsedSchedule struct {
	Schedule
	spec Spec
}

// Scheduler — cron хуваарийг job болгон enqueue хийнэ.
//
// Leader сонгох шаардлагагүй: instance бүр ажиллаж, tick бүрийн мөрийг
// `khan.job_schedule_runs (name, scheduled_for)` PRIMARY KEY-д INSERT хийнэ.
// Яг нэг instance л амжилттай (ижил транзакцдаа job-ийг үүсгэнэ); бусад нь
// ON CONFLICT-оор чимээгүй алгасна. Tick-ийн цаг нь spec-ээс тооцогдох тул
// instance-уудын эхэлсэн цаг/цагийн зөрүү (clock skew) үр дүнд нөлөөлөхгүй.
type Scheduler struct {
	DB        *gorm.DB
	Schedules []Schedule
	// CheckInterval — шалгах давтамж; 0 → DefaultCheckInterval (tick-ийн хоцрох дээд хугацаа).
	CheckInterval time.Duration
	// Grace — tick-ийг "цагтаа" гэж тооцох хугацаа; үүнээс хуучин нь өнгөрсөн
	// (Missed бодлогоор шийдэгдэнэ). 0 → DefaultGrace.
	Grace time.Duration
	// Lookback — өнгөрсөн tick хайх дээд хугацаа; 0 → DefaultLookback.
	Lookback time.Duration
	// Now — тест/инжекц; nil бол time.Now.
	Now func() time.Time
	// Logger — nil бол obs.From(ctx).
	Logger *slog.Logger

	mu     sync.Mutex
	parsed []parsedSchedule
	cursor map[string]time.Time
}

func (s *Scheduler) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Scheduler) log(ctx context.Context) *slog.Logger {
	if s.Logger != nil {
		return s.Logger
	}
	return obs.From(ctx)
}

// Validate — бүх хуваарийг шалгана (нэр, spec, kind, queue, org). Run/Tick
// эхний удаа дуудахдаа өөрөө шалгадаг.
func (s *Scheduler) Validate() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.prepareLocked()
}

func (s *Scheduler) prepareLocked() error {
	if s.parsed != nil {
		return nil
	}
	if s.DB == nil {
		return errors.New("jobs: Scheduler.DB nil")
	}
	seen := map[string]bool{}
	out := make([]parsedSchedule, 0, len(s.Schedules))
	for _, sc := range s.Schedules {
		if !scheduleNameRe.MatchString(sc.Name) {
			return fmt.Errorf("jobs: schedule нэр буруу: %q", sc.Name)
		}
		if seen[sc.Name] {
			return fmt.Errorf("jobs: schedule нэр давхардсан: %q", sc.Name)
		}
		seen[sc.Name] = true
		if err := (Job{Queue: sc.Queue, Kind: sc.Kind, OrgID: sc.OrgID, MaxAttempts: sc.MaxAttempts}).validate(); err != nil {
			return fmt.Errorf("jobs: schedule %q: %w", sc.Name, err)
		}
		if sc.Missed != MissedSkip && sc.Missed != MissedCatchUp {
			return fmt.Errorf("jobs: schedule %q: Missed бодлого буруу", sc.Name)
		}
		if sc.MaxCatchUp < 0 {
			return fmt.Errorf("jobs: schedule %q: MaxCatchUp сөрөг", sc.Name)
		}
		spec, err := ParseSpec(sc.Spec, sc.TZ)
		if err != nil {
			return fmt.Errorf("jobs: schedule %q: %w", sc.Name, err)
		}
		out = append(out, parsedSchedule{Schedule: sc, spec: spec})
	}
	s.parsed = out
	s.cursor = map[string]time.Time{}
	return nil
}

func (s *Scheduler) grace() time.Duration {
	if s.Grace > 0 {
		return s.Grace
	}
	return DefaultGrace
}

func (s *Scheduler) lookback() time.Duration {
	if s.Lookback > 0 {
		return s.Lookback
	}
	return DefaultLookback
}

// Run — ctx дуустал CheckInterval тутам Tick хийнэ. DB-ийн алдааг логлоод
// үргэлжилнэ. Хуваарь буруу бол шууд алдаа буцаана; ctx дуусахад nil.
func (s *Scheduler) Run(ctx context.Context) error {
	if err := s.Validate(); err != nil {
		return err
	}
	every := s.CheckInterval
	if every <= 0 {
		every = DefaultCheckInterval
	}
	log := s.log(ctx)
	log.Info("jobs: scheduler эхэллээ", "schedules", len(s.Schedules), "check_interval", every)
	for ctx.Err() == nil {
		if _, err := s.Tick(ctx); err != nil && ctx.Err() == nil {
			log.Error("jobs: scheduler tick алдаа", "error", err)
		}
		t := time.NewTimer(every)
		select {
		case <-ctx.Done():
		case <-t.C:
		}
		t.Stop()
	}
	return nil
}

// Tick — одоогоос (Now) өмнөх хүлээгдэж буй tick бүрийг enqueue хийнэ. Энэ
// дуудлагад шинээр үүссэн job-ийн тоог буцаана (өөр instance аль хэдийн үүсгэсэн
// tick тоонд орохгүй). Нэг хуваарийн алдаа бусдыг зогсоохгүй; алдаануудыг нэгтгэж буцаана.
func (s *Scheduler) Tick(ctx context.Context) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.prepareLocked(); err != nil {
		return 0, err
	}
	now := s.now()
	created := 0
	var errs []error
	for _, ps := range s.parsed {
		n, err := s.tickOne(ctx, ps, now)
		created += n
		if err != nil {
			errs = append(errs, fmt.Errorf("jobs: schedule %q: %w", ps.Name, err))
		}
	}
	return created, errors.Join(errs...)
}

// dueTicks — (from, now] дахь tick-үүд; өнгөрсөн бодлогоор шүүгдэнэ.
func (s *Scheduler) dueTicks(ps parsedSchedule, from, now time.Time) []time.Time {
	if min := now.Add(-s.lookback()); from.Before(min) {
		from = min
	}
	var missed, onTime []time.Time
	cutoff := now.Add(-s.grace())
	for t := ps.spec.Next(from); !t.IsZero() && !t.After(now); t = ps.spec.Next(t) {
		if t.Before(cutoff) {
			missed = append(missed, t)
		} else {
			onTime = append(onTime, t)
		}
	}
	if ps.Missed != MissedCatchUp {
		return onTime
	}
	max := ps.MaxCatchUp
	if max == 0 {
		max = DefaultMaxCatchUp
	}
	if len(missed) > max {
		missed = missed[len(missed)-max:]
	}
	return append(missed, onTime...)
}

func (s *Scheduler) tickOne(ctx context.Context, ps parsedSchedule, now time.Time) (int, error) {
	from, ok := s.cursor[ps.Name]
	if !ok {
		var last struct{ Last *time.Time }
		err := s.DB.WithContext(ctx).Raw(`SELECT max(scheduled_for) AS last FROM `+runsTable+` WHERE name = ?`, ps.Name).Scan(&last).Error
		if err != nil {
			return 0, fmt.Errorf("сүүлийн tick унших: %w", err)
		}
		from = now // түүх байхгүй: эхний удаа — зөвхөн ирээдүйн tick
		if last.Last != nil {
			from = *last.Last
		}
	}
	created := 0
	for _, tick := range s.dueTicks(ps, from, now) {
		ok, err := s.fire(ctx, ps, tick)
		if err != nil {
			// cursor-ийг ахиулахгүй: дараагийн Tick дахин оролдоно (PK нь давхардлаас хамгаална).
			return created, fmt.Errorf("tick %s: %w", tick.Format(time.RFC3339), err)
		}
		if ok {
			created++
		}
	}
	s.cursor[ps.Name] = now
	return created, nil
}

// fire — tick-ийг нэг транзакцад "эзэмшиж" (PK), job-ийг үүсгэнэ. created=false
// бол өөр instance аль хэдийн эзэмшсэн (эсвэл SkipIfRunning-ээр алгассан).
func (s *Scheduler) fire(ctx context.Context, ps parsedSchedule, tick time.Time) (created bool, err error) {
	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Exec(`INSERT INTO `+runsTable+` (name, scheduled_for) VALUES (?, ?)
			ON CONFLICT (name, scheduled_for) DO NOTHING`, ps.Name, tick.UTC())
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return nil // өөр instance авсан
		}
		if ps.SkipIfRunning {
			var active []int64
			if err := tx.Raw(`SELECT id FROM `+table+`
				WHERE schedule = ? AND status IN ('pending','running') LIMIT 1`, ps.Name).Scan(&active).Error; err != nil {
				return err
			}
			if len(active) > 0 {
				s.log(ctx).Warn("jobs: өмнөх ажил дуусаагүй тул tick алгасав", "schedule", ps.Name, "tick", tick, "active_job", active[0])
				return tx.Exec(`UPDATE `+runsTable+` SET note = ? WHERE name = ? AND scheduled_for = ?`,
					fmt.Sprintf("алгассан: өмнөх job #%d идэвхтэй", active[0]), ps.Name, tick.UTC()).Error
			}
		}
		id, _, err := enqueue(ctx, tx, Job{
			Queue: ps.Queue, Kind: ps.Kind, OrgID: ps.OrgID, Payload: ps.Payload,
			Priority: ps.Priority, MaxAttempts: ps.MaxAttempts,
			RunAt:     tick,
			UniqueKey: fmt.Sprintf("schedule:%s:%d", ps.Name, tick.Unix()),
		}, ps.Name)
		if err != nil {
			return err
		}
		if err := tx.Exec(`UPDATE `+runsTable+` SET job_id = ? WHERE name = ? AND scheduled_for = ?`,
			id, ps.Name, tick.UTC()).Error; err != nil {
			return err
		}
		created = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return created, nil
}
