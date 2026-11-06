package db

import (
	"database/sql"
	"errors"
	"strings"
	"testing"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func dryRunDB(t *testing.T) *gorm.DB {
	t.Helper()
	sqlDB := sql.OpenDB(nopConnector{})
	t.Cleanup(func() { _ = sqlDB.Close() })
	g, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{
		DryRun: true, DisableAutomaticPing: true, SkipDefaultTransaction: true, Logger: logger.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func TestVersionedUpdateSQLShape(t *testing.T) {
	g := dryRunDB(t)
	res := versionedUpdate(g, "finance.invoice",
		map[string]any{"org_id": "o1", "id": 7},
		3, map[string]any{"status": "posted"})
	if res.Error != nil {
		t.Fatal(res.Error)
	}
	sql := res.Statement.SQL.String()
	for _, want := range []string{
		`UPDATE "finance"."invoice" SET`,
		`"status"=$`, `"version"=version + 1`, `"updated_at"=now()`,
		`"id" = $`, `"org_id" = $`, `"version" = $`,
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("SQL-д %q алга:\n%s", want, sql)
		}
	}
	if !strings.Contains(sql, "WHERE") {
		t.Fatalf("WHERE алга: %s", sql)
	}
	if strings.Index(sql, `"id"`) > strings.Index(sql, `"org_id"`) {
		t.Errorf("where тогтвортой эрэмбээр биш: %s", sql)
	}
}

func TestUpdateVersionedValidation(t *testing.T) {
	g := dryRunDB(t)
	ok := map[string]any{"org_id": "o1", "id": 1}
	vals := map[string]any{"name": "x"}
	cases := map[string]struct {
		table  string
		where  map[string]any
		values map[string]any
	}{
		"bad table":        {"x; drop", ok, vals},
		"upper table":      {"Finance.Invoice", ok, vals},
		"three part":       {"a.b.c", ok, vals},
		"empty where":      {"t", nil, vals},
		"no org":           {"t", map[string]any{"id": 1}, vals},
		"nil org":          {"t", map[string]any{"org_id": nil}, vals},
		"empty org":        {"t", map[string]any{"org_id": ""}, vals},
		"zero org":         {"t", map[string]any{"org_id": zeroUUID}, vals},
		"bad where col":    {"t", map[string]any{"org_id": "o", "id; x": 1}, vals},
		"version in where": {"t", map[string]any{"org_id": "o", "version": 1}, vals},
		"no values":        {"t", ok, nil},
		"bad value col":    {"t", ok, map[string]any{"a b": 1}},
		"version value":    {"t", ok, map[string]any{"version": 9}},
		"updated_at":       {"t", ok, map[string]any{"updated_at": 9}},
	}
	for name, c := range cases {
		if _, err := UpdateVersioned(g, c.table, c.where, 1, c.values); err == nil {
			t.Errorf("%s: алдаа хүлээсэн", name)
		}
	}
	if _, err := UpdateVersioned(nil, "t", ok, 1, vals); !errors.Is(err, ErrNilDB) {
		t.Errorf("nil db: %v", err)
	}
}
