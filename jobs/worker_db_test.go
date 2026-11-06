package jobs

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"
)

func TestEnqueueValidation(t *testing.T) {
	ctx := context.Background()
	if _, err := Enqueue(ctx, nil, Job{Kind: "a.b"}); err == nil {
		t.Error("nil db")
	}
	gdb := testDB(t)
	tx := gdb.Begin()
	defer tx.Rollback()
	bad := []Job{
		{Kind: ""}, {Kind: "Bad.Kind"}, {Kind: "a..b"}, {Kind: "1abc"}, {Kind: strings.Repeat("a", 129)},
		{Kind: "a.b", Queue: "Bad Queue"}, {Kind: "a.b", Queue: strings.Repeat("q", 64)},
		{Kind: "a.b", OrgID: "not-a-uuid"}, {Kind: "a.b", MaxAttempts: -1}, {Kind: "a.b", Delay: -time.Second},
		{Kind: "a.b", Payload: make(chan int)},
	}
	for i, j := range bad {
		if _, err := Enqueue(ctx, tx, j); err == nil {
			t.Errorf("#%d %+v алдаагүй", i, j)
		}
	}
	// Алдаа DB-д хүрээгүй тул tx хэвээр ажиллана.
	if _, err := Enqueue(ctx, tx, Job{Kind: "a.b", Queue: "t-validation"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := EnqueueUnique(ctx, tx, Job{Kind: "a.b"}); !errors.Is(err, ErrNoUniqueKey) {
		t.Errorf("ErrNoUniqueKey: %v", err)
	}
}

func TestEnqueueRequiresTx(t *testing.T) {
	gdb := testDB(t)
	_, err := Enqueue(context.Background(), gdb, Job{Kind: testKind, Queue: "t-notx"})
	if !errors.Is(err, ErrNotInTx) {
		t.Fatalf("got %v", err)
	}
}

func TestEnqueueRollbackAndCommit(t *testing.T) {
	gdb := testDB(t)
	q := testQueue(t, gdb)
	ctx := context.Background()

	tx := gdb.Begin()
	if _, err := Enqueue(ctx, tx, Job{Queue: q, Kind: testKind}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := EnqueueUnique(ctx, tx, Job{Queue: q, Kind: testKind, UniqueKey: "k"}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback().Error; err != nil {
		t.Fatal(err)
	}
	var n int
	gdb.Raw(`SELECT count(*) FROM khan.jobs WHERE queue = ?`, q).Scan(&n)
	if n != 0 {
		t.Fatalf("rollback-ийн дараа %d job үлдсэн", n)
	}

	// Бизнес өгөгдөл бичих үед алдаа гарвал job мөн алга болно.
	err := gdb.Transaction(func(tx *gorm.DB) error {
		if _, err := Enqueue(ctx, tx, Job{Queue: q, Kind: testKind}); err != nil {
			return err
		}
		return errors.New("business failure")
	})
	if err == nil {
		t.Fatal("алдаа хүлээсэн")
	}
	gdb.Raw(`SELECT count(*) FROM khan.jobs WHERE queue = ?`, q).Scan(&n)
	if n != 0 {
		t.Fatalf("алдаатай tx-ийн дараа %d job", n)
	}

	enqueueOne(t, gdb, Job{Queue: q, Kind: testKind})
	gdb.Raw(`SELECT count(*) FROM khan.jobs WHERE queue = ?`, q).Scan(&n)
	if n != 1 {
		t.Fatalf("commit-ийн дараа %d", n)
	}
}

func TestEnqueueFieldsAndDefaults(t *testing.T) {
	gdb := testDB(t)
	q := testQueue(t, gdb)
	ctx := context.Background()

	// Анхдагч утгууд (rollback tx — shared "default" queue-д үлдээхгүй).
	tx := gdb.Begin()
	id, err := Enqueue(ctx, tx, Job{Kind: testKind})
	if err != nil {
		t.Fatal(err)
	}
	d, err := Get(ctx, tx, SystemScope(), id)
	if err != nil {
		t.Fatal(err)
	}
	tx.Rollback()
	if d.Queue != DefaultQueue || d.MaxAttempts != DefaultMaxAttempts || d.Status != StatusPending ||
		d.Attempts != 0 || d.OrgID != "" || string(d.Payload) != "null" || d.Priority != 0 {
		t.Fatalf("defaults: %+v", d)
	}

	past := time.Now().Add(-time.Hour).Truncate(time.Millisecond)
	id = enqueueOne(t, gdb, Job{
		Queue: q, Kind: testKind, OrgID: orgA, Payload: map[string]any{"n": 7, "s": "сайн"},
		Priority: 3, MaxAttempts: 9, RunAt: past, UniqueKey: "uk",
	})
	r := getJob(t, gdb, id)
	if r.OrgID != orgA || r.Priority != 3 || r.MaxAttempts != 9 || r.UniqueKey != "uk" || !r.RunAt.Equal(past) {
		t.Fatalf("fields: %+v", r)
	}
	var p struct {
		N int
		S string
	}
	if err := r.Decode(&p); err != nil || p.N != 7 || p.S != "сайн" {
		t.Fatalf("decode %v %+v", err, p)
	}

	// Delay: DB-ийн now()-аас хойш.
	id = enqueueOne(t, gdb, Job{Queue: q, Kind: testKind, Delay: time.Hour})
	if r := getJob(t, gdb, id); r.RunAt.Before(time.Now().Add(59 * time.Minute)) {
		t.Fatalf("delay: run_at=%s", r.RunAt)
	}
}

func TestUniqueKeyDedup(t *testing.T) {
	gdb := testDB(t)
	q := testQueue(t, gdb)
	ctx := context.Background()
	j := Job{Queue: q, Kind: testKind, OrgID: orgA, UniqueKey: "invoice:1"}

	var id1, id2, id3 int64
	err := gdb.Transaction(func(tx *gorm.DB) error {
		var created bool
		var err error
		if id1, created, err = EnqueueUnique(ctx, tx, j); err != nil || !created {
			return fmt.Errorf("эхний: created=%v err=%v", created, err)
		}
		// Давхардал нь tx-ийг abort хийх ёсгүй.
		if id2, created, err = EnqueueUnique(ctx, tx, j); err != nil || created {
			return fmt.Errorf("давхар: created=%v err=%v", created, err)
		}
		// Enqueue + UniqueKey: байгаа job-ийн id.
		if id3, err = Enqueue(ctx, tx, j); err != nil {
			return err
		}
		// tx амьд хэвээр.
		_, err = Enqueue(ctx, tx, Job{Queue: q, Kind: testKind})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if id1 == 0 || id1 != id2 || id1 != id3 {
		t.Fatalf("ids: %d %d %d", id1, id2, id3)
	}
	if n := countStatus(t, gdb, q, StatusPending); n != 2 {
		t.Fatalf("pending=%d, 2 хүлээсэн", n)
	}

	// Өөр tx-ээс ч давхардахгүй; өөр kind/queue/key өөр.
	err = gdb.Transaction(func(tx *gorm.DB) error {
		id, created, err := EnqueueUnique(ctx, tx, j)
		if err != nil || created || id != id1 {
			return fmt.Errorf("tx2: %d %v %v", id, created, err)
		}
		if _, created, err = EnqueueUnique(ctx, tx, Job{Queue: q, Kind: "test.other", UniqueKey: "invoice:1"}); err != nil || !created {
			return fmt.Errorf("өөр kind: %v %v", created, err)
		}
		if _, created, err = EnqueueUnique(ctx, tx, Job{Queue: q, Kind: testKind, UniqueKey: "invoice:2"}); err != nil || !created {
			return fmt.Errorf("өөр key: %v %v", created, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Дууссаны дараа түлхүүр чөлөөлөгдөнө.
	w := testWorker(gdb, q)
	w.Register(testKind, func(context.Context, Record) error { return nil })
	for i := 0; i < 5; i++ {
		w.RunOnce(ctx)
	}
	if r := getJob(t, gdb, id1); r.Status != StatusSucceeded {
		t.Fatalf("status=%s", r.Status)
	}
	var id4 int64
	var created bool
	err = gdb.Transaction(func(tx *gorm.DB) error {
		var err error
		id4, created, err = EnqueueUnique(ctx, tx, j)
		return err
	})
	if err != nil || !created || id4 == id1 {
		t.Fatalf("дууссаны дараа: id=%d created=%v err=%v", id4, created, err)
	}
}

func TestUniqueKeyConcurrentEnqueue(t *testing.T) {
	gdb := testDB(t)
	q := testQueue(t, gdb)
	ctx := context.Background()
	var created atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := gdb.Transaction(func(tx *gorm.DB) error {
				_, c, err := EnqueueUnique(ctx, tx, Job{Queue: q, Kind: testKind, UniqueKey: "race"})
				if c {
					created.Add(1)
				}
				return err
			})
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if created.Load() != 1 {
		t.Fatalf("created=%d", created.Load())
	}
	if n := countStatus(t, gdb, q, StatusPending); n != 1 {
		t.Fatalf("pending=%d", n)
	}
}

func TestWorkerRunsJob(t *testing.T) {
	gdb := testDB(t)
	q := testQueue(t, gdb)
	w := testWorker(gdb, q)
	var got Record
	var payload struct{ N int }
	w.Register(testKind, func(ctx context.Context, j Record) error {
		got = j
		return j.Decode(&payload)
	})
	id := enqueueOne(t, gdb, Job{Queue: q, Kind: testKind, OrgID: orgA, Payload: map[string]int{"N": 42}})
	n, err := w.RunOnce(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if got.ID != id || got.OrgID != orgA || got.Attempts != 1 || got.Kind != testKind || payload.N != 42 {
		t.Fatalf("record: %+v payload=%+v", got, payload)
	}
	r := getJob(t, gdb, id)
	if r.Status != StatusSucceeded || r.FinishedAt == nil || r.LockedBy != "" || r.LockedUntil != nil || r.Attempts != 1 {
		t.Fatalf("after: %+v", r)
	}
	if st := w.Stats(); st.Claimed != 1 || st.Succeeded != 1 || st.InFlight != 0 {
		t.Fatalf("stats: %+v", st)
	}
	if n, _ := w.RunOnce(context.Background()); n != 0 {
		t.Fatalf("дахин %d", n)
	}
}

func TestWorkerConfigErrors(t *testing.T) {
	gdb := testDB(t)
	w := &Worker{DB: gdb, Logger: quiet}
	if _, err := w.RunOnce(context.Background()); err == nil {
		t.Error("handler-гүй RunOnce")
	}
	if err := w.Run(context.Background()); err == nil {
		t.Error("handler-гүй Run")
	}
	h := func(context.Context, Record) error { return nil }
	if err := w.Register("Bad", h); !errors.Is(err, ErrInvalidKind) {
		t.Errorf("kind: %v", err)
	}
	if err := w.Register("a.b", nil); err == nil {
		t.Error("nil handler")
	}
	if err := w.Register("a.b", h); err != nil {
		t.Fatal(err)
	}
	if err := w.Register("a.b", h); err == nil {
		t.Error("давхар бүртгэл")
	}
	if _, err := (&Worker{}).RunOnce(context.Background()); err == nil {
		t.Error("DB nil")
	}
}

func TestWorkerOnlyOwnQueuesAndKinds(t *testing.T) {
	gdb := testDB(t)
	q1, q2 := testQueue(t, gdb), testQueue(t, gdb)
	enqueueOne(t, gdb, Job{Queue: q1, Kind: testKind})
	enqueueOne(t, gdb, Job{Queue: q1, Kind: "test.unregistered"})
	enqueueOne(t, gdb, Job{Queue: q2, Kind: testKind})
	w := testWorker(gdb, q1)
	w.Register(testKind, func(context.Context, Record) error { return nil })
	for i := 0; i < 3; i++ {
		w.RunOnce(context.Background())
	}
	if countStatus(t, gdb, q1, StatusSucceeded) != 1 || countStatus(t, gdb, q1, StatusPending) != 1 || countStatus(t, gdb, q2, StatusPending) != 1 {
		t.Fatal("queue/kind шүүлт буруу")
	}
}

func TestPriorityAndRunAtOrdering(t *testing.T) {
	gdb := testDB(t)
	q := testQueue(t, gdb)
	w := testWorker(gdb, q)
	w.Concurrency = 1
	var mu sync.Mutex
	var order []string
	w.Register(testKind, func(_ context.Context, j Record) error {
		var s string
		j.Decode(&s)
		mu.Lock()
		order = append(order, s)
		mu.Unlock()
		return nil
	})
	now := time.Now()
	enqueueOne(t, gdb, Job{Queue: q, Kind: testKind, Payload: "low-new", Priority: 0, RunAt: now.Add(-time.Second)})
	enqueueOne(t, gdb, Job{Queue: q, Kind: testKind, Payload: "low-old", Priority: 0, RunAt: now.Add(-time.Minute)})
	enqueueOne(t, gdb, Job{Queue: q, Kind: testKind, Payload: "high", Priority: 10, RunAt: now.Add(-time.Second)})
	enqueueOne(t, gdb, Job{Queue: q, Kind: testKind, Payload: "future", Priority: 99, Delay: time.Hour})
	for i := 0; i < 5; i++ {
		w.RunOnce(context.Background())
	}
	if got := strings.Join(order, ","); got != "high,low-old,low-new" {
		t.Fatalf("дараалал: %s", got)
	}
	if countStatus(t, gdb, q, StatusPending) != 1 {
		t.Fatal("future job авагдсан")
	}
}

func TestRetryBackoffDead(t *testing.T) {
	gdb := testDB(t)
	q := testQueue(t, gdb)
	w := testWorker(gdb, q)
	var attempts []int
	var backoffArgs []int
	w.Backoff = func(a int) time.Duration { backoffArgs = append(backoffArgs, a); return 0 }
	w.Register(testKind, func(_ context.Context, j Record) error {
		attempts = append(attempts, j.Attempts)
		return fmt.Errorf("boom %d", j.Attempts)
	})
	id := enqueueOne(t, gdb, Job{Queue: q, Kind: testKind, MaxAttempts: 3})

	w.RunOnce(context.Background())
	r := getJob(t, gdb, id)
	if r.Status != StatusPending || r.Attempts != 1 || r.LastError != "boom 1" || r.LockedBy != "" {
		t.Fatalf("1: %+v", r)
	}
	w.RunOnce(context.Background())
	w.RunOnce(context.Background())
	r = getJob(t, gdb, id)
	if r.Status != StatusDead || r.Attempts != 3 || r.LastError != "boom 3" || r.FinishedAt == nil {
		t.Fatalf("dead: %+v", r)
	}
	if n, _ := w.RunOnce(context.Background()); n != 0 {
		t.Fatal("dead job дахин авагдсан")
	}
	if fmt.Sprint(attempts) != "[1 2 3]" || fmt.Sprint(backoffArgs) != "[1 2]" {
		t.Fatalf("attempts=%v backoff=%v", attempts, backoffArgs)
	}
	if st := w.Stats(); st.Retried != 2 || st.Dead != 1 || st.Succeeded != 0 {
		t.Fatalf("stats %+v", st)
	}
}

func TestBackoffDelaysRetry(t *testing.T) {
	gdb := testDB(t)
	q := testQueue(t, gdb)
	w := testWorker(gdb, q)
	w.Backoff = func(int) time.Duration { return time.Hour }
	w.Register(testKind, func(context.Context, Record) error { return errors.New("x") })
	id := enqueueOne(t, gdb, Job{Queue: q, Kind: testKind})
	w.RunOnce(context.Background())
	r := getJob(t, gdb, id)
	if r.Status != StatusPending || r.RunAt.Before(time.Now().Add(59*time.Minute)) {
		t.Fatalf("run_at=%s status=%s", r.RunAt, r.Status)
	}
	if n, _ := w.RunOnce(context.Background()); n != 0 {
		t.Fatal("backoff-д байгаа job авагдсан")
	}
}

func TestDefaultBackoffShape(t *testing.T) {
	prev := time.Duration(0)
	for a := 1; a <= 30; a++ {
		base := backoffBase(a)
		if base < prev || base > maxBackoff {
			t.Fatalf("attempt %d base=%s prev=%s", a, base, prev)
		}
		prev = base
		for i := 0; i < 50; i++ {
			d := DefaultBackoff(a)
			if d < time.Duration(float64(base)*0.8) || d > time.Duration(float64(base)*1.2) {
				t.Fatalf("attempt %d jitter %s base %s", a, d, base)
			}
		}
	}
	if backoffBase(1) != 5*time.Second || backoffBase(2) != 10*time.Second || backoffBase(0) != 5*time.Second || backoffBase(30) != maxBackoff {
		t.Fatal("base утга")
	}
	// Jitter үнэхээр санамсаргүй.
	seen := map[time.Duration]bool{}
	for i := 0; i < 20; i++ {
		seen[DefaultBackoff(3)] = true
	}
	if len(seen) < 2 {
		t.Fatal("jitter алга")
	}
}

func TestPermanentErrorGoesDead(t *testing.T) {
	gdb := testDB(t)
	q := testQueue(t, gdb)
	w := testWorker(gdb, q)
	w.Register(testKind, func(context.Context, Record) error { return Permanent(errors.New("bad payload")) })
	id := enqueueOne(t, gdb, Job{Queue: q, Kind: testKind, MaxAttempts: 10})
	w.RunOnce(context.Background())
	r := getJob(t, gdb, id)
	if r.Status != StatusDead || r.Attempts != 1 || r.LastError != "bad payload" {
		t.Fatalf("%+v", r)
	}
	if Permanent(nil) != nil || !IsPermanent(fmt.Errorf("wrap: %w", Permanent(errors.New("x")))) || IsPermanent(errors.New("x")) {
		t.Fatal("Permanent helper")
	}
}

func TestPanicRecovery(t *testing.T) {
	gdb := testDB(t)
	q := testQueue(t, gdb)
	w := testWorker(gdb, q)
	var calls atomic.Int64
	w.Register(testKind, func(_ context.Context, j Record) error {
		var s string
		j.Decode(&s)
		if s == "panic" && calls.Add(1) == 1 {
			panic("kaboom")
		}
		return nil
	})
	pid := enqueueOne(t, gdb, Job{Queue: q, Kind: testKind, Payload: "panic"})
	oid := enqueueOne(t, gdb, Job{Queue: q, Kind: testKind, Payload: "ok"})
	n, err := w.RunOnce(context.Background())
	if err != nil || n != 2 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	p := getJob(t, gdb, pid)
	if p.Status != StatusPending || !strings.Contains(p.LastError, "panic: kaboom") {
		t.Fatalf("panic job: %+v", p)
	}
	if getJob(t, gdb, oid).Status != StatusSucceeded {
		t.Fatal("бусад job панкид нөлөөлсөн")
	}
	w.RunOnce(context.Background())
	if getJob(t, gdb, pid).Status != StatusSucceeded {
		t.Fatal("panic-ийн дараах retry")
	}
	if w.Stats().Panics != 1 {
		t.Fatalf("panics=%d", w.Stats().Panics)
	}
}

func TestLongErrorTruncatedUTF8(t *testing.T) {
	gdb := testDB(t)
	q := testQueue(t, gdb)
	w := testWorker(gdb, q)
	w.Register(testKind, func(context.Context, Record) error {
		return errors.New(strings.Repeat("алдаа\x00", 1000))
	})
	id := enqueueOne(t, gdb, Job{Queue: q, Kind: testKind})
	w.RunOnce(context.Background())
	r := getJob(t, gdb, id)
	if len(r.LastError) > maxErrLen || strings.Contains(r.LastError, "\x00") || r.Status != StatusPending {
		t.Fatalf("len=%d status=%s", len(r.LastError), r.Status)
	}
}

func TestTwoWorkersNeverRunSameJob(t *testing.T) {
	gdb := testDB(t)
	q := testQueue(t, gdb)
	const total = 300
	err := gdb.Transaction(func(tx *gorm.DB) error {
		for i := 0; i < total; i++ {
			if _, err := Enqueue(context.Background(), tx, Job{Queue: q, Kind: testKind, Payload: i}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var seen sync.Map
	var dups, runs atomic.Int64
	handler := func(_ context.Context, j Record) error {
		runs.Add(1)
		if _, loaded := seen.LoadOrStore(j.ID, true); loaded {
			dups.Add(1)
		}
		time.Sleep(time.Millisecond)
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	var workers []*Worker
	for i := 0; i < 2; i++ {
		w := testWorker(gdb, q)
		w.Concurrency = 8
		w.ID = fmt.Sprintf("w%d", i)
		w.Register(testKind, handler)
		workers = append(workers, w)
		wg.Add(1)
		go func() { defer wg.Done(); w.Run(ctx) }()
	}
	waitFor(t, 20*time.Second, "бүх job дуусах", func() bool { return countStatus(t, gdb, q, StatusSucceeded) == total })
	cancel()
	wg.Wait()
	if dups.Load() != 0 || runs.Load() != total {
		t.Fatalf("dups=%d runs=%d", dups.Load(), runs.Load())
	}
	// Хоёулаа ажил хийсэн (хуваарилалт).
	a, b := workers[0].Stats().Succeeded, workers[1].Stats().Succeeded
	if a+b != total {
		t.Fatalf("succeeded %d+%d", a, b)
	}
	var attempts int
	gdb.Raw(`SELECT max(attempts) FROM khan.jobs WHERE queue = ?`, q).Scan(&attempts)
	if attempts != 1 {
		t.Fatalf("max attempts=%d", attempts)
	}
}

func TestConcurrencyLimit(t *testing.T) {
	gdb := testDB(t)
	q := testQueue(t, gdb)
	for i := 0; i < 12; i++ {
		enqueueOne(t, gdb, Job{Queue: q, Kind: testKind})
	}
	var cur, peak atomic.Int64
	w := testWorker(gdb, q)
	w.Concurrency = 3
	w.Register(testKind, func(context.Context, Record) error {
		c := cur.Add(1)
		for {
			p := peak.Load()
			if c <= p || peak.CompareAndSwap(p, c) {
				break
			}
		}
		time.Sleep(40 * time.Millisecond)
		cur.Add(-1)
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()
	waitFor(t, 15*time.Second, "12 job", func() bool { return countStatus(t, gdb, q, StatusSucceeded) == 12 })
	cancel()
	<-done
	if peak.Load() != 3 {
		t.Fatalf("peak=%d, 3 хүлээсэн", peak.Load())
	}
}

func TestLeaseExpiryReclaim(t *testing.T) {
	gdb := testDB(t)
	q := testQueue(t, gdb)
	id := enqueueOne(t, gdb, Job{Queue: q, Kind: testKind, MaxAttempts: 3})
	// Унасан worker-ийг дуурайх: job running, lease нь хүчинтэй.
	err := gdb.Exec(`UPDATE khan.jobs SET status='running', attempts=1, locked_by='ghost',
		locked_until = now() + interval '1 hour' WHERE id = ?`, id).Error
	if err != nil {
		t.Fatal(err)
	}
	var ran atomic.Int64
	w := testWorker(gdb, q)
	w.Register(testKind, func(_ context.Context, j Record) error {
		ran.Add(1)
		if j.Attempts != 2 {
			t.Errorf("attempts=%d", j.Attempts)
		}
		return nil
	})
	if n, _ := w.RunOnce(context.Background()); n != 0 || ran.Load() != 0 {
		t.Fatal("хүчинтэй lease-тэй job авагдсан")
	}
	gdb.Exec(`UPDATE khan.jobs SET locked_until = now() - interval '1 second' WHERE id = ?`, id)
	if n, _ := w.RunOnce(context.Background()); n != 1 || ran.Load() != 1 {
		t.Fatalf("reclaim хийгдсэнгүй n=%d", n)
	}
	r := getJob(t, gdb, id)
	if r.Status != StatusSucceeded || r.Attempts != 2 {
		t.Fatalf("%+v", r)
	}
}

func TestExpiredLeaseExhaustedAttemptsGoesDead(t *testing.T) {
	gdb := testDB(t)
	q := testQueue(t, gdb)
	id := enqueueOne(t, gdb, Job{Queue: q, Kind: testKind, MaxAttempts: 2})
	gdb.Exec(`UPDATE khan.jobs SET status='running', attempts=2, locked_by='ghost',
		locked_until = now() - interval '1 second' WHERE id = ?`, id)
	var ran atomic.Int64
	w := testWorker(gdb, q)
	w.Register(testKind, func(context.Context, Record) error { ran.Add(1); return nil })
	w.RunOnce(context.Background())
	r := getJob(t, gdb, id)
	if ran.Load() != 0 || r.Status != StatusDead || !strings.Contains(r.LastError, "lease") || r.FinishedAt == nil {
		t.Fatalf("ran=%d %+v", ran.Load(), r)
	}
}

func TestHeartbeatKeepsLease(t *testing.T) {
	gdb := testDB(t)
	q := testQueue(t, gdb)
	id := enqueueOne(t, gdb, Job{Queue: q, Kind: testKind})

	var runs atomic.Int64
	started := make(chan struct{})
	slow := testWorker(gdb, q) // Lease 600мс → heartbeat 200мс
	slow.ID = "slow"
	slow.Register(testKind, func(ctx context.Context, j Record) error {
		runs.Add(1)
		close(started)
		time.Sleep(2 * time.Second) // lease-ээс 3 дахин урт
		return ctx.Err()
	})
	done := make(chan struct{})
	go func() { slow.RunOnce(context.Background()); close(done) }()
	<-started

	other := testWorker(gdb, q)
	other.ID = "other"
	other.Register(testKind, func(context.Context, Record) error { runs.Add(1); return nil })
	deadline := time.Now().Add(1800 * time.Millisecond)
	for time.Now().Before(deadline) {
		if n, _ := other.RunOnce(context.Background()); n != 0 {
			t.Fatal("heartbeat-тэй job-ийг өөр worker авсан")
		}
		time.Sleep(50 * time.Millisecond)
	}
	<-done
	if runs.Load() != 1 {
		t.Fatalf("runs=%d", runs.Load())
	}
	if r := getJob(t, gdb, id); r.Status != StatusSucceeded || r.Attempts != 1 {
		t.Fatalf("%+v", r)
	}
}

func TestCancelRunningKeepsKeyUntilHandlerStops(t *testing.T) {
	gdb := testDB(t)
	q := testQueue(t, gdb)
	ctx := context.Background()
	id := enqueueOne(t, gdb, Job{Queue: q, Kind: testKind, UniqueKey: "k1"})
	w := testWorker(gdb, q)
	started := make(chan struct{})
	finish := make(chan struct{})
	var cause atomic.Value
	w.Register(testKind, func(hctx context.Context, j Record) error {
		close(started)
		<-hctx.Done()
		cause.Store(context.Cause(hctx))
		<-finish // handler ctx-ийг хүндэтгээд ч удаан зогсож буйг дуурайна
		return hctx.Err()
	})
	done := make(chan struct{})
	go func() { w.RunOnce(ctx); close(done) }()
	<-started
	if err := Cancel(ctx, gdb, SystemScope(), id); err != nil {
		t.Fatal(err)
	}
	if err := Cancel(ctx, gdb, SystemScope(), id); err != nil { // давтан хүсэлт аюулгүй
		t.Fatalf("давтан cancel: %v", err)
	}
	// Төлөв running хэвээр, түлхүүр эзлэгдсэн: ижил түлхүүрээр шинэ job үүсэхгүй.
	r := getJob(t, gdb, id)
	if r.Status != StatusRunning || !r.CancelRequested {
		t.Fatalf("running+cancel_requested хүлээсэн: %+v", r)
	}
	err := gdb.Transaction(func(tx *gorm.DB) error {
		got, created, err := EnqueueUnique(ctx, tx, Job{Queue: q, Kind: testKind, UniqueKey: "k1"})
		if err != nil || created || got != id {
			t.Errorf("түлхүүр чөлөөлөгдсөн: id=%d created=%v err=%v", got, created, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Worker heartbeat-ээр ажиглаад ctx-ийг цуцална.
	waitFor(t, 5*time.Second, "handler ctx цуцлагдах", func() bool { return cause.Load() != nil })
	if c, _ := cause.Load().(error); !errors.Is(c, ErrCancelRequested) {
		t.Fatalf("cause=%v", c)
	}
	if getJob(t, gdb, id).Status != StatusRunning {
		t.Fatal("handler зогсохоос өмнө төлөв өөрчлөгдсөн")
	}
	close(finish)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RunOnce дууссангүй")
	}
	if r := getJob(t, gdb, id); r.Status != StatusCanceled || r.FinishedAt == nil {
		t.Fatalf("worker цуцлалтыг дарж бичсэн: %+v", r)
	}
	if s := w.Stats(); s.Canceled != 1 || s.LeaseLost != 0 || s.Retried != 0 {
		t.Fatalf("stats %+v", s)
	}
	// Одоо түлхүүр чөлөөлөгдсөн.
	gdb.Transaction(func(tx *gorm.DB) error {
		if _, created, err := EnqueueUnique(ctx, tx, Job{Queue: q, Kind: testKind, UniqueKey: "k1"}); err != nil || !created {
			t.Errorf("created=%v err=%v", created, err)
		}
		return nil
	})
}

// Lease нь дууссан (worker унасан) running job-д Cancel шууд canceled болгоно.
func TestCancelExpiredRunningImmediate(t *testing.T) {
	gdb := testDB(t)
	q := testQueue(t, gdb)
	id := enqueueOne(t, gdb, Job{Queue: q, Kind: testKind})
	gdb.Exec(`UPDATE khan.jobs SET status='running', locked_by='dead-worker', attempts=1,
		locked_until = now() - interval '1 minute' WHERE id = ?`, id)
	if err := Cancel(context.Background(), gdb, SystemScope(), id); err != nil {
		t.Fatal(err)
	}
	if r := getJob(t, gdb, id); r.Status != StatusCanceled {
		t.Fatalf("%+v", r)
	}
}

// Унасан worker-ийн cancel_requested-тэй job reclaim хийгдэхгүй, canceled болно.
func TestReapCancelRequestedExpired(t *testing.T) {
	gdb := testDB(t)
	q := testQueue(t, gdb)
	id := enqueueOne(t, gdb, Job{Queue: q, Kind: testKind})
	gdb.Exec(`UPDATE khan.jobs SET status='running', locked_by='dead-worker', attempts=1,
		cancel_requested = true, locked_until = now() - interval '1 minute' WHERE id = ?`, id)
	var ran atomic.Int32
	w := testWorker(gdb, q)
	w.Register(testKind, func(context.Context, Record) error { ran.Add(1); return nil })
	if n, err := w.RunOnce(context.Background()); err != nil || n != 0 || ran.Load() != 0 {
		t.Fatalf("n=%d ran=%d err=%v", n, ran.Load(), err)
	}
	if r := getJob(t, gdb, id); r.Status != StatusCanceled {
		t.Fatalf("%+v", r)
	}
}

// reap болон claim хоёрын хооронд lease нь дууссан, attempts == max_attempts job
// дахин авагдаж (attempts=max+1) ажиллах ёсгүй.
func TestClaimSkipsExhaustedExpiredRunning(t *testing.T) {
	gdb := testDB(t)
	q := testQueue(t, gdb)
	id := enqueueOne(t, gdb, Job{Queue: q, Kind: testKind, MaxAttempts: 2})
	gdb.Exec(`UPDATE khan.jobs SET status='running', locked_by='dead-worker', attempts=2,
		locked_until = now() - interval '1 minute' WHERE id = ?`, id)
	w := testWorker(gdb, q)
	w.Register(testKind, func(context.Context, Record) error { return nil })
	// reap-ийг алгасаж claim-ийн SQL-ийг шууд шалгана (уралдааны цонх).
	var rows []row
	err := gdb.Raw(`
		WITH c AS (
			SELECT id FROM khan.jobs
			WHERE queue IN ? AND kind IN ?
				AND ((status = 'pending' AND run_at <= now())
					OR (status = 'running' AND locked_until < now()
						AND attempts < max_attempts AND NOT cancel_requested))
			LIMIT 10 FOR UPDATE SKIP LOCKED)
		SELECT id FROM c`, []string{q}, []string{testKind}).Scan(&rows).Error
	if err != nil || len(rows) != 0 {
		t.Fatalf("claim-ийн нөхцөл: rows=%d err=%v", len(rows), err)
	}
	// Жинхэнэ claim: reap → dead, ажиллахгүй.
	recs, _, err := w.claim(context.Background(), 5)
	if err != nil || len(recs) != 0 {
		t.Fatalf("recs=%d err=%v", len(recs), err)
	}
	if r := getJob(t, gdb, id); r.Status != StatusDead || r.Attempts != 2 {
		t.Fatalf("%+v", r)
	}
}

// DB-д heartbeat хүрэхгүй бол локал lease дуусмагц handler-ийн ctx ErrLeaseLost-оор
// цуцлагдаж, үр дүн ХАДГАЛАГДАХГҮЙ.
func TestHeartbeatFailureCancelsHandlerAndSkipsFinalize(t *testing.T) {
	gdb := testDB(t)
	q := testQueue(t, gdb)
	id := enqueueOne(t, gdb, Job{Queue: q, Kind: testKind})
	var failHB atomic.Bool
	name := "jobs_test:fail_heartbeat_" + randHex(4)
	err := gdb.Callback().Row().Before("gorm:row").Register(name, func(d *gorm.DB) {
		if failHB.Load() && strings.Contains(d.Statement.SQL.String(), "RETURNING cancel_requested") {
			d.AddError(errors.New("тест: DB хүрэхгүй"))
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { gdb.Callback().Row().Remove(name) })

	w := testWorker(gdb, q)
	var cause atomic.Value
	w.Register(testKind, func(hctx context.Context, j Record) error {
		failHB.Store(true)
		<-hctx.Done()
		cause.Store(context.Cause(hctx))
		return nil // handler нь "амжилттай" гэж буцаасан ч үр дүн бичигдэхгүй
	})
	start := time.Now()
	if _, err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	failHB.Store(false)
	if c, _ := cause.Load().(error); !errors.Is(c, ErrLeaseLost) {
		t.Fatalf("cause=%v", c)
	}
	if el := time.Since(start); el > 3*w.Lease {
		t.Fatalf("lease хугацаанаас хэт хожуу цуцлагдсан: %s", el)
	}
	if r := getJob(t, gdb, id); r.Status != StatusRunning || r.LockedBy != w.workerID() {
		t.Fatalf("finalize хийсэн: %+v", r)
	}
	if s := w.Stats(); s.LeaseLost != 1 || s.Succeeded != 0 {
		t.Fatalf("stats %+v", s)
	}
	// Lease дуусмагц өөр worker reclaim хийнэ.
	waitFor(t, 3*time.Second, "reclaim", func() bool {
		w2 := testWorker(gdb, q)
		w2.ID = "w2"
		w2.Register(testKind, func(context.Context, Record) error { return nil })
		n, _ := w2.RunOnce(context.Background())
		return n == 1
	})
	if r := getJob(t, gdb, id); r.Status != StatusSucceeded || r.Attempts != 2 {
		t.Fatalf("%+v", r)
	}
}

func TestUniqueKeyScopedByOrg(t *testing.T) {
	gdb := testDB(t)
	q := testQueue(t, gdb)
	ctx := context.Background()
	enq := func(org string) (int64, bool) {
		var id int64
		var created bool
		err := gdb.Transaction(func(tx *gorm.DB) error {
			var err error
			id, created, err = EnqueueUnique(ctx, tx, Job{Queue: q, Kind: testKind, OrgID: org, UniqueKey: "same"})
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return id, created
	}
	a, ca := enq(orgA)
	b, cb := enq(orgB)
	s, cs := enq("")
	if !ca || !cb || !cs || a == b || a == s || b == s {
		t.Fatalf("org бүрт тусдаа job: a=%d/%v b=%d/%v sys=%d/%v", a, ca, b, cb, s, cs)
	}
	if id, c := enq(orgA); c || id != a {
		t.Fatalf("orgA давхардал: %d %v", id, c)
	}
	if id, c := enq(""); c || id != s {
		t.Fatalf("систем давхардал: %d %v", id, c)
	}
	if id, c := enq(orgB); c || id != b {
		t.Fatalf("orgB давхардал: %d %v", id, c)
	}
}

// Хуучин (org-гүй) unique индекс байвал SchemaSQL шинийг үүсгээд хуучныг устгана.
func TestSchemaMigratesOldUniqueIndex(t *testing.T) {
	gdb := testDB(t)
	run := func() {
		err := gdb.Transaction(func(tx *gorm.DB) error {
			if err := tx.Exec(`SELECT pg_advisory_xact_lock(1802398062)`).Error; err != nil {
				return err
			}
			return tx.Exec(SchemaSQL).Error
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	has := func(name string) bool {
		var n int
		gdb.Raw(`SELECT count(*) FROM pg_indexes WHERE schemaname='khan' AND tablename='jobs' AND indexname = ?`, name).Scan(&n)
		return n == 1
	}
	if err := gdb.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS jobs_unique_key_idx ON khan.jobs (queue, kind, unique_key)
		WHERE unique_key IS NOT NULL AND status IN ('pending','running')`).Error; err != nil {
		t.Fatal(err)
	}
	run()
	run() // идемпотент
	if has("jobs_unique_key_idx") || !has("jobs_unique_key_org_idx") {
		t.Fatal("индекс шилжээгүй")
	}
}

func TestFencingStaleWorkerCannotOverwrite(t *testing.T) {
	gdb := testDB(t)
	q := testQueue(t, gdb)
	id := enqueueOne(t, gdb, Job{Queue: q, Kind: testKind})
	stale := testWorker(gdb, q)
	stale.ID = "stale"
	release := make(chan struct{})
	started := make(chan struct{})
	stale.Register(testKind, func(ctx context.Context, j Record) error {
		close(started)
		<-release
		return nil
	})
	done := make(chan struct{})
	go func() { stale.RunOnce(context.Background()); close(done) }()
	<-started
	// Өөр worker lease-ийг булаан авсан гэж дуурайна.
	gdb.Exec(`UPDATE khan.jobs SET locked_by='thief', attempts=attempts+1 WHERE id = ?`, id)
	close(release)
	<-done
	r := getJob(t, gdb, id)
	if r.Status != StatusRunning || r.LockedBy != "thief" || r.Attempts != 2 {
		t.Fatalf("stale worker дарж бичсэн: %+v", r)
	}
	if stale.Stats().LeaseLost != 1 || stale.Stats().Succeeded != 0 {
		t.Fatalf("stats %+v", stale.Stats())
	}
}

func TestJobTimeout(t *testing.T) {
	gdb := testDB(t)
	q := testQueue(t, gdb)
	w := testWorker(gdb, q)
	w.JobTimeout = 100 * time.Millisecond
	w.Register(testKind, func(ctx context.Context, j Record) error {
		<-ctx.Done()
		return ctx.Err()
	})
	// Per-kind timeout нь Worker-ийнхийг дарна.
	w.Register("test.slowok", func(ctx context.Context, j Record) error {
		select {
		case <-time.After(250 * time.Millisecond):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}, WithTimeout(5*time.Second))
	id := enqueueOne(t, gdb, Job{Queue: q, Kind: testKind})
	id2 := enqueueOne(t, gdb, Job{Queue: q, Kind: "test.slowok"})
	w.RunOnce(context.Background())
	r := getJob(t, gdb, id)
	if r.Status != StatusPending || !strings.Contains(r.LastError, "хугацаа хэтэрсэн") {
		t.Fatalf("%+v", r)
	}
	if getJob(t, gdb, id2).Status != StatusSucceeded {
		t.Fatal("WithTimeout ажиллаагүй")
	}
	if w.Stats().Timeouts != 1 {
		t.Fatalf("stats %+v", w.Stats())
	}
}

func TestGracefulShutdownFinishesInFlight(t *testing.T) {
	gdb := testDB(t)
	q := testQueue(t, gdb)
	id := enqueueOne(t, gdb, Job{Queue: q, Kind: testKind})
	extra := enqueueOne(t, gdb, Job{Queue: q, Kind: testKind, Delay: 0})
	w := testWorker(gdb, q)
	w.Concurrency = 1
	started := make(chan struct{})
	var once sync.Once
	var finished atomic.Bool
	w.Register(testKind, func(ctx context.Context, j Record) error {
		once.Do(func() { close(started) })
		time.Sleep(400 * time.Millisecond) // ctx-г үл тооно: дуустал нь хүлээгдэнэ
		finished.Store(true)
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	<-started
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run зогссонгүй")
	}
	if !finished.Load() {
		t.Fatal("in-flight job дуусаагүй байхад Run буцсан")
	}
	// Эхний job дууссан; хоёр дахь нь авагдаагүй (зогсож буй worker шинэ job авдаггүй).
	succ := countStatus(t, gdb, q, StatusSucceeded)
	pend := countStatus(t, gdb, q, StatusPending)
	if succ != 1 || pend != 1 {
		t.Fatalf("succeeded=%d pending=%d (ids %d,%d)", succ, pend, id, extra)
	}
}

func TestShutdownReleasesStuckJob(t *testing.T) {
	gdb := testDB(t)
	q := testQueue(t, gdb)
	id := enqueueOne(t, gdb, Job{Queue: q, Kind: testKind})
	w := testWorker(gdb, q)
	w.ShutdownTimeout = 150 * time.Millisecond
	started := make(chan struct{})
	var cause atomic.Value
	w.Register(testKind, func(ctx context.Context, j Record) error {
		close(started)
		<-ctx.Done()
		cause.Store(context.Cause(ctx))
		return ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	<-started
	begin := time.Now()
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if d := time.Since(begin); d < 100*time.Millisecond || d > 3*time.Second {
		t.Fatalf("зогсоход %s", d)
	}
	if c, _ := cause.Load().(error); !errors.Is(c, ErrShutdown) {
		t.Fatalf("cause=%v", c)
	}
	r := getJob(t, gdb, id)
	if r.Status != StatusPending || r.Attempts != 0 || r.LockedBy != "" || r.LastError != "" {
		t.Fatalf("release хийгдсэнгүй: %+v", r)
	}
	if w.Stats().Released != 1 || w.Stats().Retried != 0 {
		t.Fatalf("stats %+v", w.Stats())
	}
	// Өөр worker шууд аваад дуусгана; анхны оролдлого гэж тооцогдоно.
	w2 := testWorker(gdb, q)
	w2.Register(testKind, func(_ context.Context, j Record) error {
		if j.Attempts != 1 {
			t.Errorf("attempts=%d", j.Attempts)
		}
		return nil
	})
	if n, _ := w2.RunOnce(context.Background()); n != 1 {
		t.Fatal("чөлөөлсөн job шууд авагдсангүй")
	}
}

func TestRunOnceCanceledContextReleases(t *testing.T) {
	gdb := testDB(t)
	q := testQueue(t, gdb)
	id := enqueueOne(t, gdb, Job{Queue: q, Kind: testKind})
	w := testWorker(gdb, q)
	started := make(chan struct{})
	w.Register(testKind, func(ctx context.Context, j Record) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.RunOnce(ctx); close(done) }()
	<-started
	cancel()
	<-done
	if r := getJob(t, gdb, id); r.Status != StatusPending || r.Attempts != 0 {
		t.Fatalf("%+v", r)
	}
}

func TestRegisterAfterRunStarts(t *testing.T) {
	gdb := testDB(t)
	q := testQueue(t, gdb)
	w := testWorker(gdb, q)
	w.Register("test.first", func(context.Context, Record) error { return nil })
	id := enqueueOne(t, gdb, Job{Queue: q, Kind: testKind})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()
	time.Sleep(100 * time.Millisecond)
	if getJob(t, gdb, id).Status != StatusPending {
		t.Fatal("бүртгэлгүй kind авагдсан")
	}
	w.Register(testKind, func(context.Context, Record) error { return nil })
	waitFor(t, 5*time.Second, "шинэ handler-ийн job", func() bool { return getJob(t, gdb, id).Status == StatusSucceeded })
	cancel()
	<-done
}
