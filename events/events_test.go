package events

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/khan-vision/khan/testkit"
	"gorm.io/gorm"
)

const testOrg = "11111111-1111-1111-1111-111111111111"

// ensureSchema — SchemaSQL-г advisory lock-оор (бусад package-ийн тесттэй
// зэрэг CREATE ... IF NOT EXISTS уралдахаас сэргийлж) ажиллуулна.
func ensureSchema(t *testing.T, tx *gorm.DB) {
	t.Helper()
	if err := tx.Exec(`SELECT pg_advisory_xact_lock(1802398062)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := tx.Exec(SchemaSQL).Error; err != nil {
		t.Fatal(err)
	}
}

// txDispatcher — rollback-д орох тусгай tx дээр, хоосон outbox-той Dispatcher.
func txDispatcher(t *testing.T) (*Dispatcher, *gorm.DB) {
	t.Helper()
	tx := testkit.TxDB(t)
	ensureSchema(t, tx)
	if err := tx.Exec(`DELETE FROM khan.outbox_events`).Error; err != nil {
		t.Fatal(err)
	}
	return &Dispatcher{DB: tx, Backoff: func(int) time.Duration { return 0 }}, tx
}

type state struct {
	Processed, Dead *time.Time
	Attempts        int
	LastError       *string
	Available       time.Time
}

func rowState(t *testing.T, tx *gorm.DB, topic string) state {
	t.Helper()
	var s state
	err := tx.Raw(`SELECT processed_at AS processed, dead_at AS dead, attempts, last_error, available_at AS available
		FROM khan.outbox_events WHERE topic = ?`, topic).Scan(&s).Error
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestPublishValidation(t *testing.T) {
	for _, topic := range []string{"", "Finance.Invoice", "a..b", ".a", "a.", "a b", "a-b", "1a", "a.*"} {
		if err := Publish(&gorm.DB{}, Event{Topic: topic}); !errors.Is(err, ErrInvalidTopic) {
			t.Errorf("%q: ErrInvalidTopic хүлээсэн, %v", topic, err)
		}
	}
	if err := Publish(nil, Event{Topic: "a.b"}); err == nil {
		t.Error("nil db: алдаа хүлээсэн")
	}
}

func TestHandleValidation(t *testing.T) {
	d := &Dispatcher{}
	ok := func(context.Context, Delivered) error { return nil }
	for _, topic := range []string{"a.b", "a.*", "*", "finance"} {
		if err := d.Handle(topic, ok); err != nil {
			t.Errorf("%q: %v", topic, err)
		}
	}
	for _, topic := range []string{"", "A.b", "a.**", ".*", "a*"} {
		if err := d.Handle(topic, ok); !errors.Is(err, ErrInvalidTopic) {
			t.Errorf("%q: ErrInvalidTopic хүлээсэн, %v", topic, err)
		}
	}
	if err := d.Handle("a.b", nil); err == nil {
		t.Error("nil handler: алдаа хүлээсэн")
	}
}

func TestRouteMatching(t *testing.T) {
	d := &Dispatcher{}
	var got []string
	mk := func(name string) Handler {
		return func(context.Context, Delivered) error { got = append(got, name); return nil }
	}
	_ = d.Handle("finance.invoice.posted", mk("exact"))
	_ = d.Handle("finance.*", mk("fin"))
	_ = d.Handle("fin.*", mk("fin2"))
	_ = d.Handle("*", mk("all"))
	for _, h := range d.handlersFor("finance.invoice.posted") {
		_ = h(context.Background(), Delivered{})
	}
	if strings.Join(got, ",") != "exact,fin,all" {
		t.Fatalf("got %v", got)
	}
	if n := len(d.handlersFor("finance")); n != 1 { // зөвхөн "*"
		t.Fatalf("finance: %d", n)
	}
}

func TestDefaultBackoff(t *testing.T) {
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}
	for i, w := range want {
		if got := DefaultBackoff(i + 1); got != w {
			t.Errorf("attempt %d: %v != %v", i+1, got, w)
		}
	}
	for _, a := range []int{10, 21, 1000} {
		if got := DefaultBackoff(a); got != maxBackoff {
			t.Errorf("attempt %d: %v", a, got)
		}
	}
}

func TestPublishRequiresTx(t *testing.T) {
	gdb := testkit.DB(t)
	if err := Publish(gdb, Event{Topic: "a.b"}); !errors.Is(err, ErrNotInTx) {
		t.Fatalf("ErrNotInTx хүлээсэн: %v", err)
	}
}

func TestPublishRollbackLeavesNothing(t *testing.T) {
	gdb := testkit.DB(t)
	setup := gdb.Begin()
	ensureSchema(t, setup)
	if err := setup.Commit().Error; err != nil {
		t.Fatal(err)
	}
	topic := fmt.Sprintf("test.rollback_%d", time.Now().UnixNano())
	t.Cleanup(func() { gdb.Exec(`DELETE FROM khan.outbox_events WHERE topic = ?`, topic) })

	errBoom := errors.New("boom")
	err := gdb.Transaction(func(tx *gorm.DB) error {
		if err := Publish(tx, Event{Topic: topic, Payload: map[string]int{"a": 1}}); err != nil {
			return err
		}
		return errBoom
	})
	if !errors.Is(err, errBoom) {
		t.Fatal(err)
	}
	var n int64
	gdb.Table("khan.outbox_events").Where("topic = ?", topic).Count(&n)
	if n != 0 {
		t.Fatalf("rollback-ийн дараа %d мөр үлдсэн", n)
	}
	// Commit хийвэл үлдэнэ.
	if err := gdb.Transaction(func(tx *gorm.DB) error {
		return Publish(tx, Event{Topic: topic})
	}); err != nil {
		t.Fatal(err)
	}
	gdb.Table("khan.outbox_events").Where("topic = ?", topic).Count(&n)
	if n != 1 {
		t.Fatalf("commit-ийн дараа %d мөр", n)
	}
}

func TestDispatchSuccess(t *testing.T) {
	d, tx := txDispatcher(t)
	err := Publish(tx, Event{
		OrgID: testOrg, Topic: "test.inv.posted", AggregateType: "invoice", AggregateID: "42",
		Payload: map[string]any{"amount": "10.50", "note": `a\b"c`}, Headers: map[string]string{"trace": "t1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var got Delivered
	_ = d.Handle("test.inv.posted", func(_ context.Context, e Delivered) error { got = e; return nil })

	n, err := d.ProcessOnce(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if got.OrgID != testOrg || got.AggregateType != "invoice" || got.AggregateID != "42" ||
		got.Headers["trace"] != "t1" || got.Attempts != 1 || got.ID == 0 || got.CreatedAt.IsZero() {
		t.Fatalf("delivered = %+v", got)
	}
	var p struct{ Amount, Note string }
	if err := got.Decode(&p); err != nil || p.Amount != "10.50" || p.Note != `a\b"c` {
		t.Fatalf("decode %+v %v", p, err)
	}
	if s := rowState(t, tx, "test.inv.posted"); s.Processed == nil {
		t.Fatal("processed_at тавигдаагүй")
	}
	if n, _ := d.ProcessOnce(context.Background()); n != 0 {
		t.Fatalf("дахин хүргэсэн: %d", n)
	}
}

func TestDispatchRetryThenSuccess(t *testing.T) {
	d, tx := txDispatcher(t)
	if err := Publish(tx, Event{Topic: "test.retry", Payload: 1}); err != nil {
		t.Fatal(err)
	}
	var calls, lastAttempt int
	_ = d.Handle("test.retry", func(_ context.Context, e Delivered) error {
		calls++
		lastAttempt = e.Attempts
		if calls < 3 {
			return errors.New("түр алдаа")
		}
		return nil
	})
	for i := 1; i <= 2; i++ {
		if n, err := d.ProcessOnce(context.Background()); err != nil || n != 0 {
			t.Fatalf("n=%d err=%v", n, err)
		}
		s := rowState(t, tx, "test.retry")
		if s.Attempts != i || s.Processed != nil || s.Dead != nil || s.LastError == nil || *s.LastError != "түр алдаа" {
			t.Fatalf("after fail %d: %+v", i, s)
		}
	}
	if n, err := d.ProcessOnce(context.Background()); err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	s := rowState(t, tx, "test.retry")
	if s.Processed == nil || s.Attempts != 2 || s.LastError != nil || lastAttempt != 3 {
		t.Fatalf("final: %+v lastAttempt=%d", s, lastAttempt)
	}
}

func TestDispatchBackoffDefersEvent(t *testing.T) {
	d, tx := txDispatcher(t)
	d.Backoff = func(int) time.Duration { return time.Hour }
	if err := Publish(tx, Event{Topic: "test.defer"}); err != nil {
		t.Fatal(err)
	}
	var calls int
	_ = d.Handle("test.defer", func(context.Context, Delivered) error { calls++; return errors.New("x") })
	_, _ = d.ProcessOnce(context.Background())
	_, _ = d.ProcessOnce(context.Background())
	if calls != 1 {
		t.Fatalf("backoff дотор дахин оролдсон: %d", calls)
	}
	if s := rowState(t, tx, "test.defer"); !s.Available.After(time.Now().Add(50 * time.Minute)) {
		t.Fatalf("available_at = %v", s.Available)
	}
}

func TestDispatchDeadAfterMaxAttempts(t *testing.T) {
	d, tx := txDispatcher(t)
	d.MaxAttempts = 2
	if err := Publish(tx, Event{Topic: "test.dead"}); err != nil {
		t.Fatal(err)
	}
	var calls int
	_ = d.Handle("test.dead", func(context.Context, Delivered) error { calls++; return errors.New("үргэлж унана") })
	for i := 0; i < 4; i++ {
		if n, err := d.ProcessOnce(context.Background()); err != nil || n != 0 {
			t.Fatalf("n=%d err=%v", n, err)
		}
	}
	s := rowState(t, tx, "test.dead")
	if calls != 2 || s.Dead == nil || s.Processed != nil || s.Attempts != 2 {
		t.Fatalf("calls=%d %+v", calls, s)
	}
}

func TestDispatchPanicRecovered(t *testing.T) {
	d, tx := txDispatcher(t)
	if err := Publish(tx, Event{Topic: "test.panic"}); err != nil {
		t.Fatal(err)
	}
	_ = d.Handle("test.panic", func(context.Context, Delivered) error { panic("гай") })
	if n, err := d.ProcessOnce(context.Background()); err != nil || n != 0 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	s := rowState(t, tx, "test.panic")
	if s.Attempts != 1 || s.LastError == nil || !strings.Contains(*s.LastError, "гай") {
		t.Fatalf("%+v", s)
	}
}

func TestDispatchWildcard(t *testing.T) {
	d, tx := txDispatcher(t)
	for _, topic := range []string{"test.wild.a", "test.wild.b.c", "test.other"} {
		if err := Publish(tx, Event{Topic: topic}); err != nil {
			t.Fatal(err)
		}
	}
	var seen []string
	_ = d.Handle("test.wild.*", func(_ context.Context, e Delivered) error { seen = append(seen, e.Topic); return nil })
	_, err := d.ProcessOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(seen, ",") != "test.wild.a,test.wild.b.c" {
		t.Fatalf("seen = %v", seen)
	}
}

func TestNoHandlerPolicy(t *testing.T) {
	d, tx := txDispatcher(t)
	if err := Publish(tx, Event{Topic: "test.nobody"}); err != nil {
		t.Fatal(err)
	}
	// OnlyRegistered: claim хийхгүй.
	d.OnlyRegistered = true
	_ = d.Handle("test.somebody", func(context.Context, Delivered) error { return nil })
	if n, err := d.ProcessOnce(context.Background()); err != nil || n != 0 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if s := rowState(t, tx, "test.nobody"); s.Processed != nil {
		t.Fatal("OnlyRegistered үед handler-гүй event хөндөгдсөн")
	}
	// Анхдагч: боловсруулсанд тооцно.
	d.OnlyRegistered = false
	if n, err := d.ProcessOnce(context.Background()); err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if s := rowState(t, tx, "test.nobody"); s.Processed == nil {
		t.Fatal("handler-гүй event processed болоогүй")
	}
}

func TestOnlyRegisteredWildcardEscapes(t *testing.T) {
	d, tx := txDispatcher(t)
	d.OnlyRegistered = true
	for _, topic := range []string{"my_mod.x", "myxmod.x"} {
		if err := Publish(tx, Event{Topic: topic}); err != nil {
			t.Fatal(err)
		}
	}
	var seen []string
	_ = d.Handle("my_mod.*", func(_ context.Context, e Delivered) error { seen = append(seen, e.Topic); return nil })
	if _, err := d.ProcessOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || seen[0] != "my_mod.x" {
		t.Fatalf("seen = %v", seen)
	}
}

func TestBatchSizeAndPurge(t *testing.T) {
	d, tx := txDispatcher(t)
	d.BatchSize = 2
	for i := 0; i < 3; i++ {
		if err := Publish(tx, Event{Topic: "test.batch"}); err != nil {
			t.Fatal(err)
		}
	}
	_ = d.Handle("test.batch", func(context.Context, Delivered) error { return nil })
	if n, _ := d.ProcessOnce(context.Background()); n != 2 {
		t.Fatalf("n=%d", n)
	}
	if n, _ := d.ProcessOnce(context.Background()); n != 1 {
		t.Fatalf("n=%d", n)
	}
	// 2 мөрийг "хуучин" болгоод purge.
	if err := tx.Exec(`UPDATE khan.outbox_events SET processed_at = now() - interval '2 days'
		WHERE id IN (SELECT id FROM khan.outbox_events ORDER BY id LIMIT 2)`).Error; err != nil {
		t.Fatal(err)
	}
	n, err := Purge(context.Background(), tx, 24*time.Hour)
	if err != nil || n != 2 {
		t.Fatalf("purge n=%d err=%v", n, err)
	}
	var left int64
	tx.Table("khan.outbox_events").Count(&left)
	if left != 1 {
		t.Fatalf("left = %d", left)
	}
}

func TestRunStopsOnContext(t *testing.T) {
	d, tx := txDispatcher(t)
	d.PollInterval = 10 * time.Millisecond
	if err := Publish(tx, Event{Topic: "test.run"}); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	_ = d.Handle("test.run", func(context.Context, Delivered) error { close(done); return nil })
	ctx, cancel := context.WithCancel(context.Background())
	res := make(chan error, 1)
	go func() { res <- d.Run(ctx) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run event хүргээгүй")
	}
	cancel()
	select {
	case err := <-res:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run зогссонгүй")
	}
}

// Хоёр зэрэг ProcessOnce нэг event-ийг хоёр удаа хүргэхгүй (SKIP LOCKED).
func TestConcurrentProcessOnceNoDuplicates(t *testing.T) {
	gdb := testkit.DB(t)
	setup := gdb.Begin()
	ensureSchema(t, setup)
	if err := setup.Commit().Error; err != nil {
		t.Fatal(err)
	}
	topic := fmt.Sprintf("test.conc_%d", time.Now().UnixNano())
	t.Cleanup(func() { gdb.Exec(`DELETE FROM khan.outbox_events WHERE topic = ?`, topic) })

	const total = 40
	if err := gdb.Transaction(func(tx *gorm.DB) error {
		for i := 0; i < total; i++ {
			if err := Publish(tx, Event{Topic: topic, Payload: i}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	seen := map[int64]int{}
	var inflight, maxInflight atomic.Int32
	handler := func(context.Context, Delivered) error { return nil }
	mk := func() *Dispatcher {
		d := &Dispatcher{DB: gdb, BatchSize: 5, OnlyRegistered: true}
		_ = d.Handle(topic, func(ctx context.Context, e Delivered) error {
			cur := inflight.Add(1)
			for {
				m := maxInflight.Load()
				if cur <= m || maxInflight.CompareAndSwap(m, cur) {
					break
				}
			}
			time.Sleep(5 * time.Millisecond)
			inflight.Add(-1)
			mu.Lock()
			seen[e.ID]++
			mu.Unlock()
			return handler(ctx, e)
		})
		return d
	}
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d := mk()
			for {
				n, err := d.ProcessOnce(context.Background())
				if err != nil {
					t.Error(err)
					return
				}
				if n == 0 {
					return
				}
			}
		}()
	}
	wg.Wait()
	if len(seen) != total {
		t.Fatalf("хүргэгдсэн %d / %d", len(seen), total)
	}
	for id, c := range seen {
		if c != 1 {
			t.Errorf("event %d %d удаа хүргэгдсэн", id, c)
		}
	}
	if maxInflight.Load() < 2 {
		t.Log("анхааруулга: зэрэг боловсруулалт ажиглагдсангүй")
	}
}
