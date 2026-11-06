// Package db нь PostgreSQL-д GORM холболт нээнэ (khan = Postgres-only).
//
// Lambda-ийн DB-ээс ялгаа:
//   - `init()`-д холбогдохгүй; Open() explicit, алдаа = error (log.Fatal биш).
//   - DSN-ийн утгуудыг libpq дүрмээр quote хийнэ (хоосон/зайтай password
//     эвдрэхгүй).
//   - Connection pool-ийг ҮРГЭЛЖ тохируулна (lambda MaxIdleConns=2 үлдээж,
//     managed PG-д холболт бүр ~105ms TLS+SCRAM handshake төлж байсан).
//   - Глобал `DB.DB` байхгүй — *gorm.DB-г module/handler-т дамжуулна.
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/khan-vision/khan/config"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// DefaultPool — prod-д хэмжиж тогтоосон анхдагч pool.
//
// DO managed PG `max_connections = 100`; апп ердийн үед ~24 холболт эзэлдэг,
// бусад хэрэглэгч ~10. 50 нь 2 дахин нөөцтэй бөгөөд "too many connections"-оос
// сэргийлнэ. Managed DB идэвхгүй холболтыг өөрөө тасалдаг тул idle/lifetime-аар
// идэвхтэй шинэчилж "connection reset by peer"-ээс сэргийлнэ.
var DefaultPool = config.Pool{
	MaxOpen:     50,
	MaxIdle:     25,
	MaxIdleTime: 10 * time.Minute,
	MaxLifetime: time.Hour,
}

// Option — Open-ийн тохиргоо.
type Option func(*options)

type options struct {
	gorm   *gorm.Config
	logger logger.Interface
	ping   bool
}

// WithGormConfig — өөрийн gorm.Config (Logger талбарыг WithLogger дарна).
func WithGormConfig(c *gorm.Config) Option { return func(o *options) { o.gorm = c } }

// WithLogger — GORM logger.
func WithLogger(l logger.Interface) Option { return func(o *options) { o.logger = l } }

// WithoutPing — Open үед холболтыг шалгахгүй (lazy).
func WithoutPing() Option { return func(o *options) { o.ping = false } }

// Open — config.DB-ээр PostgreSQL холболт нээж, pool тохируулж, ping хийнэ.
func Open(c config.DB, opts ...Option) (*gorm.DB, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	o := options{ping: true}
	for _, fn := range opts {
		fn(&o)
	}
	gc := &gorm.Config{}
	if o.gorm != nil {
		cp := *o.gorm
		gc = &cp
	}
	switch {
	case o.logger != nil:
		gc.Logger = o.logger
	case gc.Logger == nil:
		level := logger.Warn
		if c.Debug {
			level = logger.Info
		}
		gc.Logger = logger.New(log.New(os.Stdout, "\r\n", log.LstdFlags), logger.Config{
			SlowThreshold:             200 * time.Millisecond,
			LogLevel:                  level,
			IgnoreRecordNotFoundError: true,
		})
	}

	gdb, err := gorm.Open(postgres.Open(DSN(c)), gc)
	if err != nil {
		return nil, fmt.Errorf("db: нээж чадсангүй: %w", err)
	}
	sqlDB, err := gdb.DB()
	if err != nil {
		return nil, fmt.Errorf("db: sql.DB: %w", err)
	}
	ConfigurePool(sqlDB, c.Pool)
	if o.ping {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := sqlDB.PingContext(ctx); err != nil {
			_ = sqlDB.Close()
			return nil, fmt.Errorf("db: %s:%s/%s холбогдсонгүй: %w", c.Host, c.Port, c.Database, err)
		}
	}
	return gdb, nil
}

// Close — GORM-ийн доорх *sql.DB-г хаана.
func Close(gdb *gorm.DB) error {
	if gdb == nil {
		return nil
	}
	sqlDB, err := gdb.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

// ConfigurePool — тэг утгатай талбарт DefaultPool-ийг хэрэглэнэ. MaxIdle нь
// MaxOpen-оос их байж болохгүй.
func ConfigurePool(sqlDB *sql.DB, p config.Pool) config.Pool {
	if p.MaxOpen <= 0 {
		p.MaxOpen = DefaultPool.MaxOpen
	}
	if p.MaxIdle <= 0 {
		p.MaxIdle = DefaultPool.MaxIdle
	}
	if p.MaxIdle > p.MaxOpen {
		p.MaxIdle = p.MaxOpen
	}
	if p.MaxIdleTime <= 0 {
		p.MaxIdleTime = DefaultPool.MaxIdleTime
	}
	if p.MaxLifetime <= 0 {
		p.MaxLifetime = DefaultPool.MaxLifetime
	}
	sqlDB.SetMaxOpenConns(p.MaxOpen)
	sqlDB.SetMaxIdleConns(p.MaxIdle)
	sqlDB.SetConnMaxIdleTime(p.MaxIdleTime)
	sqlDB.SetConnMaxLifetime(p.MaxLifetime)
	return p
}

// DSN — libpq key=value холболтын мөр. Утга бүрийг quote хийж, Extra-г
// (ж: "sslmode=require application_name=khan") төгсгөлд нь залгана.
func DSN(c config.DB) string {
	parts := []string{
		"host=" + quote(c.Host),
		"port=" + quote(c.Port),
		"user=" + quote(c.User),
		"dbname=" + quote(c.Database),
		"password=" + quote(c.Password),
	}
	if extra := strings.TrimSpace(c.Extra); extra != "" {
		parts = append(parts, extra)
	}
	return strings.Join(parts, " ")
}

// quote — libpq: хоосон, зай, ' эсвэл \ агуулсан утгыг '...'-д хийж escape хийнэ.
func quote(v string) string {
	if v != "" && !strings.ContainsAny(v, " \t\n'\\") {
		return v
	}
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(v) + "'"
}

// ErrNilDB — nil *gorm.DB дамжуулсан.
var ErrNilDB = errors.New("db: *gorm.DB nil")

// Tx — fn-ийг нэг транзакцад ажиллуулна; fn алдаа буцаавал (эсвэл panic) rollback.
func Tx(ctx context.Context, gdb *gorm.DB, fn func(tx *gorm.DB) error) error {
	if gdb == nil {
		return ErrNilDB
	}
	return gdb.WithContext(ctx).Transaction(fn)
}
