package migrate

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/khan-vision/khan/testkit"
	"gorm.io/gorm"
)

// env — тестийн DB, санамсаргүй table/schema нэр; дуусахад DROP.
type env struct {
	gdb    *gorm.DB
	opts   Options
	schema string
}

func newEnv(t *testing.T) env {
	t.Helper()
	gdb := testkit.DB(t)
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	id := hex.EncodeToString(b)
	e := env{
		gdb:    gdb,
		schema: "khan_mt_" + id,
		opts:   Options{Table: "khan.schema_migrations_test_" + id},
	}
	t.Cleanup(func() {
		gdb.Exec("DROP TABLE IF EXISTS " + e.opts.Table)
		gdb.Exec("DROP SCHEMA IF EXISTS " + e.schema + " CASCADE")
	})
	return e
}

func (e env) load(t *testing.T, files map[string]string) []Migration {
	t.Helper()
	fsys := fstest.MapFS{}
	for name, body := range files {
		fsys[name] = &fstest.MapFile{Data: []byte(strings.ReplaceAll(body, "$S", e.schema))}
	}
	ms, err := Load(fsys)
	if err != nil {
		t.Fatal(err)
	}
	return ms
}

func (e env) count(t *testing.T) int64 {
	t.Helper()
	var n int64
	if err := e.gdb.Raw("SELECT count(*) FROM " + e.opts.Table).Scan(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

func TestUpApplyAndIdempotent(t *testing.T) {
	e := newEnv(t)
	ms := e.load(t, map[string]string{
		"0001_schema.sql": "CREATE SCHEMA $S;",
		"0002_table.sql":  "CREATE TABLE $S.a (id int primary key); INSERT INTO $S.a VALUES (1);",
	})
	ctx := context.Background()

	st, err := StatusOf(ctx, e.gdb, ms, e.opts)
	if err != nil || st[0].Applied || st[1].Applied {
		t.Fatalf("өмнөх status = %+v err = %v", st, err)
	}
	var logs []string
	e.opts.Logf = func(f string, a ...any) { logs = append(logs, f) }
	applied, err := Up(ctx, e.gdb, ms, e.opts)
	if err != nil || len(applied) != 2 || len(logs) == 0 {
		t.Fatalf("applied = %d err = %v logs = %d", len(applied), err, len(logs))
	}
	if n := e.count(t); n != 2 {
		t.Fatalf("rows = %d", n)
	}
	st, err = StatusOf(ctx, e.gdb, ms, e.opts)
	if err != nil || !st[0].Applied || !st[1].Applied || st[1].AppliedAt == nil {
		t.Fatalf("status = %+v err = %v", st, err)
	}
	applied, err = Up(ctx, e.gdb, ms, e.opts)
	if err != nil || len(applied) != 0 {
		t.Fatalf("дахин: applied = %d err = %v", len(applied), err)
	}
	p, err := Pending(ctx, e.gdb, ms, e.opts)
	if err != nil || len(p) != 0 {
		t.Fatalf("pending = %d err = %v", len(p), err)
	}
}

func TestUpConcurrentSerialized(t *testing.T) {
	e := newEnv(t)
	ms := e.load(t, map[string]string{
		"0001_schema.sql": "CREATE SCHEMA $S;",
		"0002_table.sql":  "CREATE TABLE $S.a (id int); SELECT pg_sleep(0.3);",
	})
	var wg sync.WaitGroup
	errs := make([]error, 3)
	counts := make([]int, 3)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a, err := Up(context.Background(), e.gdb, ms, e.opts)
			errs[i], counts[i] = err, len(a)
		}()
	}
	wg.Wait()
	total := 0
	for i := range errs {
		if errs[i] != nil {
			t.Fatalf("up %d: %v", i, errs[i])
		}
		total += counts[i]
	}
	if total != 2 {
		t.Fatalf("нийт хэрэглэгдсэн = %d, 2 байх ёстой", total)
	}
}

