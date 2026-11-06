package query

import (
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
	"gorm.io/gorm"
)

// legacyRun — refactor-оос ӨМНӨХ Run-ийн хуулбар (зөвхөн SQL харьцуулах тестэд).
func legacyRun(c fiber.Ctx, newQuery func() *gorm.DB, dest interface{}, spec Spec) (fiber.Map, error) {
	defLimit := spec.DefaultLimit
	if defLimit <= 0 {
		defLimit = 25
	}
	maxLimit := spec.MaxLimit
	if maxLimit <= 0 {
		maxLimit = 500
	}
	page := fiber.Query[int](c, "page", 1)
	if page < 1 {
		page = 1
	}
	limit := fiber.Query[int](c, "limit", defLimit)
	if limit < 1 {
		limit = defLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	searchVal := strings.TrimSpace(c.Query("search"))
	conds, ferr := spec.parseFilters(c.Query("filters"))
	if ferr != nil {
		return nil, ferr
	}
	base := func() *gorm.DB {
		return applyFilters(spec.applySearch(newQuery(), searchVal), conds)
	}
	var total int64
	if err := base().Count(&total).Error; err != nil {
		return nil, err
	}
	order := spec.DefaultOrder
	if sortKey := c.Query("sort"); sortKey != "" && spec.Sort != nil {
		if col, ok := spec.Sort[sortKey]; ok {
			dir := strings.ToLower(c.Query("dir", "desc"))
			if dir != "asc" {
				dir = "desc"
			}
			order = col + " " + strings.ToUpper(dir)
		}
	}
	dq := base()
	if order != "" {
		dq = dq.Order(order)
	}
	if err := dq.Offset((page - 1) * limit).Limit(limit).Scan(dest).Error; err != nil {
		return nil, err
	}
	return fiber.Map{"data": dest, "total": total, "page": page, "limit": limit}, nil
}

// capturedRun — өгөгдсөн run функцийг query string-тэй дуудаж SQL-үүдийг буцаана.
func capturedRun(t *testing.T, query string, run func(fiber.Ctx, func() *gorm.DB, interface{}, Spec) (fiber.Map, error)) ([]captured, error) {
	t.Helper()
	db, log := captureDB(t)
	newQuery := func() *gorm.DB {
		return db.Session(&gorm.Session{DryRun: true, NewDB: true}).
			Table("finance.invoices i").
			Joins("LEFT JOIN party.party p ON p.id = i.party_id").
			Where("i.org_id = ?", "org-1")
	}
	app := fiber.New()
	var runErr error
	app.Get("/list", func(c fiber.Ctx) error {
		var rows []map[string]interface{}
		_, runErr = run(c, newQuery, &rows, invoiceSpec())
		return c.SendString("ok")
	})
	if _, err := app.Test(httptest.NewRequest("GET", "/list?"+query, nil), noTimeout); err != nil {
		t.Fatalf("fiber test: %v", err)
	}
	return log.all(), runErr
}

// Refactor-ийн дараах Run нь хуучин Run-тэй ЯГ ижил SQL + Vars үүсгэнэ.
func TestRun_RefactorKeepsIdenticalSQL(t *testing.T) {
	queries := []string{
		"",
		"page=3&limit=10",
		"page=0&limit=0",
		"page=-5&limit=-1",
		"limit=99999",
		"limit=abc&page=xyz",
		"sort=invoice_date&dir=asc",
		"sort=invoice_date&dir=ASC",
		"sort=invoice_date",
		"sort=invoice_date&dir=sideways",
		"sort=i.total_amount%3B+DROP&dir=asc",
		"search=%20%D0%B3%D0%BE%D0%BE%20",
		"filters=%7B%22status%22%3A%7B%22op%22%3A%22in%22%2C%22vs%22%3A%5B%22posted%22%5D%7D%7D&search=x&page=2&limit=5&sort=invoice_date&dir=asc",
	}
	for _, q := range queries {
		oldSQL, oldErr := capturedRun(t, q, legacyRun)
		newSQL, newErr := capturedRun(t, q, Run)
		if (oldErr == nil) != (newErr == nil) || IsFilterError(oldErr) != IsFilterError(newErr) {
			t.Errorf("%q: алдаа зөрөв old=%v new=%v", q, oldErr, newErr)
		}
		if len(oldSQL) == 0 {
			t.Fatalf("%q: SQL баригдсангүй", q)
		}
		if !reflect.DeepEqual(oldSQL, newSQL) {
			t.Errorf("%q: SQL зөрөв\nold=%#v\nnew=%#v", q, oldSQL, newSQL)
		}
	}
}

// Run == RunRequest(FromQuery(c), ...).
func TestRunRequest_FromQueryEqualsRun(t *testing.T) {
	q := "page=2&limit=7&search=abc&sort=invoice_date&dir=asc"
	a, _ := capturedRun(t, q, Run)
	b, _ := capturedRun(t, q, func(c fiber.Ctx, nq func() *gorm.DB, d interface{}, s Spec) (fiber.Map, error) {
		return RunRequest(FromQuery(c), nq, d, s)
	})
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("Run ба RunRequest(FromQuery) зөрөв\n%#v\n%#v", a, b)
	}
}

