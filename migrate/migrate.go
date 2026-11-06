// Package migrate нь PostgreSQL-д зориулсан хувилбартай, зөвхөн урагшаа (forward-only)
// SQL migration гүйцэтгэнэ.
//
// Анхдагчаар аюулгүй: migration бүр өөрийн транзакцид `SET LOCAL lock_timeout`
// (5s) болон `statement_timeout` (15m)-тэй ажиллана. Production-д hot хүснэгт
// (`chat_bots`) дээрх DDL lock_timeout-гүйгээс удаан түгжигдсэн явдлаас сэргийлнэ.
//
// Файлын нэр `NNNN_description.sql`; хувилбар = тэргүүн бүхэл тоо (давтагдахгүй).
// Файлын эхний хоосон биш мөр нь `-- khan:no-transaction` бол транзакцгүй ажиллана
// (`CREATE INDEX CONCURRENTLY`). Тийм файлд ЗӨВХӨН НЭГ statement бич: олон
// statement-тэй simple query нь далд транзакц үүсгэдэг тул CONCURRENTLY алдаа өгнө.
// Транзакцгүй migration дундаа унавал хагас үлдэгдэл (ж: INVALID index) үлдэж болно;
// дахин ажиллуулахаас өмнө гараар цэвэрлэ.
//
// Хяналтын хүснэгт (анхдагч `khan.schema_migrations`) нь хувилбар бүрийн checksum-ыг
// хадгална. Хэрэглэгдсэн migration-ы файл өөрчлөгдвөл (drift) юу ч ажиллуулахгүй
// алдаа буцаана.
package migrate

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
)

// Анхдагч утгууд.
const (
	DefaultTable            = "khan.schema_migrations"
	DefaultLockTimeout      = 5 * time.Second
	DefaultStatementTimeout = 15 * time.Minute

	// NoTransactionMarker — файлын эхний хоосон биш мөр.
	NoTransactionMarker = "-- khan:no-transaction"

	// advisoryLockKey — зэрэгцээ Up-аас хамгаалах pg_advisory_lock түлхүүр ("khanmigr").
	advisoryLockKey int64 = 0x6b68616e6d696772
)

// Алдааны төрлүүд (errors.Is-ээр шалгана).
var (
	ErrDrift   = errors.New("migrate: хэрэглэгдсэн migration өөрчлөгдсөн")
	ErrMissing = errors.New("migrate: хэрэглэгдсэн migration эх сурвалжид алга")
	ErrNilDB   = errors.New("migrate: *gorm.DB nil")
)

var (
	fileRe  = regexp.MustCompile(`^(\d+)_(.+)\.sql$`)
	identRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,62}$`)
)

// Migration — нэг SQL файл.
type Migration struct {
	Version  int64
	Name     string // файлын нэрний тайлбар хэсэг (NNNN_ ба .sql-гүй)
	Checksum string // файлын агуулгын sha256 (hex)
	SQL      string
	NoTx     bool // `-- khan:no-transaction`
}

// File — "0001_init" хэлбэрийн нэр (мэдээллийн мөр, алдаанд).
func (m Migration) File() string { return fmt.Sprintf("%04d_%s", m.Version, m.Name) }

// Load — fsys-ийн язгуур дахь `NNNN_name.sql` файлуудыг хувилбараар эрэмбэлж уншина.
// .sql биш файл болон дэд хавтсыг алгасна.
func Load(fsys fs.FS) ([]Migration, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("migrate: хавтас унших: %w", err)
	}
	var out []Migration
	seen := map[int64]string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		m := fileRe.FindStringSubmatch(e.Name())
		if m == nil {
			return nil, fmt.Errorf("migrate: файлын нэр буруу %q (хүлээгдэх хэлбэр: NNNN_description.sql)", e.Name())
		}
		ver, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("migrate: %q хувилбар хэт том: %w", e.Name(), err)
		}
		if prev, dup := seen[ver]; dup {
			return nil, fmt.Errorf("migrate: хувилбар %d давхардсан: %q ба %q", ver, prev, e.Name())
		}
		seen[ver] = e.Name()
		b, err := fs.ReadFile(fsys, e.Name())
		if err != nil {
			return nil, fmt.Errorf("migrate: %q унших: %w", e.Name(), err)
		}
		if strings.TrimSpace(string(b)) == "" {
			return nil, fmt.Errorf("migrate: %q хоосон", e.Name())
		}
		sum := sha256.Sum256(b)
		out = append(out, Migration{
			Version:  ver,
			Name:     m[2],
			Checksum: hex.EncodeToString(sum[:]),
			SQL:      string(b),
			NoTx:     firstLine(string(b)) == NoTransactionMarker,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

// firstLine — эхний хоосон биш мөр (trim хийсэн).
func firstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return ""
}

// Options — Up/StatusOf/Pending-ийн тохиргоо. Тэг утга = анхдагч.
type Options struct {
	// Table — хяналтын хүснэгт, "schema.name" эсвэл "name" (анхдагч khan.schema_migrations).
	Table string
	// LockTimeout — migration бүрийн SET LOCAL lock_timeout (0 = 5s, <0 = унтраана).
	LockTimeout time.Duration
	// StatementTimeout — statement_timeout (0 = 15m, <0 = унтраана).
	StatementTimeout time.Duration
	// AllowMissing — DB-д хэрэглэгдсэн боловч эх сурвалжид алга болсон хувилбарыг алдаа болгохгүй.
	AllowMissing bool
	// Logf — явцын мэдээлэл (nil = чимээгүй).
	Logf func(format string, args ...any)
}

func (o Options) logf(format string, args ...any) {
	if o.Logf != nil {
		o.Logf(format, args...)
	}
}

func (o Options) timeouts() (lock, stmt time.Duration) {
	lock, stmt = o.LockTimeout, o.StatementTimeout
	if lock == 0 {
		lock = DefaultLockTimeout
	}
	if stmt == 0 {
		stmt = DefaultStatementTimeout
	}
	if lock < 0 {
		lock = 0
	}
	if stmt < 0 {
		stmt = 0
	}
	return lock, stmt
}

// table — quote хийсэн "schema"."name" ба schema (хоосон байж болно).
func (o Options) table() (qualified, schemaName string, err error) {
	t := o.Table
	if t == "" {
		t = DefaultTable
	}
	parts := strings.Split(t, ".")
	if len(parts) > 2 {
		return "", "", fmt.Errorf("migrate: хүснэгтийн нэр буруу %q", t)
	}
	for i, p := range parts {
		if !identRe.MatchString(p) {
			return "", "", fmt.Errorf("migrate: хүснэгтийн нэр буруу %q (үсэг, тоо, _ ашигла)", t)
		}
		parts[i] = `"` + p + `"`
	}
	if len(parts) == 2 {
		return parts[0] + "." + parts[1], strings.Trim(parts[0], `"`), nil
	}
	return parts[0], "", nil
}

