package db

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/khan-vision/khan/config"
)

func TestDSNQuoting(t *testing.T) {
	got := DSN(config.DB{Host: "127.0.0.1", Port: "5432", User: "u", Database: "khan_test", Password: "", Extra: " sslmode=disable "})
	want := "host=127.0.0.1 port=5432 user=u dbname=khan_test password='' sslmode=disable"
	if got != want {
		t.Fatalf("DSN = %q\nwant %q", got, want)
	}
	got = DSN(config.DB{Host: "h", Port: "1", User: "u", Database: "d", Password: `a b'c\d`})
	want = `host=h port=1 user=u dbname=d password='a b\'c\\d'`
	if got != want {
		t.Fatalf("DSN = %q\nwant %q", got, want)
	}
}

func TestOpenValidates(t *testing.T) {
	if _, err := Open(config.DB{}); err == nil {
		t.Fatal("хоосон config-д алдаа хүлээсэн")
	}
}

func TestConfigurePoolDefaults(t *testing.T) {
	// Сүлжээгүй fake connector — pool тохиргоо холболт нээхгүй.
	sqlDB := sql.OpenDB(nopConnector{})
	defer sqlDB.Close()

	p := ConfigurePool(sqlDB, config.Pool{MaxOpen: 10, MaxIdle: 40})
	if p.MaxOpen != 10 || p.MaxIdle != 10 || p.MaxIdleTime != DefaultPool.MaxIdleTime || p.MaxLifetime != time.Hour {
		t.Fatalf("pool = %+v", p)
	}
	if got := sqlDB.Stats().MaxOpenConnections; got != 10 {
		t.Fatalf("MaxOpenConnections = %d", got)
	}
	if p := ConfigurePool(sqlDB, config.Pool{}); p != DefaultPool {
		t.Fatalf("тэг pool → %+v, want DefaultPool", p)
	}
}

func TestTxNil(t *testing.T) {
	if err := Tx(context.Background(), nil, nil); !errors.Is(err, ErrNilDB) {
		t.Fatalf("Tx(nil) = %v", err)
	}
}
