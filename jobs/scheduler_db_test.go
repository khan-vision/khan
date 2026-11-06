package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"
)

func newScheduler(gdb *gorm.DB, now *atomic.Int64, scs ...Schedule) *Scheduler {
	return &Scheduler{
		DB: gdb, Schedules: scs, Logger: quiet,
		Now: func() time.Time { return time.Unix(0, now.Load()) },
	}
}

func setNow(now *atomic.Int64, t time.Time) { now.Store(t.UnixNano()) }

func compactJSON(b []byte) string {
	var buf bytes.Buffer
	if err := json.Compact(&buf, b); err != nil {
		return string(b)
	}
	return buf.String()
}

func scheduleJobs(t *testing.T, gdb *gorm.DB, name string) []Record {
	t.Helper()
	rs, err := List(context.Background(), gdb, SystemScope(), ListFilter{Schedule: name, Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	return rs
}

func countRuns(t *testing.T, gdb *gorm.DB, name string) int {
	t.Helper()
	var n int
	gdb.Raw(`SELECT count(*) FROM khan.job_schedule_runs WHERE name = ?`, name).Scan(&n)
	return n
}

// tickAll — n scheduler-ийг ЗЭРЭГ Tick хийлгэж, нийт үүссэн job-ийг буцаана.
func tickAll(t *testing.T, ss []*Scheduler) int {
	t.Helper()
	var total atomic.Int64
	var wg sync.WaitGroup
	for _, s := range ss {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n, err := s.Tick(context.Background())
			if err != nil {
				t.Error(err)
			}
			total.Add(int64(n))
		}()
	}
	wg.Wait()
	return int(total.Load())
}

func TestSchedulerThreeInstancesExactlyOnePerTick(t *testing.T) {
	gdb := testDB(t)
	q := testQueue(t, gdb)
	name := testSchedName(t, gdb)
	var now atomic.Int64
	sc := Schedule{Name: name, Spec: "*/5 * * * *", Kind: testKind, Queue: q, Payload: map[string]string{"a": "b"}, OrgID: orgA, Priority: 4, MaxAttempts: 2}
	var ss []*Scheduler
	for i := 0; i < 3; i++ {
		ss = append(ss, newScheduler(gdb, &now, sc))
	}

	t0 := at(t, "2026-11-06 12:00:30", ub)
	setNow(&now, t0)
	// Түүх байхгүй: эхний Tick зөвхөн cursor тавина.
	if n := tickAll(t, ss); n != 0 {
		t.Fatalf("эхний tick %d job үүсгэв", n)
	}
	for i, tick := range []string{"12:05:00", "12:10:00", "12:15:00"} {
		setNow(&now, at(t, "2026-11-06 "+tick, ub).Add(7*time.Second))
		if n := tickAll(t, ss); n != 1 {
			t.Fatalf("tick %s: үүссэн=%d", tick, n)
		}
		if got := len(scheduleJobs(t, gdb, name)); got != i+1 {
			t.Fatalf("tick %s: нийт job=%d", tick, got)
		}
		// Дахин Tick хийхэд (бүгд) нэмэлт job үүсэхгүй.
		if n := tickAll(t, ss); n != 0 {
			t.Fatalf("давхар tick %d", n)
		}
	}
	jobs := scheduleJobs(t, gdb, name)
	if countRuns(t, gdb, name) != 3 || len(jobs) != 3 {
		t.Fatalf("runs=%d jobs=%d", countRuns(t, gdb, name), len(jobs))
	}
	j := jobs[2] // хамгийн эртний (id DESC)
	if want := at(t, "2026-11-06 12:05:00", ub); !j.RunAt.Equal(want) {
		t.Fatalf("run_at=%s want %s", j.RunAt, want)
	}
	if j.Schedule != name || j.Queue != q || j.OrgID != orgA || j.Priority != 4 || j.MaxAttempts != 2 ||
		compactJSON(j.Payload) != `{"a":"b"}` || !strings.HasPrefix(j.UniqueKey, "schedule:"+name+":") {
		t.Fatalf("job: %+v", j)
	}

	// Шинэ instance (restart) DB-ээс cursor сэргээж, давхар үүсгэхгүй.
	fresh := newScheduler(gdb, &now, sc)
	if n, err := fresh.Tick(context.Background()); err != nil || n != 0 {
		t.Fatalf("restart: n=%d err=%v", n, err)
	}
}

func TestSchedulerMissedSkip(t *testing.T) {
	gdb := testDB(t)
	q := testQueue(t, gdb)
	name := testSchedName(t, gdb)
	var now atomic.Int64
	sc := Schedule{Name: name, Spec: "0 * * * *", Kind: testKind, Queue: q}
	gdb.Exec(`INSERT INTO khan.job_schedule_runs (name, scheduled_for) VALUES (?, ?)`, name, at(t, "2026-11-06 12:00:00", ub))

	s := newScheduler(gdb, &now, sc)
	setNow(&now, at(t, "2026-11-06 15:00:10", ub)) // 13:00, 14:00 өнгөрсөн; 15:00 цагтаа
	n, err := s.Tick(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	jobs := scheduleJobs(t, gdb, name)
	if len(jobs) != 1 || !jobs[0].RunAt.Equal(at(t, "2026-11-06 15:00:00", ub)) {
		t.Fatalf("%+v", jobs)
	}

	// 15:00 tick Grace-ээс хоцорвол (ж: DB удаан) бас алгасагдана.
	s2 := newScheduler(gdb, &now, Schedule{Name: name + "x", Spec: "0 * * * *", Kind: testKind, Queue: q})
	t.Cleanup(func() { gdb.Exec(`DELETE FROM khan.job_schedule_runs WHERE name = ?`, name+"x") })
	gdb.Exec(`INSERT INTO khan.job_schedule_runs (name, scheduled_for) VALUES (?, ?)`, name+"x", at(t, "2026-11-06 12:00:00", ub))
	setNow(&now, at(t, "2026-11-06 15:30:00", ub))
	if n, err := s2.Tick(context.Background()); err != nil || n != 0 {
		t.Fatalf("n=%d err=%v", n, err)
	}
}

func TestSchedulerMissedCatchUp(t *testing.T) {
	gdb := testDB(t)
	q := testQueue(t, gdb)
	name := testSchedName(t, gdb)
	var now atomic.Int64
	sc := Schedule{Name: name, Spec: "0 * * * *", Kind: testKind, Queue: q, Missed: MissedCatchUp, MaxCatchUp: 2}
	gdb.Exec(`INSERT INTO khan.job_schedule_runs (name, scheduled_for) VALUES (?, ?)`, name, at(t, "2026-11-06 09:00:00", ub))

	setNow(&now, at(t, "2026-11-06 15:00:10", ub))
	// Үүсэх: сүүлийн 2 өнгөрсөн (13:00, 14:00) + цагтаа 15:00. 10:00-12:00 нь хязгаараас хэтэрсэн.
	ss := []*Scheduler{newScheduler(gdb, &now, sc), newScheduler(gdb, &now, sc)}
	if n := tickAll(t, ss); n != 3 {
		t.Fatalf("n=%d", n)
	}
	var runAts []string
	jobs := scheduleJobs(t, gdb, name)
	for i := len(jobs) - 1; i >= 0; i-- {
		runAts = append(runAts, jobs[i].RunAt.In(ub).Format("15:04"))
	}
	if strings.Join(runAts, ",") != "13:00,14:00,15:00" {
		t.Fatalf("run_at: %v", runAts)
	}
	if n := tickAll(t, ss); n != 0 {
		t.Fatalf("давтан %d", n)
	}

	// Анхдагч MaxCatchUp (5).
	name2 := testSchedName(t, gdb)
	gdb.Exec(`INSERT INTO khan.job_schedule_runs (name, scheduled_for) VALUES (?, ?)`, name2, at(t, "2026-11-06 01:00:00", ub))
	s := newScheduler(gdb, &now, Schedule{Name: name2, Spec: "0 * * * *", Kind: testKind, Queue: q, Missed: MissedCatchUp})
	if n, err := s.Tick(context.Background()); err != nil || n != DefaultMaxCatchUp+1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
}

func TestSchedulerLookbackBoundsCatchUp(t *testing.T) {
	gdb := testDB(t)
	q := testQueue(t, gdb)
	name := testSchedName(t, gdb)
	var now atomic.Int64
	gdb.Exec(`INSERT INTO khan.job_schedule_runs (name, scheduled_for) VALUES (?, ?)`, name, at(t, "2026-01-01 00:00:00", ub))
	s := newScheduler(gdb, &now, Schedule{Name: name, Spec: "* * * * *", Kind: testKind, Queue: q, Missed: MissedCatchUp, MaxCatchUp: 3})
	s.Lookback = time.Hour
	setNow(&now, at(t, "2026-11-06 15:00:10", ub))
	// Lookback дотор 14:01..15:00; 14:57-14:59 нь нөхөгдөх 3 өнгөрсөн + 15:00 цагтаа.
	n, err := s.Tick(context.Background())
	if err != nil || n != 4 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	jobs := scheduleJobs(t, gdb, name)
	oldest := jobs[len(jobs)-1].RunAt
	if !oldest.Equal(at(t, "2026-11-06 14:57:00", ub)) {
		t.Fatalf("oldest=%s", oldest)
	}
}

func TestSchedulerSkipIfRunning(t *testing.T) {
	gdb := testDB(t)
	q := testQueue(t, gdb)
	name := testSchedName(t, gdb)
	var now atomic.Int64
	s := newScheduler(gdb, &now, Schedule{Name: name, Spec: "*/5 * * * *", Kind: testKind, Queue: q, SkipIfRunning: true})

	setNow(&now, at(t, "2026-11-06 12:00:30", ub))
	s.Tick(context.Background())
	setNow(&now, at(t, "2026-11-06 12:05:05", ub))
	if n, _ := s.Tick(context.Background()); n != 1 {
		t.Fatalf("n=%d", n)
	}
	// Өмнөх job pending хэвээр → дараагийн tick алгасагдана.
	setNow(&now, at(t, "2026-11-06 12:10:05", ub))
	if n, _ := s.Tick(context.Background()); n != 0 {
		t.Fatalf("overlap: n=%d", n)
	}
	if len(scheduleJobs(t, gdb, name)) != 1 || countRuns(t, gdb, name) != 2 {
		t.Fatalf("jobs=%d runs=%d", len(scheduleJobs(t, gdb, name)), countRuns(t, gdb, name))
	}
	var note string
	gdb.Raw(`SELECT note FROM khan.job_schedule_runs WHERE name = ? AND job_id IS NULL`, name).Scan(&note)
	if !strings.Contains(note, "алгассан") {
		t.Fatalf("note=%q", note)
	}
	// Дууссаны дараа дахин үүснэ.
	w := testWorker(gdb, q)
	w.Register(testKind, func(context.Context, Record) error { return nil })
	w.RunOnce(context.Background())
	setNow(&now, at(t, "2026-11-06 12:15:05", ub))
	if n, _ := s.Tick(context.Background()); n != 1 {
		t.Fatalf("дараа: n=%d", n)
	}
}

func TestSchedulerValidate(t *testing.T) {
	gdb := testDB(t)
	ok := Schedule{Name: "a", Spec: "* * * * *", Kind: "x.y"}
	cases := map[string][]Schedule{
		"давхар нэр":    {ok, ok},
		"буруу нэр":     {{Name: "Bad Name", Spec: "* * * * *", Kind: "x.y"}},
		"хоосон нэр":    {{Spec: "* * * * *", Kind: "x.y"}},
		"буруу spec":    {{Name: "a", Spec: "nope", Kind: "x.y"}},
		"буруу kind":    {{Name: "a", Spec: "* * * * *", Kind: "Bad"}},
		"буруу tz":      {{Name: "a", Spec: "* * * * *", Kind: "x.y", TZ: "Mars/Base"}},
		"буруу org":     {{Name: "a", Spec: "* * * * *", Kind: "x.y", OrgID: "z"}},
		"буруу policy":  {{Name: "a", Spec: "* * * * *", Kind: "x.y", Missed: 9}},
		"сөрөг catchup": {{Name: "a", Spec: "* * * * *", Kind: "x.y", MaxCatchUp: -1}},
	}
	for name, scs := range cases {
		s := &Scheduler{DB: gdb, Schedules: scs}
		if err := s.Validate(); err == nil {
			t.Errorf("%s: алдаагүй", name)
		}
		if _, err := s.Tick(context.Background()); err == nil {
			t.Errorf("%s: Tick алдаагүй", name)
		}
		if err := s.Run(context.Background()); err == nil {
			t.Errorf("%s: Run алдаагүй", name)
		}
	}
	if err := (&Scheduler{DB: gdb, Schedules: []Schedule{ok}}).Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (&Scheduler{Schedules: []Schedule{ok}}).Validate(); err == nil {
		t.Error("DB nil")
	}
}

func TestSchedulerTickErrorIsolation(t *testing.T) {
	gdb := testDB(t)
	q := testQueue(t, gdb)
	good := testSchedName(t, gdb)
	bad := testSchedName(t, gdb)
	var now atomic.Int64
	s := newScheduler(gdb, &now,
		Schedule{Name: bad, Spec: "* * * * *", Kind: testKind, Queue: q, Payload: make(chan int)}, // JSON болохгүй
		Schedule{Name: good, Spec: "* * * * *", Kind: testKind, Queue: q})
	setNow(&now, at(t, "2026-11-06 12:00:00", ub))
	s.Tick(context.Background())
	setNow(&now, at(t, "2026-11-06 12:01:05", ub))
	n, err := s.Tick(context.Background())
	if err == nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if len(scheduleJobs(t, gdb, good)) != 1 || len(scheduleJobs(t, gdb, bad)) != 0 || countRuns(t, gdb, bad) != 0 {
		t.Fatal("алдаатай хуваарь tick-ийг эзэмшсэн / сайныг зогсоосон")
	}
	// Алдаа cursor-ийг ахиулаагүй тул дараагийн Tick дахин оролдоно (rollback хийгдсэн).
	n, err = s.Tick(context.Background())
	if err == nil || n != 0 {
		t.Fatalf("retry: n=%d err=%v", n, err)
	}
}

// Бодит цаг + 3 scheduler + worker: @every 1s → tick бүрт яг нэг job, яг нэг удаа ажиллана.
func TestSchedulerEndToEndThreeInstances(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	gdb := testDB(t)
	q := testQueue(t, gdb)
	name := testSchedName(t, gdb)
	sc := Schedule{Name: name, Spec: "@every 1s", Kind: testKind, Queue: q}

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		s := &Scheduler{DB: gdb, Schedules: []Schedule{sc}, CheckInterval: 40 * time.Millisecond, Logger: quiet}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.Run(ctx); err != nil {
				t.Error(err)
			}
		}()
	}
	var execs sync.Map
	var dup atomic.Int64
	w := testWorker(gdb, q)
	w.Register(testKind, func(_ context.Context, j Record) error {
		if _, loaded := execs.LoadOrStore(j.UniqueKey, true); loaded {
			dup.Add(1)
		}
		return nil
	})
	wg.Add(1)
	go func() { defer wg.Done(); w.Run(ctx) }()

	time.Sleep(3500 * time.Millisecond)
	cancel()
	wg.Wait()

	jobs := scheduleJobs(t, gdb, name)
	runs := countRuns(t, gdb, name)
	keys := map[string]bool{}
	for _, j := range jobs {
		if keys[j.UniqueKey] {
			t.Fatalf("давхар unique_key %s", j.UniqueKey)
		}
		keys[j.UniqueKey] = true
	}
	if len(jobs) < 2 || len(jobs) > 5 || runs != len(jobs) {
		t.Fatalf("jobs=%d runs=%d", len(jobs), runs)
	}
	if dup.Load() != 0 {
		t.Fatalf("давхар ажилласан %d", dup.Load())
	}
}