// Status — migration ба түүний төлөв.
type Status struct {
	Migration
	Applied   bool
	AppliedAt *time.Time
}

type appliedRow struct {
	Version   int64
	Name      string
	Checksum  string
	AppliedAt time.Time
}

// querier — *sql.Conn ба *sql.DB-д нийтлэг уншилт.
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func readApplied(ctx context.Context, q querier, qualified string, create bool) (map[int64]appliedRow, error) {
	if !create {
		var reg sql.NullString
		if err := q.QueryRowContext(ctx, "SELECT to_regclass($1)::text", qualified).Scan(&reg); err != nil {
			return nil, fmt.Errorf("migrate: хүснэгт шалгах: %w", err)
		}
		if !reg.Valid {
			return map[int64]appliedRow{}, nil
		}
	}
	rows, err := q.QueryContext(ctx, "SELECT version, name, checksum, applied_at FROM "+qualified)
	if err != nil {
		return nil, fmt.Errorf("migrate: хэрэглэгдсэнийг унших: %w", err)
	}
	defer rows.Close()
	out := map[int64]appliedRow{}
	for rows.Next() {
		var r appliedRow
		if err := rows.Scan(&r.Version, &r.Name, &r.Checksum, &r.AppliedAt); err != nil {
			return nil, fmt.Errorf("migrate: scan: %w", err)
		}
		out[r.Version] = r
	}
	return out, rows.Err()
}

// plan — эх сурвалж ба DB-ийг тулгаж Status жагсаалт гаргана; drift/missing бол алдаа.
func plan(ms []Migration, applied map[int64]appliedRow, o Options) ([]Status, error) {
	src := make(map[int64]bool, len(ms))
	out := make([]Status, 0, len(ms))
	for _, m := range ms {
		if src[m.Version] {
			return nil, fmt.Errorf("migrate: хувилбар %d давхардсан", m.Version)
		}
		src[m.Version] = true
		s := Status{Migration: m}
		if a, ok := applied[m.Version]; ok {
			if a.Checksum != m.Checksum {
				return nil, fmt.Errorf("%w: %s (DB: %.12s, файл: %.12s)", ErrDrift, m.File(), a.Checksum, m.Checksum)
			}
			t := a.AppliedAt
			s.Applied, s.AppliedAt = true, &t
		}
		out = append(out, s)
	}
	if !o.AllowMissing {
		var missing []string
		for v, a := range applied {
			if !src[v] {
				missing = append(missing, fmt.Sprintf("%04d_%s", v, a.Name))
			}
		}
		if len(missing) > 0 {
			sort.Strings(missing)
			return nil, fmt.Errorf("%w: %s", ErrMissing, strings.Join(missing, ", "))
		}
	}
	return out, nil
}

// StatusOf — migration бүрийн төлөв (DB-д бичихгүй; хүснэгт байхгүй бол бүгд pending).
func StatusOf(ctx context.Context, gdb *gorm.DB, ms []Migration, o Options) ([]Status, error) {
	if gdb == nil {
		return nil, ErrNilDB
	}
	qualified, _, err := o.table()
	if err != nil {
		return nil, err
	}
	sqlDB, err := gdb.DB()
	if err != nil {
		return nil, fmt.Errorf("migrate: sql.DB: %w", err)
	}
	applied, err := readApplied(ctx, sqlDB, qualified, false)
	if err != nil {
		return nil, err
	}
	return plan(ms, applied, o)
}

