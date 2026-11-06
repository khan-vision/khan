package jobs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/khan-vision/khan/testkit"
	"gorm.io/gorm"
)

const (
	orgA = "11111111-1111-1111-1111-111111111111"
	orgB = "22222222-2222-2222-2222-222222222222"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// testDB — committed schema-тай тестийн DB (DB тохируулаагүй бол skip).
// Schema-г advisory lock-оор (events зэрэг бусад package-ийн тесттэй уралдахгүй) үүсгэнэ.
func testDB(t *testing.T) *gorm.DB {
	t.Helper()
	gdb := testkit.DB(t)
	err := gdb.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`SELECT pg_advisory_xact_lock(1802398062)`).Error; err != nil {
			return err
		}
		return tx.Exec(SchemaSQL).Error
	})
	if err != nil {
		t.Fatal(err)
	}
	return gdb
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// testQueue — тест бүрт өвөрмөц queue; тест дуусахад түүний job-ууд устна.
func testQueue(t *testing.T, gdb *gorm.DB) string {
	t.Helper()
	q := "t-" + randHex(6)
	t.Cleanup(func() {
		gdb.Exec(`DELETE FROM khan.jobs WHERE queue = ?`, q)
	})
	return q
}

// testSchedName — тест бүрт өвөрмөц schedule нэр; дуусахад run мөр, job устна.
func testSchedName(t *testing.T, gdb *gorm.DB) string {
	t.Helper()
	n := "s-" + randHex(6)
	t.Cleanup(func() {
		gdb.Exec(`DELETE FROM khan.job_schedule_runs WHERE name = ?`, n)
		gdb.Exec(`DELETE FROM khan.jobs WHERE schedule = ?`, n)
	})
	return n
}

const testKind = "test.job"

// testWorker — хурдан poll/lease-тэй, backoff-гүй, чимээгүй Worker.
func testWorker(gdb *gorm.DB, queue string) *Worker {
	return &Worker{
		DB: gdb, Queues: []string{queue}, Concurrency: 4,
		PollInterval: 20 * time.Millisecond, Lease: 600 * time.Millisecond,
		ShutdownTimeout: 2 * time.Second,
		Backoff:         func(int) time.Duration { return 0 },
		Logger:          quiet,
	}
}

// enqueueOne — өөрийн транзакцад enqueue хийж commit хийнэ.
func enqueueOne(t *testing.T, gdb *gorm.DB, j Job) int64 {
	t.Helper()
	var id int64
	err := gdb.Transaction(func(tx *gorm.DB) error {
		var err error
		id, err = Enqueue(context.Background(), tx, j)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func getJob(t *testing.T, gdb *gorm.DB, id int64) Record {
	t.Helper()
	r, err := Get(context.Background(), gdb, SystemScope(), id)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func waitFor(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("хүлээлт дууслаа: %s", what)
}

func countStatus(t *testing.T, gdb *gorm.DB, queue string, st Status) int {
	t.Helper()
	var n int
	if err := gdb.Raw(`SELECT count(*) FROM khan.jobs WHERE queue = ? AND status = ?`, queue, string(st)).Scan(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}