func TestPrepare_LimitsAndDefaults(t *testing.T) {
	spec := Spec{
		Sort:         map[string]string{"name": "p.name"},
		DefaultOrder: "p.created_at DESC",
		DefaultLimit: 30,
		MaxLimit:     100,
	}
	cases := []struct {
		req         Request
		page, limit int
		order       string
		search      string
	}{
		{Request{}, 1, 30, "p.created_at DESC", ""},
		{Request{Page: -3, Limit: -1}, 1, 30, "p.created_at DESC", ""},
		{Request{Page: 4, Limit: 1000}, 4, 100, "p.created_at DESC", ""},
		{Request{Limit: 50, Sort: "name", Dir: "ASC"}, 1, 50, "p.name ASC", ""},
		{Request{Sort: "name"}, 1, 30, "p.name DESC", ""},
		{Request{Sort: "name", Dir: "bogus"}, 1, 30, "p.name DESC", ""},
		{Request{Sort: "p.name; DROP TABLE x"}, 1, 30, "p.created_at DESC", ""},
		{Request{Search: "  гоо  "}, 1, 30, "p.created_at DESC", "гоо"},
	}
	for i, tc := range cases {
		p, err := spec.prepare(tc.req)
		if err != nil {
			t.Fatalf("#%d: %v", i, err)
		}
		if p.page != tc.page || p.limit != tc.limit || p.order != tc.order || p.search != tc.search {
			t.Errorf("#%d: got page=%d limit=%d order=%q search=%q", i, p.page, p.limit, p.order, p.search)
		}
	}
	// Spec-ийн анхдагч (25/500).
	p, _ := Spec{}.prepare(Request{Limit: 10_000})
	if p.limit != 500 {
		t.Errorf("MaxLimit анхдагч 500 байх ёстой, got %d", p.limit)
	}
	p, _ = Spec{}.prepare(Request{})
	if p.limit != 25 {
		t.Errorf("DefaultLimit анхдагч 25 байх ёстой, got %d", p.limit)
	}
}

func TestParseBody(t *testing.T) {
	r, err := parseBody([]byte(`{"page":2,"limit":50,"search":"x","sort":"name","dir":"asc","filters":{"status":{"op":"eq","v":"a"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if r.Page != 2 || r.Limit != 50 || r.Search != "x" || r.Sort != "name" || r.Dir != "asc" ||
		r.Filters != `{"status":{"op":"eq","v":"a"}}` {
		t.Fatalf("объект filters буруу задарсан: %#v", r)
	}
	// filters нь JSON мөр хэлбэрээр.
	r, err = parseBody([]byte(`{"filters":"{\"status\":{\"op\":\"eq\",\"v\":\"a\"}}"}`))
	if err != nil || r.Filters != `{"status":{"op":"eq","v":"a"}}` || r.Page != 1 {
		t.Fatalf("мөр filters буруу: %#v %v", r, err)
	}
	r, err = parseBody([]byte(`{"filters":null}`))
	if err != nil || r.Filters != "" {
		t.Fatalf("null filters: %#v %v", r, err)
	}
	for _, bad := range []string{`{not json`, `{"filters":[1,2]}`, `{"filters":5}`, `{"page":"x"}`, `[]`} {
		if _, err := parseBody([]byte(bad)); !IsFilterError(err) {
			t.Errorf("%s → FilterError хүлээсэн, got %v", bad, err)
		}
	}
	big := `{"search":"` + strings.Repeat("a", maxBodyBytes) + `"}`
	if _, err := parseBody([]byte(big)); !IsFilterError(err) {
		t.Errorf("16KB-аас том body → FilterError, got %v", err)
	}
}

func TestRequestFrom_PostBodyVsQuery(t *testing.T) {
	app := fiber.New()
	var got Request
	var gotErr error
	h := func(c fiber.Ctx) error {
		got, gotErr = RequestFrom(c)
		return c.SendString("ok")
	}
	app.Get("/", h)
	app.Post("/query", h)

	_, _ = app.Test(httptest.NewRequest("GET", "/?page=3&limit=9&search=s&sort=a&dir=asc&filters=%7B%7D", nil), noTimeout)
	if gotErr != nil || got.Page != 3 || got.Limit != 9 || got.Search != "s" || got.Sort != "a" || got.Dir != "asc" || got.Filters != "{}" {
		t.Fatalf("GET: %#v %v", got, gotErr)
	}

	req := httptest.NewRequest("POST", "/query?page=99", strings.NewReader(`{"page":4,"limit":11}`))
	req.Header.Set("Content-Type", "application/json")
	_, _ = app.Test(req, noTimeout)
	if gotErr != nil || got.Page != 4 || got.Limit != 11 {
		t.Fatalf("POST body давуу байх ёстой: %#v %v", got, gotErr)
	}

	req = httptest.NewRequest("POST", "/query", strings.NewReader(`{bad`))
	req.Header.Set("Content-Type", "application/json")
	_, _ = app.Test(req, noTimeout)
	if !IsFilterError(gotErr) {
		t.Fatalf("буруу body → FilterError, got %v", gotErr)
	}
}