// Pending — хэрэглэгдээгүй migration-ууд (хувилбарын дарааллаар).
func Pending(ctx context.Context, gdb *gorm.DB, ms []Migration, o Options) ([]Migration, error) {
	st, err := StatusOf(ctx, gdb, ms, o)
	if err != nil {
		return nil, err
	}
	return pendingOf(st), nil
}

func pendingOf(st []Status) []Migration {
	var out []Migration
	for _, s := range st {
		if !s.Applied {
			out = append(out, s.Migration)
		}
	}
	return out
}

// Up — хэрэглэгдээгүй migration-уудыг дарааллаар ажиллуулна. Нэг тусгай холболт дээр
// pg_advisory_lock барьж зэрэгцээ Up-ыг хаана. Эхний алдаанд зогсож, хувилбарыг
// заасан алдаа (PG алдааг wrap хийсэн) болон тэр хүртэл амжилттай хэрэглэгдсэнийг буцаана.
func Up(ctx context.Context, gdb *gorm.DB, ms []Migration, o Options) (applied []Migration, err error) {
	if gdb == nil {
		return nil, ErrNilDB
	}
	qualified, schemaName, err := o.table()
	if err != nil {
		return nil, err
	}
	sqlDB, err := gdb.DB()
	if err != nil {
		return nil, fmt.Errorf("migrate: sql.DB: %w", err)
	}
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("migrate: холболт авах: %w", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_lock($1)", advisoryLockKey); err != nil {
		return nil, fmt.Errorf("migrate: advisory lock: %w", err)
	}
	defer func() {
		// ctx цуцлагдсан ч түгжээг суллана.
		uctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if _, uerr := conn.ExecContext(uctx, "SELECT pg_advisory_unlock($1)", advisoryLockKey); uerr != nil && err == nil {
			err = fmt.Errorf("migrate: advisory unlock: %w", uerr)
		}
	}()

	if schemaName != "" {
		if _, err := conn.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS "`+schemaName+`"`); err != nil {
			return nil, fmt.Errorf("migrate: schema үүсгэх: %w", err)
		}
	}
	ddl := `CREATE TABLE IF NOT EXISTS ` + qualified + ` (
	version     bigint PRIMARY KEY,
	name        text NOT NULL,
	checksum    text NOT NULL,
	applied_at  timestamptz NOT NULL DEFAULT now(),
	duration_ms bigint NOT NULL
)`
	if _, err := conn.ExecContext(ctx, ddl); err != nil {
		return nil, fmt.Errorf("migrate: хяналтын хүснэгт үүсгэх: %w", err)
	}

	rows, err := readApplied(ctx, conn, qualified, true)
	if err != nil {
		return nil, err
	}
	st, err := plan(ms, rows, o)
	if err != nil {
		return nil, err
	}
	lock, stmt := o.timeouts()
	for _, m := range pendingOf(st) {
		o.logf("migrate: %s ...", m.File())
		start := time.Now()
		if err := apply(ctx, conn, qualified, m, lock, stmt, start); err != nil {
			return applied, fmt.Errorf("migrate: %s алдаатай: %w", m.File(), err)
		}
		o.logf("migrate: %s ✓ (%s)", m.File(), time.Since(start).Round(time.Millisecond))
		applied = append(applied, m)
	}
	return applied, nil
}

func millis(d time.Duration) int64 { return d.Milliseconds() }

func insertSQL(qualified string) string {
	return "INSERT INTO " + qualified + " (version, name, checksum, duration_ms) VALUES ($1, $2, $3, $4)"
}

func apply(ctx context.Context, conn *sql.Conn, qualified string, m Migration, lock, stmt time.Duration, start time.Time) error {
	if m.NoTx {
		return applyNoTx(ctx, conn, qualified, m, lock, stmt, start)
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // commit-ийн дараа ErrTxDone
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("SET LOCAL lock_timeout = %d", millis(lock))); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("SET LOCAL statement_timeout = %d", millis(stmt))); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, m.SQL); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, insertSQL(qualified), m.Version, m.Name, m.Checksum, millis(time.Since(start))); err != nil {
		return err
	}
	return tx.Commit()
}

func applyNoTx(ctx context.Context, conn *sql.Conn, qualified string, m Migration, lock, stmt time.Duration, start time.Time) (err error) {
	if _, err := conn.ExecContext(ctx, fmt.Sprintf("SET lock_timeout = %d", millis(lock))); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, fmt.Sprintf("SET statement_timeout = %d", millis(stmt))); err != nil {
		return err
	}
	defer func() {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if _, rerr := conn.ExecContext(rctx, "RESET lock_timeout; RESET statement_timeout"); rerr != nil && err == nil {
			err = fmt.Errorf("timeout RESET: %w", rerr)
		}
	}()
	if _, err := conn.ExecContext(ctx, m.SQL); err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, insertSQL(qualified), m.Version, m.Name, m.Checksum, millis(time.Since(start)))
	return err
}