func TestUpDrift(t *testing.T) {
	e := newEnv(t)
	ms := e.load(t, map[string]string{"0001_schema.sql": "CREATE SCHEMA $S;"})
	if _, err := Up(context.Background(), e.gdb, ms, e.opts); err != nil {
		t.Fatal(err)
	}
	changed := e.load(t, map[string]string{
		"0001_schema.sql": "CREATE SCHEMA IF NOT EXISTS $S;",
		"0002_more.sql":   "CREATE TABLE $S.never (id int);",
	})
	applied, err := Up(context.Background(), e.gdb, changed, e.opts)
	if !errors.Is(err, ErrDrift) || len(applied) != 0 {
		t.Fatalf("applied = %d err = %v", len(applied), err)
	}
	var reg *string
	e.gdb.Raw("SELECT to_regclass(?)::text", e.schema+".never").Scan(&reg)
	if reg != nil {
		t.Fatal("drift үед юу ч ажиллах ёсгүй")
	}
	if _, err := Pending(context.Background(), e.gdb, changed, e.opts); !errors.Is(err, ErrDrift) {
		t.Fatalf("pending err = %v", err)
	}
}

func TestUpMissing(t *testing.T) {
	e := newEnv(t)
	ms := e.load(t, map[string]string{
		"0001_schema.sql": "CREATE SCHEMA $S;",
		"0002_table.sql":  "CREATE TABLE $S.a (id int);",
	})
	ctx := context.Background()
	if _, err := Up(ctx, e.gdb, ms, e.opts); err != nil {
		t.Fatal(err)
	}
	if _, err := Up(ctx, e.gdb, ms[:1], e.opts); !errors.Is(err, ErrMissing) {
		t.Fatalf("err = %v", err)
	}
	e.opts.AllowMissing = true
	if _, err := Up(ctx, e.gdb, ms[:1], e.opts); err != nil {
		t.Fatalf("AllowMissing: %v", err)
	}
}

func TestUpFailureRollsBackOnlyItself(t *testing.T) {
	e := newEnv(t)
	ms := e.load(t, map[string]string{
		"0001_schema.sql": "CREATE SCHEMA $S;",
		"0002_ok.sql":     "CREATE TABLE $S.ok (id int);",
		"0003_bad.sql":    "CREATE TABLE $S.half (id int); SELECT 1/0;",
		"0004_after.sql":  "CREATE TABLE $S.after (id int);",
	})
	applied, err := Up(context.Background(), e.gdb, ms, e.opts)
	if err == nil || !strings.Contains(err.Error(), "0003_bad") || len(applied) != 2 {
		t.Fatalf("applied = %d err = %v", len(applied), err)
	}
	if n := e.count(t); n != 2 {
		t.Fatalf("rows = %d, 2 байх ёстой", n)
	}
	for tbl, want := range map[string]bool{"ok": true, "half": false, "after": false} {
		var reg *string
		e.gdb.Raw("SELECT to_regclass(?)::text", e.schema+"."+tbl).Scan(&reg)
		if (reg != nil) != want {
			t.Errorf("%s: exists = %v, want %v", tbl, reg != nil, want)
		}
	}
	pend, err := Pending(context.Background(), e.gdb, ms, e.opts)
	if err != nil || len(pend) != 2 || pend[0].Version != 3 {
		t.Fatalf("pending = %+v err = %v", pend, err)
	}
}

func TestUpAppliesTimeouts(t *testing.T) {
	e := newEnv(t)
	e.opts.LockTimeout = 1234 * time.Millisecond
	e.opts.StatementTimeout = 2 * time.Minute
	ms := e.load(t, map[string]string{
		"0001_schema.sql": "CREATE SCHEMA $S; CREATE TABLE $S.seen (lt text, st text);",
		"0002_probe.sql":  "INSERT INTO $S.seen VALUES (current_setting('lock_timeout'), current_setting('statement_timeout'));",
	})
	if _, err := Up(context.Background(), e.gdb, ms, e.opts); err != nil {
		t.Fatal(err)
	}
	var lt, st string
	row := e.gdb.Raw("SELECT lt, st FROM " + e.schema + ".seen").Row()
	if err := row.Scan(&lt, &st); err != nil {
		t.Fatal(err)
	}
	if lt != "1234ms" || st != "2min" {
		t.Fatalf("lock_timeout=%s statement_timeout=%s", lt, st)
	}
}

