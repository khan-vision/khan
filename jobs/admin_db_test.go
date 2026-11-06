package jobs

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestScopeRequired(t *testing.T) {
	gdb := testDB(t)
	ctx := context.Background()
	var zero Scope
	if _, err := List(ctx, gdb, zero, ListFilter{}); !errors.Is(err, ErrScope) {
		t.Errorf("List: %v", err)
	}
	if _, err := Get(ctx, gdb, OrgScope(""), 1); !errors.Is(err, ErrScope) {
		t.Errorf("Get: %v", err)
	}
	if err := Retry(ctx, gdb, zero, 1); !errors.Is(err, ErrScope) {
		t.Errorf("Retry: %v", err)
	}
	if err := Cancel(ctx, gdb, zero, 1); !errors.Is(err, ErrScope) {
		t.Errorf("Cancel: %v", err)
	}
	if _, err := QueueStats(ctx, gdb, zero); !errors.Is(err, ErrScope) {
		t.Errorf("QueueStats: %v", err)
	}
	if _, err := List(ctx, gdb, OrgScope("nope"), ListFilter{}); !errors.Is(err, ErrInvalidOrg) {
		t.Errorf("bad org: %v", err)
	}
	if _, err := List(ctx, gdb, SystemScope(), ListFilter{Statuses: []Status{"bogus"}}); err == nil {
		t.Error("bad status")
	}
}

func TestAdminOrgScoping(t *testing.T) {
	gdb := testDB(t)
	q := testQueue(t, gdb)
	ctx := context.Background()
	a := enqueueOne(t, gdb, Job{Queue: q, Kind: testKind, OrgID: orgA})
	b := enqueueOne(t, gdb, Job{Queue: q, Kind: testKind, OrgID: orgB})
	sys := enqueueOne(t, gdb, Job{Queue: q, Kind: "test.other"})

	ids := func(scope Scope, f ListFilter) []int64 {
		f.Queue = q
		rs, err := List(ctx, gdb, scope, f)
		if err != nil {
			t.Fatal(err)
		}
		var out []int64
		for _, r := range rs {
			out = append(out, r.ID)
		}
		return out
	}
	if got := ids(OrgScope(orgA), ListFilter{}); len(got) != 1 || got[0] != a {
		t.Fatalf("org A: %v", got)
	}
	if got := ids(OrgScope(orgB), ListFilter{}); len(got) != 1 || got[0] != b {
		t.Fatalf("org B: %v", got)
	}
	if got := ids(SystemScope(), ListFilter{}); len(got) != 3 || got[0] != sys {
		t.Fatalf("system (id DESC): %v", got)
	}
	if got := ids(SystemScope(), ListFilter{Kind: "test.other"}); len(got) != 1 || got[0] != sys {
		t.Fatalf("kind filter: %v", got)
	}
	if got := ids(SystemScope(), ListFilter{Limit: 2}); len(got) != 2 {
		t.Fatalf("limit: %v", got)
	}
	if got := ids(SystemScope(), ListFilter{Limit: 2, Offset: 2}); len(got) != 1 {
		t.Fatalf("offset: %v", got)
	}

	// Гаднын org-ийн job харагдахгүй, өөрчлөгдөхгүй.
	if _, err := Get(ctx, gdb, OrgScope(orgA), b); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get cross-org: %v", err)
	}
	if _, err := Get(ctx, gdb, OrgScope(orgA), sys); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get system job from org scope: %v", err)
	}
	if err := Cancel(ctx, gdb, OrgScope(orgA), b); !errors.Is(err, ErrNotFound) {
		t.Errorf("Cancel cross-org: %v", err)
	}
	if err := Retry(ctx, gdb, OrgScope(orgA), b); !errors.Is(err, ErrNotFound) {
		t.Errorf("Retry cross-org: %v", err)
	}
	if getJob(t, gdb, b).Status != StatusPending {
		t.Fatal("cross-org cancel нөлөөлсөн")
	}
	if err := Cancel(ctx, gdb, OrgScope(orgA), a); err != nil {
		t.Fatal(err)
	}
	if err := Cancel(ctx, gdb, OrgScope(orgA), 999999999); !errors.Is(err, ErrNotFound) {
		t.Errorf("байхгүй: %v", err)
	}

	stats, err := QueueStats(ctx, gdb, OrgScope(orgA))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range stats {
		if s.Queue == q && (s.Canceled != 1 || s.Pending != 0) {
			t.Fatalf("org A stats: %+v", s)
		}
	}
}

