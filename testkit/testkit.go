// Package testkit — khan болон түүнийг хэрэглэгч app-ийн тестийн туслах.
//
// DB тест ЗӨВХӨН local, нэрэндээ "test" агуулсан database руу холбогдоно —
// prod `.env` санамсаргүй уншигдаж prod DB-д тест бичихээс хамгаална.
// Тохиргоо байхгүй бол тест SKIP болно (CI/laptop дээр DB-гүй ажиллана).
//
//	KHAN_TEST_DB_HOST=127.0.0.1 KHAN_TEST_DB_DATABASE=khan_vision_test \
//	KHAN_TEST_DB_USERNAME=postgres KHAN_TEST_DB_PASSWORD=postgres go test ./...
package testkit

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/golang-jwt/jwt/v5"
	"github.com/khan-vision/khan/config"
	"github.com/khan-vision/khan/db"
	"github.com/khan-vision/khan/tenant"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// EnvPrefix — тестийн DB-ийн env-ийн угтвар (KHAN_TEST_DB_HOST г.м.).
const EnvPrefix = "KHAN_TEST_"

// DBConfig — KHAN_TEST_DB_* env-ээс тестийн DB тохиргоо. Тохируулаагүй бол ok=false.
func DBConfig() (config.DB, bool, error) {
	cfg, err := config.Load(config.WithLookup(func(k string) (string, bool) {
		return os.LookupEnv(EnvPrefix + k)
	}))
	if err != nil {
		return config.DB{}, false, err
	}
	if cfg.DB.Host == "" && cfg.DB.Database == "" {
		return config.DB{}, false, nil
	}
	if err := GuardLocal(cfg.DB); err != nil {
		return config.DB{}, false, err
	}
	return cfg.DB, true, nil
}

// GuardLocal — host нь local (127.0.0.1/localhost/::1/unix socket) БА database-ийн
// нэр "test" агуулсан байхыг шаардана.
func GuardLocal(c config.DB) error {
	host := c.Host
	isLocal := host == "127.0.0.1" || host == "localhost" || host == "::1" || strings.HasPrefix(host, "/")
	if !isLocal {
		return fmt.Errorf("testkit: DB host %q local биш — тест зөвхөн local DB дээр", host)
	}
	if !strings.Contains(strings.ToLower(c.Database), "test") {
		return fmt.Errorf("testkit: database %q нэрэнд \"test\" алга — prod/dev DB-г хамгаалав", c.Database)
	}
	return nil
}

// DB — тестийн DB холболт; тохиргоогүй бол t.Skip. Тест дуусахад хаана.
func DB(t testing.TB) *gorm.DB {
	t.Helper()
	cfg, ok, err := DBConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Skip("testkit: KHAN_TEST_DB_* тохируулаагүй — DB тест алгасав")
	}
	gdb, err := db.Open(cfg, db.WithLogger(logger.Discard))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close(gdb) })
	return gdb
}

// TxDB — тестийн DB дээр транзакц нээж, тест дуусахад ROLLBACK хийнэ
// (өгөгдөл үлдэхгүй). DDL ч rollback болно (Postgres).
func TxDB(t testing.TB) *gorm.DB {
	t.Helper()
	tx := DB(t).Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	return tx
}

// Claims — тестийн JWT claims (Khan Vision-ий claim нэрстэй).
func Claims(userID, orgID string, role int) jwt.MapClaims {
	return jwt.MapClaims{
		"id":     userID,
		"org_id": orgID,
		"role":   role,
		"exp":    time.Now().Add(time.Hour).Unix(),
	}
}

// WithClaims — c.Locals(tenant.LocalsKey)-д claims-тай token тавих middleware
// (JWT гарын үсэг шалгахгүй — handler-ийн unit тестэд).
func WithClaims(claims jwt.MapClaims) fiber.Handler {
	return func(c fiber.Ctx) error {
		if claims != nil {
			c.Locals(tenant.LocalsKey, &jwt.Token{Claims: claims, Valid: true})
		}
		return c.Next()
	}
}

// SignedToken — HS256 гарын үсэгтэй token (end-to-end middleware тестэд).
func SignedToken(secret string, claims jwt.MapClaims) (string, error) {
	if secret == "" {
		return "", errors.New("testkit: secret хоосон")
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
}