func TestUpLockTimeoutFailsFast(t *testing.T) {
	e := newEnv(t)
	e.opts.LockTimeout = 300 * time.Millisecond
	setup := e.load(t, map[string]string{"0001_schema.sql": "CREATE SCHEMA $S; CREATE TABLE $S.hot (id int);"})
	ctx := context.Background()
	if _, err := Up(ctx, e.gdb, setup, e.opts); err != nil {
		t.Fatal(err)
	}
	// Өөр холболт hot хүснэгтийг түгжинэ.
	sqlDB, _ := e.gdb.DB()
	blocker, err := sqlDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback()
	if _, err := blocker.ExecContext(ctx, "LOCK TABLE "+e.schema+".hot IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatal(err)
	}
	all := e.load(t, map[string]string{
		"0001_schema.sql": "CREATE SCHEMA $S; CREATE TABLE $S.hot (id int);",
		"0002_alter.sql":  "ALTER TABLE $S.hot ADD COLUMN x int;",
	})
	start := time.Now()
	applied, err := Up(ctx, e.gdb, all, e.opts)
	if err == nil || len(applied) != 0 || !strings.Contains(err.Error(), "lock timeout") {
		t.Fatalf("applied = %d err = %v", len(applied), err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("хурдан унах ёстой, %s болов", d)
	}
}

func TestUpNoTransactionConcurrentIndex(t *testing.T) {
	e := newEnv(t)
	e.opts.LockTimeout = 2 * time.Second
	ms := e.load(t, map[string]string{
		"0001_table.sql": "CREATE SCHEMA $S; CREATE TABLE $S.t (a int);",
		"0002_index.sql": "-- khan:no-transaction\nCREATE INDEX CONCURRENTLY t_a_idx ON $S.t (a);",
	})
	if !ms[1].NoTx {
		t.Fatal("NoTx танигдсангүй")
	}
	// Нэг холболттой pool: RESET нь session-д үнэхээр хийгдсэнийг шалгана.
	sqlDB, _ := e.gdb.DB()
	sqlDB.SetMaxOpenConns(1)
	defer sqlDB.SetMaxOpenConns(0)
	if _, err := Up(context.Background(), e.gdb, ms, e.opts); err != nil {
		t.Fatal(err)
	}
	var valid bool
	err := e.gdb.Raw(`SELECT i.indisvalid FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
		WHERE c.relname = 't_a_idx' AND c.relnamespace = ?::regnamespace`, e.schema).Row().Scan(&valid)
	if err != nil || !valid {
		t.Fatalf("index valid=%v err=%v", valid, err)
	}
	var lt string
	e.gdb.Raw("SHOW lock_timeout").Row().Scan(&lt)
	if lt != "0" {
		t.Fatalf("lock_timeout RESET хийгдсэнгүй: %q", lt)
	}
	if n := e.count(t); n != 2 {
		t.Fatalf("rows = %d", n)
	}
}

func TestUpNoTransactionFailureRecordsNothing(t *testing.T) {
	e := newEnv(t)
	ms := e.load(t, map[string]string{
		"0001_table.sql": "CREATE SCHEMA $S;",
		"0002_bad.sql":   "-- khan:no-transaction\nCREATE INDEX CONCURRENTLY x ON $S.missing (a);",
	})
	applied, err := Up(context.Background(), e.gdb, ms, e.opts)
	if err == nil || len(applied) != 1 || e.count(t) != 1 {
		t.Fatalf("applied = %d err = %v", len(applied), err)
	}
}
