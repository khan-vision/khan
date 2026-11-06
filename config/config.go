// Package config нь khan app-ийн typed тохиргоог орчны хувьсагчаас уншина.
//
// Lambda-ийн config-оос ялгаа:
//   - `init()` ДОТОР ЮУ Ч УНШИХГҮЙ — Load()-ийг explicit дуудна (тест, CLI,
//     олон instance аюулгүй).
//   - `.env` файлыг зөвхөн WithEnvFile өгсөн үед уншина; орчны хувьсагч файлыг
//     ДАРНА (godotenv.Load) — lambda `Overload` хийдэг байсан тул prod `.env`
//     тест/CLI-ийн env-ийг дардаг урхитай байв.
//   - Глобал хувьсагч байхгүй; алдаа = error (log.Fatal биш).
//
// Env нэрс lambda-тай ИЖИЛ (DB_HOST, DB_DATABASE, DB_USERNAME, JWT_SECRET,
// JWT_TTL, SYSADMIN_UUID, MAIL_*, FILE_MAX_SIZE …) тул шилжилтэд .env өөрчлөхгүй.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

// Config — khan app-ийн бүх тохиргоо.
type Config struct {
	App      App
	DB       DB
	JWT      JWT
	Mail     Mail
	Upload   Upload
	SysAdmin SysAdmin
}

// App — процессын тохиргоо.
type App struct {
	Name string // APP_NAME
	Port string // APP_PORT (анхдагч 8080)
	Env  string // APP_ENV: "production" | "development" | "test" (анхдагч production)
}

// IsProduction — APP_ENV production (эсвэл хоосон) эсэх.
func (a App) IsProduction() bool { return a.Env == "" || a.Env == "production" }

// DB — PostgreSQL холболт.
type DB struct {
	Host     string // DB_HOST
	Port     string // DB_PORT (анхдагч 5432)
	Database string // DB_DATABASE
	User     string // DB_USERNAME
	Password string // DB_PASSWORD
	// Extra — DSN-д нэмэх key=value хэсгүүд (DB_EXTRA, ж: "sslmode=require").
	Extra string
	Debug bool // DB_DEBUG — бүх SQL-ийг лог руу
	Pool  Pool
}

// Pool — database/sql connection pool. Анхдагч утгын шалтгаан → db.DefaultPool.
type Pool struct {
	MaxOpen     int           // DB_MAX_OPEN_CONNS
	MaxIdle     int           // DB_MAX_IDLE_CONNS
	MaxIdleTime time.Duration // DB_CONN_MAX_IDLE_TIME (Go duration, ж: "10m")
	MaxLifetime time.Duration // DB_CONN_MAX_LIFETIME
}

// Validate — холбогдоход заавал хэрэгтэй талбарууд.
func (d DB) Validate() error {
	var missing []string
	if d.Host == "" {
		missing = append(missing, "DB_HOST")
	}
	if d.Database == "" {
		missing = append(missing, "DB_DATABASE")
	}
	if d.User == "" {
		missing = append(missing, "DB_USERNAME")
	}
	if len(missing) > 0 {
		return fmt.Errorf("config: DB тохиргоо дутуу: %s", strings.Join(missing, ", "))
	}
	return nil
}

// JWT — access token.
type JWT struct {
	Secret string // JWT_SECRET (≥ MinJWTSecretLen)
	// TTL — access token-ий хугацаа. JWT_TTL нь lambda-тай адил ЦАГААР.
	TTL                 time.Duration
	DisableCookieSecure bool // JWT_DISABLE_COOKIE_SECURE (зөвхөн local dev)
}

// MinJWTSecretLen — HS256 secret-ийн доод урт.
const MinJWTSecretLen = 32

// Validate — secret-ийн урт.
func (j JWT) Validate() error {
	if len(j.Secret) < MinJWTSecretLen {
		return fmt.Errorf("config: JWT_SECRET хамгийн багадаа %d тэмдэгт байх ёстой", MinJWTSecretLen)
	}
	return nil
}

// Mail — SMTP.
type Mail struct {
	Host        string // MAIL_HOST
	Port        int    // MAIL_PORT
	Username    string // MAIL_USERNAME
	Password    string // MAIL_PASSWORD
	Encryption  string // MAIL_ENCRYPTION
	FromAddress string // MAIL_FROM_ADDRESS
	FromName    string // MAIL_FROM_NAME
}

// Upload — файл хүлээн авах.
type Upload struct {
	// MaxBytes — хүсэлтийн body-ийн дээд хэмжээ. FILE_MAX_SIZE нь lambda-тай адил МБ-аар.
	MaxBytes int
}

