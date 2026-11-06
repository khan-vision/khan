package query

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// dryDB — SQL зөвхөн БҮТЭЭГДЭНЭ, серверт очихгүй (DryRun). Тестэд DB хэрэггүй.
func dryDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(
		postgres.New(postgres.Config{DSN: "host=127.0.0.1 user=x dbname=x", WithoutQuotingCheck: true}),
		&gorm.Config{DryRun: true, DisableAutomaticPing: true},
	)
	if err != nil {
		t.Skipf("dry-run DB бэлдэх боломжгүй: %v", err)
	}
	return db
}

func invoiceSpec() Spec {
	return Spec{
		Search: []string{"i.invoice_number", "p.name"},
		Sort:   map[string]string{"invoice_date": "i.invoice_date"},
		Filters: map[string]FilterCol{
			"party_name": Text("p.name"),
			"status":     Enum("i.status"),
		},
		DefaultOrder: "i.created_at DESC",
		Totals:       map[string]string{"total_amount": "COALESCE(SUM(i.total_amount),0)"},
	}
}

// runWith — Run-ыг тодорхой query string-тэй дуудаж, үүссэн SQL-үүдийг цуглуулна.
func runWith(t *testing.T, spec Spec, query string) (fiber.Map, error, []string) {
	t.Helper()
	db := dryDB(t)

	var built []string
	newQuery := func() *gorm.DB {
		return db.Session(&gorm.Session{DryRun: true, NewDB: true}).
			Table("finance.invoices i").
			Joins("LEFT JOIN party.party p ON p.id = i.party_id").
			Where("i.org_id = ?", "org-1")
	}

	app := fiber.New()
	var resp fiber.Map
	var runErr error
	app.Get("/list", func(c fiber.Ctx) error {
		var rows []map[string]interface{}
		resp, runErr = Run(c, newQuery, &rows, spec)
		return c.SendString("ok")
	})
	req := httptest.NewRequest("GET", "/list?"+query, nil)
	if _, err := app.Test(req, noTimeout); err != nil {
		t.Fatalf("fiber test алдаа: %v", err)
	}
	_ = built
	return resp, runErr, built
}

// ─── Шүүлт нь count/totals/data гурвуулангаар дамжина ───

// Хөлийн дүн нь харагдаж буй мөрүүдээс өөр зүйл ярихгүй байх гол баталгаа:
// Run доторх `base()` closure нь гурван query-д ижил предикат өгдөг эсэх.
func TestRun_FilterAppliesToCountTotalsAndData(t *testing.T) {
	db := dryDB(t)

	base := func() *gorm.DB {
		return db.Session(&gorm.Session{DryRun: true, NewDB: true}).
			Table("finance.invoices i").
			Where("i.org_id = ?", "org-1")
	}

	spec := invoiceSpec()
	conds, err := spec.parseFilters(`{"status":{"op":"in","vs":["posted"]}}`)
	if err != nil {
		t.Fatalf("parseFilters: %v", err)
	}

	withAll := func() *gorm.DB { return applyFilters(spec.applySearch(base(), "гоо"), conds) }

	var total int64
	countSQL := withAll().Count(&total).Statement.SQL.String()

	var rows []map[string]interface{}
	dataSQL := withAll().Order("i.created_at DESC").Offset(0).Limit(25).Find(&rows).Statement.SQL.String()

	totalsSQL := withAll().Select("COALESCE(SUM(i.total_amount),0) AS total_amount").
		Find(&rows).Statement.SQL.String()

	for name, sql := range map[string]string{"count": countSQL, "data": dataSQL, "totals": totalsSQL} {
		if !strings.Contains(sql, "i.status IN") {
			t.Errorf("%s query-д баганын шүүлт алга: %s", name, sql)
		}
		if !strings.Contains(sql, "ILIKE") {
			t.Errorf("%s query-д хайлт алга: %s", name, sql)
		}
		if !strings.Contains(sql, "i.org_id") {
			t.Errorf("%s query-д org_id хамгаалалт алга: %s", name, sql)
		}
	}
}

