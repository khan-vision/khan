package obs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// MaxSQLLogLen — лог руу бичих SQL-ийн дээд урт (тэмдэгтээр).
const MaxSQLLogLen = 500

type gormLogger struct {
	base  *slog.Logger
	slow  time.Duration
	level logger.LogLevel
}

// GormLogger — GORM-ийн logger.Interface-ийг slog руу холбоно. Анхдагч түвшин Warn:
// алдаатай query (ErrRecordNotFound-ийг үл тооно) Error, `slow`-оос удаан query Warn
// (slow<=0 бол удаан query лог хийхгүй). LogMode(logger.Info) бол бүх query Debug-аар.
// ctx-д request-scoped logger эсвэл request id байвал request_id нэмэгдэнэ. SQL нь
// RedactString-ээр шүүгдэж MaxSQLLogLen-д тайрагдана.
func GormLogger(base *slog.Logger, slow time.Duration) logger.Interface {
	if base == nil {
		base = slog.Default()
	}
	return &gormLogger{base: base, slow: slow, level: logger.Warn}
}

func (g *gormLogger) LogMode(l logger.LogLevel) logger.Interface {
	cp := *g
	cp.level = l
	return &cp
}

func (g *gormLogger) log(ctx context.Context) *slog.Logger {
	if l, ok := fromOK(ctx); ok {
		return l
	}
	if id := RequestIDFrom(ctx); id != "" {
		return g.base.With("request_id", id)
	}
	return g.base
}

func (g *gormLogger) Info(ctx context.Context, msg string, args ...any) {
	if g.level >= logger.Info {
		g.log(ctx).Info(RedactString(fmt.Sprintf(msg, args...)))
	}
}

func (g *gormLogger) Warn(ctx context.Context, msg string, args ...any) {
	if g.level >= logger.Warn {
		g.log(ctx).Warn(RedactString(fmt.Sprintf(msg, args...)))
	}
}

func (g *gormLogger) Error(ctx context.Context, msg string, args ...any) {
	if g.level >= logger.Error {
		g.log(ctx).Error(RedactString(fmt.Sprintf(msg, args...)))
	}
}

func (g *gormLogger) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	if g.level <= logger.Silent {
		return
	}
	elapsed := time.Since(begin)
	switch {
	case err != nil && !errors.Is(err, gorm.ErrRecordNotFound) && g.level >= logger.Error:
		sql, rows := fc()
		g.log(ctx).Error("db query алдаа",
			"error", RedactString(err.Error()), "sql", TruncateSQL(sql), "rows", rows,
			"duration_ms", float64(elapsed.Microseconds())/1000)
	case g.slow > 0 && elapsed > g.slow && g.level >= logger.Warn:
		sql, rows := fc()
		g.log(ctx).Warn("db удаан query",
			"sql", TruncateSQL(sql), "rows", rows,
			"duration_ms", float64(elapsed.Microseconds())/1000, "threshold_ms", g.slow.Milliseconds())
	case g.level >= logger.Info:
		sql, rows := fc()
		g.log(ctx).Debug("db query",
			"sql", TruncateSQL(sql), "rows", rows, "duration_ms", float64(elapsed.Microseconds())/1000)
	}
}

// TruncateSQL — RedactString хийж, MaxSQLLogLen тэмдэгтээс уртыг "…"-ээр тайрна.
func TruncateSQL(sql string) string {
	sql = RedactString(sql)
	r := []rune(sql)
	if len(r) <= MaxSQLLogLen {
		return sql
	}
	return string(r[:MaxSQLLogLen]) + "…"
}