// SysAdmin — системийн админы тохиргоо.
type SysAdmin struct {
	// UUID — users.id нь UUID эсэх (SYSADMIN_UUID). Khan Vision-д үргэлж true.
	UUID bool
}

// Option — Load-ийн тохиргоо.
type Option func(*loader)

type loader struct {
	envFiles []string
	lookup   func(string) (string, bool)
}

// WithEnvFile — `.env` маягийн файл(ууд)-ыг уншина. Аль хэдийн тавигдсан орчны
// хувьсагчийг ДАРАХГҮЙ. Файл байхгүй бол алдаа.
func WithEnvFile(paths ...string) Option {
	return func(l *loader) { l.envFiles = append(l.envFiles, paths...) }
}

// WithLookup — os.LookupEnv-ийн оронд өөр эх сурвалж (тест).
func WithLookup(fn func(string) (string, bool)) Option {
	return func(l *loader) { l.lookup = fn }
}

// Load — тохиргоог уншина. Утгын шалгалт (DB.Validate, JWT.Validate)-ыг
// хэрэглэгч өөрт хэрэгтэйгээ дуудна (ж: `khan schema dump` JWT шаардахгүй).
func Load(opts ...Option) (Config, error) {
	l := loader{lookup: os.LookupEnv}
	for _, o := range opts {
		o(&l)
	}
	if len(l.envFiles) > 0 {
		if err := godotenv.Load(l.envFiles...); err != nil {
			return Config{}, fmt.Errorf("config: env файл: %w", err)
		}
	}

	r := reader{lookup: l.lookup}
	cfg := Config{
		App: App{
			Name: r.str("APP_NAME", ""),
			Port: r.str("APP_PORT", "8080"),
			Env:  r.str("APP_ENV", "production"),
		},
		DB: DB{
			Host:     r.str("DB_HOST", ""),
			Port:     r.str("DB_PORT", "5432"),
			Database: r.str("DB_DATABASE", ""),
			User:     r.str("DB_USERNAME", ""),
			Password: r.str("DB_PASSWORD", ""),
			Extra:    r.str("DB_EXTRA", ""),
			Debug:    r.boolean("DB_DEBUG", false),
			Pool: Pool{
				MaxOpen:     r.integer("DB_MAX_OPEN_CONNS", 0),
				MaxIdle:     r.integer("DB_MAX_IDLE_CONNS", 0),
				MaxIdleTime: r.duration("DB_CONN_MAX_IDLE_TIME", 0),
				MaxLifetime: r.duration("DB_CONN_MAX_LIFETIME", 0),
			},
		},
		JWT: JWT{
			Secret:              r.str("JWT_SECRET", ""),
			TTL:                 time.Duration(r.integer("JWT_TTL", 24)) * time.Hour,
			DisableCookieSecure: r.boolean("JWT_DISABLE_COOKIE_SECURE", false),
		},
		Mail: Mail{
			Host:        r.str("MAIL_HOST", ""),
			Port:        r.integer("MAIL_PORT", 0),
			Username:    r.str("MAIL_USERNAME", ""),
			Password:    r.str("MAIL_PASSWORD", ""),
			Encryption:  r.str("MAIL_ENCRYPTION", ""),
			FromAddress: r.str("MAIL_FROM_ADDRESS", ""),
			FromName:    r.str("MAIL_FROM_NAME", ""),
		},
		Upload: Upload{
			MaxBytes: r.integer("FILE_MAX_SIZE", 100) * 1024 * 1024,
		},
		SysAdmin: SysAdmin{
			UUID: r.boolean("SYSADMIN_UUID", true),
		},
	}
	if err := errors.Join(r.errs...); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

type reader struct {
	lookup func(string) (string, bool)
	errs   []error
}

func (r *reader) raw(key string) (string, bool) {
	v, ok := r.lookup(key)
	v = strings.TrimSpace(v)
	return v, ok && v != ""
}

func (r *reader) str(key, def string) string {
	if v, ok := r.raw(key); ok {
		return v
	}
	return def
}

func (r *reader) integer(key string, def int) int {
	v, ok := r.raw(key)
	if !ok {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		r.errs = append(r.errs, fmt.Errorf("config: %s=%q сөрөг биш бүхэл тоо байх ёстой", key, v))
		return def
	}
	return n
}

func (r *reader) boolean(key string, def bool) bool {
	v, ok := r.raw(key)
	if !ok {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		r.errs = append(r.errs, fmt.Errorf("config: %s=%q true/false байх ёстой", key, v))
		return def
	}
	return b
}

func (r *reader) duration(key string, def time.Duration) time.Duration {
	v, ok := r.raw(key)
	if !ok {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		r.errs = append(r.errs, fmt.Errorf("config: %s=%q Go duration байх ёстой (ж: 10m)", key, v))
		return def
	}
	return d
}
