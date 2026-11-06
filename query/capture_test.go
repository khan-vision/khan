package query

import (
	"sync"
	"testing"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// captured — DryRun-д БҮТЭЭГДСЭН нэг statement (серверт очоогүй).
type captured struct {
	sql  string
	vars []interface{}
}

type sqlLog struct {
	mu    sync.Mutex
	items []captured
}

func (l *sqlLog) add(db *gorm.DB) {
	if db.Statement == nil || db.Statement.SQL.Len() == 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.items = append(l.items, captured{
		sql:  db.Statement.SQL.String(),
		vars: append([]interface{}{}, db.Statement.Vars...),
	})
}

func (l *sqlLog) all() []captured {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]captured{}, l.items...)
}

// captureDB — DryRun DB + бүх statement-ийг цуглуулах callback. DSN нь байхгүй unix
// socket тул санамсаргүй ч гэсэн жинхэнэ DB рүү холбогдохгүй.
func captureDB(t *testing.T) (*gorm.DB, *sqlLog) {
	t.Helper()
	db, err := gorm.Open(
		postgres.New(postgres.Config{DSN: "host=/nonexistent-kha235 user=x dbname=x", WithoutQuotingCheck: true}),
		&gorm.Config{DryRun: true, DisableAutomaticPing: true, SkipDefaultTransaction: true, Logger: logger.Discard},
	)
	if err != nil {
		t.Skipf("dry-run DB бэлдэх боломжгүй: %v", err)
	}
	log := &sqlLog{}
	cb := db.Callback()
	must := func(err error) {
		if err != nil {
			t.Fatalf("callback бүртгэх: %v", err)
		}
	}
	must(cb.Query().After("gorm:query").Register("lq:capture_query", log.add))
	must(cb.Row().After("gorm:row").Register("lq:capture_row", log.add))
	must(cb.Update().After("gorm:update").Register("lq:capture_update", log.add))
	must(cb.Delete().After("gorm:delete").Register("lq:capture_delete", log.add))
	must(cb.Create().After("gorm:create").Register("lq:capture_create", log.add))
	return db, log
}

// varsContain — Vars дотор утга (мөр эсвэл мөрийн slice) байгаа эсэх.
func varsContain(vars []interface{}, want string) bool {
	for _, v := range vars {
		switch t := v.(type) {
		case string:
			if t == want {
				return true
			}
		case []string:
			for _, s := range t {
				if s == want {
					return true
				}
			}
		}
	}
	return false
}