func TestAdminRetryCancelStates(t *testing.T) {
	gdb := testDB(t)
	q := testQueue(t, gdb)
	ctx := context.Background()
	id := enqueueOne(t, gdb, Job{Queue: q, Kind: testKind, MaxAttempts: 1, UniqueKey: "u"})

	if err := Retry(ctx, gdb, SystemScope(), id); !errors.Is(err, ErrState) {
		t.Fatalf("pending retry: %v", err)
	}
	w := testWorker(gdb, q)
	w.Register(testKind, func(context.Context, Record) error { return errors.New("fail") })
	w.RunOnce(ctx)
	if getJob(t, gdb, id).Status != StatusDead {
		t.Fatal("dead хүлээсэн")
	}
	if err := Cancel(ctx, gdb, SystemScope(), id); !errors.Is(err, ErrState) {
		t.Fatalf("dead cancel: %v", err)
	}
	// Retry: dead → pending, оролдлого 0.
	if err := Retry(ctx, gdb, SystemScope(), id); err != nil {
		t.Fatal(err)
	}
	r := getJob(t, gdb, id)
	if r.Status != StatusPending || r.Attempts != 0 || r.FinishedAt != nil {
		t.Fatalf("%+v", r)
	}
	ok := false
	w2 := testWorker(gdb, q)
	w2.Register(testKind, func(context.Context, Record) error { ok = true; return nil })
	w2.RunOnce(ctx)
	if !ok || getJob(t, gdb, id).Status != StatusSucceeded {
		t.Fatal("retry хийсэн job ажиллаагүй")
	}
	if err := Retry(ctx, gdb, SystemScope(), id); !errors.Is(err, ErrState) {
		t.Fatalf("succeeded retry: %v", err)
	}
	if err := Cancel(ctx, gdb, SystemScope(), id); !errors.Is(err, ErrState) {
		t.Fatalf("succeeded cancel: %v", err)
	}
}

func TestAdminRetryDuplicateKey(t *testing.T) {
	gdb := testDB(t)
	q := testQueue(t, gdb)
	ctx := context.Background()
	first := enqueueOne(t, gdb, Job{Queue: q, Kind: testKind, UniqueKey: "same"})
	if err := Cancel(ctx, gdb, SystemScope(), first); err != nil {
		t.Fatal(err)
	}
	// Цуцлагдсан тул түлхүүр чөлөөлөгдсөн — шинэ идэвхтэй job үүснэ.
	second := enqueueOne(t, gdb, Job{Queue: q, Kind: testKind, UniqueKey: "same"})
	if second == first {
		t.Fatal("шинэ job хүлээсэн")
	}
	if err := Retry(ctx, gdb, SystemScope(), first); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("got %v", err)
	}
	if getJob(t, gdb, first).Status != StatusCanceled {
		t.Fatal("төлөв өөрчлөгдсөн")
	}
}

func TestQueueStatsAndPurge(t *testing.T) {
	gdb := testDB(t)
	q := testQueue(t, gdb)
	ctx := context.Background()
	enqueueOne(t, gdb, Job{Queue: q, Kind: testKind, RunAt: time.Now().Add(-30 * time.Second)}) // ready, хуучин
	enqueueOne(t, gdb, Job{Queue: q, Kind: testKind, Delay: time.Hour})                         // delayed
	enqueueOne(t, gdb, Job{Queue: q, Kind: testKind, Priority: 9})
	dead := enqueueOne(t, gdb, Job{Queue: q, Kind: testKind, MaxAttempts: 1, Priority: 8})

	w := testWorker(gdb, q)
	w.Concurrency = 1
	w.Register(testKind, func(_ context.Context, j Record) error {
		if j.ID == dead {
			return errors.New("x")
		}
		return nil
	})
	w.RunOnce(ctx) // priority 9 → ok
	w.RunOnce(ctx) // priority 8 → dead

	stats, err := QueueStats(ctx, gdb, SystemScope())
	if err != nil {
		t.Fatal(err)
	}
	var s *QueueStat
	for i := range stats {
		if stats[i].Queue == q {
			s = &stats[i]
		}
	}
	if s == nil {
		t.Fatal("queue stats-д алга")
	}
	if s.Pending != 2 || s.Ready != 1 || s.Succeeded != 1 || s.Dead != 1 || s.Running != 0 || s.OldestReadyAge < 29*time.Second {
		t.Fatalf("%+v", *s)
	}

	// Purge: succeeded-г устгана, dead-г хөдөлгөхгүй.
	gdb.Exec(`UPDATE khan.jobs SET finished_at = now() - interval '2 hours' WHERE queue = ? AND status IN ('succeeded','dead')`, q)
	n, err := Purge(ctx, gdb, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if n < 1 {
		t.Fatalf("purge=%d", n)
	}
	if countStatus(t, gdb, q, StatusSucceeded) != 0 || countStatus(t, gdb, q, StatusDead) != 1 {
		t.Fatal("purge буруу төлөв устгав")
	}
	if _, err := Purge(ctx, gdb, time.Hour, StatusDead); err != nil {
		t.Fatal(err)
	}
	if countStatus(t, gdb, q, StatusDead) != 0 {
		t.Fatal("dead purge")
	}
	if countStatus(t, gdb, q, StatusPending) != 2 {
		t.Fatal("pending устсан")
	}
	if _, err := Purge(ctx, gdb, time.Hour, StatusPending); err == nil {
		t.Error("pending purge зөвшөөрөгдсөн")
	}
	if _, err := Purge(ctx, gdb, 0); err == nil {
		t.Error("olderThan=0")
	}
}

func TestQueueStatsExpiredLease(t *testing.T) {
	gdb := testDB(t)
	q := testQueue(t, gdb)
	id := enqueueOne(t, gdb, Job{Queue: q, Kind: testKind})
	gdb.Exec(`UPDATE khan.jobs SET status='running', attempts=1, locked_by='g', locked_until=now()-interval '1 minute' WHERE id = ?`, id)
	stats, err := QueueStats(context.Background(), gdb, SystemScope())
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range stats {
		if s.Queue == q && (s.Running != 1 || s.ExpiredLeases != 1) {
			t.Fatalf("%+v", s)
		}
	}
}