// ─── Run-ийн хариулт ───

// ⚠️ Run-ийн БҮТЭН зам (Scan → хариултын map) нь жинхэнэ DB шаардана: GORM-ийн
// DryRun горим `Scan`-ыг дэмждэггүй (`dry run mode unsupported`). Тиймээс энд
// зөвхөн DB хүрэхээс ӨМНӨХ зам (шүүлт задлах, 400 алдаа) болон үүсэх SQL-ийн
// хэлбэрийг шалгана. `filterable`-ийн агуулга → filter_test.go.

func TestRun_BadFilterIsFilterError(t *testing.T) {
	_, err, _ := runWith(t, invoiceSpec(), "filters=%7B%22nope%22%3A%7B%22op%22%3A%22eq%22%2C%22v%22%3A1%7D%7D")
	if !IsFilterError(err) {
		t.Fatalf("зөвшөөрөгдөөгүй багана → FilterError (400) байх ёстой, got %v", err)
	}
}

func TestRun_MalformedFilterJSON(t *testing.T) {
	_, err, _ := runWith(t, invoiceSpec(), "filters=%7Bnot-json")
	if !IsFilterError(err) {
		t.Fatalf("буруу JSON → FilterError байх ёстой, got %v", err)
	}
}

// ─── Хуучин зан хэвээр (backward compat) ───

// `filters` параметргүй үед үүсэх SQL нь өмнөхтэй ЯГ ижил байх ёстой
// (page/limit/search/sort бүгд хэвээр, нэмэлт WHERE ороогүй).
func TestRun_NoFiltersParamKeepsOldSQL(t *testing.T) {
	db := dryDB(t)
	spec := invoiceSpec()

	conds, err := spec.parseFilters("")
	if err != nil || len(conds) != 0 {
		t.Fatalf("filters-гүй үед нөхцөл үүсэх ёсгүй: %v / %v", conds, err)
	}

	q := applyFilters(spec.applySearch(
		db.Session(&gorm.Session{DryRun: true, NewDB: true}).Table("finance.invoices i"), "abc"), conds)
	var rows []map[string]interface{}
	stmt := q.Order("i.invoice_date ASC").Offset(50).Limit(50).Find(&rows).Statement
	sql := stmt.SQL.String()

	// LIMIT/OFFSET нь параметрждэг ($3/$4) тул утгыг Vars-аас шалгана.
	if !strings.Contains(sql, "LIMIT") || !strings.Contains(sql, "OFFSET") {
		t.Fatalf("хуудаслалт алга: %s", sql)
	}
	n := len(stmt.Vars)
	if n < 2 || stmt.Vars[n-2] != 50 || stmt.Vars[n-1] != 50 {
		t.Fatalf("limit/offset утга буруу: %#v", stmt.Vars)
	}
	if !strings.Contains(sql, "ORDER BY i.invoice_date ASC") {
		t.Fatalf("эрэмбэ алга: %s", sql)
	}
	if strings.Count(sql, "WHERE") != 1 {
		t.Fatalf("нэмэлт WHERE орсон: %s", sql)
	}
}

func TestApplySearch_EscapesWildcards(t *testing.T) {
	db := dryDB(t)
	spec := invoiceSpec()
	q := spec.applySearch(
		db.Session(&gorm.Session{DryRun: true, NewDB: true}).Table("finance.invoices i"), "100%")
	var rows []map[string]interface{}
	stmt := q.Find(&rows).Statement
	if !strings.Contains(stmt.SQL.String(), `ESCAPE '\'`) {
		t.Fatalf("хайлтад ESCAPE алга: %s", stmt.SQL.String())
	}
	found := false
	for _, v := range stmt.Vars {
		if s, ok := v.(string); ok && s == `%100\%%` {
			found = true
		}
	}
	if !found {
		t.Fatalf("хайлтын `%%` escape хийгдээгүй: %#v", stmt.Vars)
	}
}
