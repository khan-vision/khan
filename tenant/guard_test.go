package tenant

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/khan-vision/khan/schema"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type guardInvoice struct {
	ID    string
	OrgID string
	Code  string
}

func (guardInvoice) TableName() string { return "finance.invoice" }

func guardDB(t *testing.T, mode GuardMode, logs *[]string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(
		postgres.New(postgres.Config{DSN: "host=/nonexistent-khan user=x dbname=x", WithoutQuotingCheck: true}),
		&gorm.Config{DryRun: true, DisableAutomaticPing: true, SkipDefaultTransaction: true, Logger: logger.Discard},
	)
	if err != nil {
		t.Fatal(err)
	}
	tables := []schema.Table{{Schema: "finance", Name: "invoice", Kind: schema.KindTable,
		Columns: []schema.Column{{Name: "id"}, {Name: "org_id", UDT: "uuid"}}}}
	logf := func(f string, a ...any) {
		if logs != nil {
			*logs = append(*logs, f)
		}
	}
	if err := db.Use(NewGuard(tables, mode, logf)); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestGuardAllowsScoped(t *testing.T) {
	db := guardDB(t, GuardStrict, nil)
	var rows []guardInvoice
	ok := map[string]*gorm.DB{
		"scope alias":   db.Table("finance.invoice i").Scopes(Scope("i", "org-A")).Find(&rows),
		"scope model":   db.Scopes(Scope("", "org-A")).Find(&rows),
		"where map":     db.Where(map[string]any{"org_id": "org-A"}).Find(&rows),
		"where struct":  db.Where(&guardInvoice{OrgID: "org-A"}).Find(&rows),
		"in":            db.Where("org_id IN ?", []string{"a", "b"}).Find(&rows),
		"and + other":   db.Where("code = ?", "x").Where("i.org_id = ?", "org-A").Table("finance.invoice i").Find(&rows),
		"update scoped": db.Model(&guardInvoice{}).Where("org_id = ? AND id = ?", "org-A", "1").Update("code", "y"),
		"delete scoped": db.Where("org_id = ?", "org-A").Delete(&guardInvoice{}),
		"create org":    db.Create(&guardInvoice{ID: "1", OrgID: "org-A"}),
		"create batch":  db.Create(&[]guardInvoice{{ID: "1", OrgID: "a"}, {ID: "2", OrgID: "b"}}),
		"create map":    db.Table("finance.invoice").Create(map[string]any{"id": "1", "org_id": "org-A"}),
		"other table":   db.Table("public.users").Find(&[]map[string]any{}),
		"tx context":    db.WithContext(context.WithValue(context.Background(), ctxOrg, "org-A")).Find(&rows),
		"system ctx":    db.WithContext(context.WithValue(context.Background(), ctxBypass, true)).Find(&rows),
	}
	for name, res := range ok {
		if res.Error != nil {
			t.Errorf("%s: %v", name, res.Error)
		}
	}
}

func TestGuardBlocksUnscoped(t *testing.T) {
	db := guardDB(t, GuardStrict, nil)
	var rows []guardInvoice
	bad := map[string]*gorm.DB{
		"no where":       db.Find(&rows),
		"by id only":     db.First(&guardInvoice{}, "id = ?", "1"),
		"table expr":     db.Table("finance.invoice i").Where("i.code = ?", "x").Find(&rows),
		"bare name":      db.Table("invoice").Find(&rows),
		"quoted":         db.Table(`"finance"."invoice"`).Find(&rows),
		"or bypass":      db.Where("org_id = ? OR 1=1", "org-A").Find(&rows),
		"or conditions":  db.Where("code = ?", "x").Or("org_id = ?", "org-A").Find(&rows),
		"empty org eq":   db.Where(map[string]any{"org_id": ""}).Find(&rows),
		"empty org expr": db.Where("org_id = ?", "").Find(&rows),
		"zero org expr":  db.Where("org_id = ?", ZeroUUID).Find(&rows),
		"org in comment": db.Where("code = 'org_id'").Find(&rows),
		"update":         db.Model(&guardInvoice{}).Where("id = ?", "1").Update("code", "y"),
		"delete":         db.Where("id = ?", "1").Delete(&guardInvoice{}),
		"create no org":  db.Create(&guardInvoice{ID: "1"}),
		"batch one miss": db.Create(&[]guardInvoice{{ID: "1", OrgID: "a"}, {ID: "2"}}),
		"create map":     db.Table("finance.invoice").Create(map[string]any{"id": "1"}),
		"count":          db.Model(&guardInvoice{}).Count(new(int64)),
	}
	for name, res := range bad {
		if !errors.Is(res.Error, ErrUnscoped) {
			t.Errorf("%s: ErrUnscoped хүлээсэн, авсан %v", name, res.Error)
		}
	}
}

func TestGuardWarnMode(t *testing.T) {
	var logs []string
	db := guardDB(t, GuardWarn, &logs)
	if err := db.Find(&[]guardInvoice{}).Error; err != nil {
		t.Fatalf("warn горим query-г зогсоов: %v", err)
	}
	if len(logs) != 1 || !strings.Contains(logs[0], "tenant-guard") {
		t.Fatalf("logs = %v", logs)
	}
}

func TestNewGuardFor(t *testing.T) {
	g := NewGuardFor([]string{"hr.leave"}, GuardStrict, nil)
	if !g.tables["hr.leave"] || !g.tables["leave"] {
		t.Fatalf("tables = %v", g.tables)
	}
}

func TestPolicySQL(t *testing.T) {
	soft, err := PolicySQL("finance.invoice", PolicyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"ALTER TABLE finance.invoice ENABLE ROW LEVEL SECURITY;",
		"ALTER TABLE finance.invoice FORCE ROW LEVEL SECURITY;",
		"DROP POLICY IF EXISTS khan_tenant_isolation ON finance.invoice;",
		"NULLIF(current_setting('app.org_id', true), '') IS NULL OR",
		"org_id = NULLIF(current_setting('app.org_id', true), '')::uuid",
		"WITH CHECK",
	} {
		if !strings.Contains(soft, want) {
			t.Errorf("soft policy-д %q алга:\n%s", want, soft)
		}
	}
	strict, _ := PolicySQL("finance.invoice", PolicyOptions{Mode: PolicyStrict, ColumnType: "text", NoForce: true})
	if strings.Contains(strict, "IS NULL OR") || !strings.Contains(strict, "::text") ||
		!strings.Contains(strict, "NO FORCE") || !strings.Contains(strict, "app.bypass_rls") {
		t.Fatalf("strict policy:\n%s", strict)
	}
	for _, bad := range []string{"invoice", "finance.invoice; DROP", `"finance".invoice`} {
		if _, err := PolicySQL(bad, PolicyOptions{}); err == nil {
			t.Errorf("%q зөвшөөрөгдөв", bad)
		}
	}
	if _, err := PolicySQL("finance.invoice", PolicyOptions{Column: "org id"}); err == nil {
		t.Error("буруу багана зөвшөөрөгдөв")
	}
	if _, err := PolicySQL("finance.invoice", PolicyOptions{ColumnType: "uuid); DROP"}); err == nil {
		t.Error("буруу төрөл зөвшөөрөгдөв")
	}
}

func TestTenantTablesAndPolicyForTable(t *testing.T) {
	cat := &schema.Catalog{Tables: []schema.Table{
		{Schema: "finance", Name: "invoice", Kind: schema.KindTable, Columns: []schema.Column{{Name: "org_id", UDT: "uuid"}}},
		{Schema: "crm", Name: "lead", Kind: schema.KindTable, Columns: []schema.Column{{Name: "org_id", UDT: "varchar"}}},
		{Schema: "finance", Name: "v_invoice", Kind: schema.KindView, Columns: []schema.Column{{Name: "org_id", UDT: "uuid"}}},
		{Schema: "public", Name: "users", Kind: schema.KindTable, Columns: []schema.Column{{Name: "id"}}},
	}}
	tables := TenantTables(cat)
	if len(tables) != 2 {
		t.Fatalf("tenant tables = %v", tables)
	}
	sql, err := PolicySQLForTable(tables[1], PolicyStrict)
	if err != nil || !strings.Contains(sql, "crm.lead") || !strings.Contains(sql, "::text") {
		t.Fatalf("crm.lead policy = %v\n%s", err, sql)
	}
	if _, err := PolicySQLForTable(cat.Tables[3], PolicySoft); err == nil {
		t.Fatal("org_id-гүй хүснэгтэд алдаа хүлээсэн")
	}
}

func TestWithOrgRequiresTx(t *testing.T) {
	db := guardDB(t, GuardWarn, nil)
	if err := WithOrg(db, "org-A"); !errors.Is(err, ErrNotInTx) {
		t.Fatalf("WithOrg(no tx) = %v", err)
	}
	if err := WithOrg(db, ""); !errors.Is(err, ErrNoOrg) {
		t.Fatalf("WithOrg(empty) = %v", err)
	}
	if err := WithBypass(db); !errors.Is(err, ErrNotInTx) {
		t.Fatalf("WithBypass(no tx) = %v", err)
	}
	if err := Tx(context.Background(), db, ZeroUUID, nil); !errors.Is(err, ErrNoOrg) {
		t.Fatalf("Tx(zero) = %v", err)
	}
	if _, ok := OrgFromContext(context.Background()); ok {
		t.Fatal("хоосон context-д org олдов")
	}
}