func TestSchedulerRunStopsOnCancel(t *testing.T) {
	gdb := testDB(t)
	name := testSchedName(t, gdb)
	s := &Scheduler{DB: gdb, Schedules: []Schedule{{Name: name, Spec: "@hourly", Kind: testKind}}, CheckInterval: 20 * time.Millisecond, Logger: quiet}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run зогссонгүй")
	}
}

func TestPurgeKeepsLatestScheduleRun(t *testing.T) {
	gdb := testDB(t)
	name := testSchedName(t, gdb)
	for _, d := range []string{"3 days", "2 days", "1 hour"} {
		gdb.Exec(`INSERT INTO khan.job_schedule_runs (name, scheduled_for) VALUES (?, now() - ?::interval)`, name, d)
	}
	if _, err := Purge(context.Background(), gdb, 24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if n := countRuns(t, gdb, name); n != 1 {
		t.Fatalf("runs=%d", n)
	}
	// Зөвхөн нэг хуучин мөр байвал (cursor сэргээхэд хэрэгтэй) үлдэнэ.
	gdb.Exec(`DELETE FROM khan.job_schedule_runs WHERE name = ?`, name)
	gdb.Exec(`INSERT INTO khan.job_schedule_runs (name, scheduled_for) VALUES (?, now() - interval '9 days')`, name)
	Purge(context.Background(), gdb, 24*time.Hour)
	if n := countRuns(t, gdb, name); n != 1 {
		t.Fatalf("сүүлийн мөр устсан: %d", n)
	}
}
