package query

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/golang-jwt/jwt/v5"
	"gorm.io/gorm"
)

const (
	orgA = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	orgB = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
)

// testResource — DryRun DB-тэй Resource + SQL лог.
func testResource(t *testing.T, canWrite bool) (Resource, *sqlLog) {
	t.Helper()
	db, log := captureDB(t)
	session := func() *gorm.DB { return db.Session(&gorm.Session{DryRun: true, NewDB: true}) }
	ex := roomExport()
	bulk := BulkSpec{
		Table:       "organization.conference_room",
		AllowDelete: true,
		Fields:      map[string]BulkField{"quantity_fit_number": {Col: "quantity_fit_number", Kind: KindNumber, Nullable: true}},
	}
	r := Resource{
		Spec: roomSpec(),
		NewQuery: func(orgID string) *gorm.DB {
			return session().Table("organization.conference_room r").
				Select("r.id, r.room_name").Where("r.org_id = ?", orgID)
		},
		Export:   &ex,
		Bulk:     &bulk,
		DB:       session,
		Can:      func(c fiber.Ctx) map[string]bool { return map[string]bool{"read": true} },
		CanWrite: func(c fiber.Ctx) bool { return canWrite },
	}
	return r, log
}

// newApp — JWT (org=orgA) тавьдаг middleware-тэй app. withJWT=false бол токенгүй.
func newApp(r Resource, withJWT bool) *fiber.App {
	app := fiber.New()
	if withJWT {
		app.Use(func(c fiber.Ctx) error {
			c.Locals("user", &jwt.Token{Claims: jwt.MapClaims{"org_id": orgA, "id": "1", "role": float64(5)}})
			return c.Next()
		})
	}
	r.Mount(app.Group("/rooms"))
	return app
}

func do(t *testing.T, app *fiber.App, method, url, body string) *http.Response {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, url, rd)
	req.Header.Set("X-Org-ID", orgB) // хуурамч header — хэзээ ч уншигдах ёсгүй
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := app.Test(req, noTimeout)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// assertOnlyOrgA — баригдсан бүх SQL нь JWT-ийн orgA-г л bind хийсэн, orgB огт үгүй.
func assertOnlyOrgA(t *testing.T, log *sqlLog, minStatements int) {
	t.Helper()
	all := log.all()
	if len(all) < minStatements {
		t.Fatalf("дор хаяж %d statement хүлээсэн, got %d", minStatements, len(all))
	}
	for _, c := range all {
		if !varsContain(c.vars, orgA) {
			t.Errorf("JWT org (orgA) bind алга: %s %#v", c.sql, c.vars)
		}
		if varsContain(c.vars, orgB) || strings.Contains(c.sql, orgB) {
			t.Errorf("client-ийн orgB SQL-д орсон: %s %#v", c.sql, c.vars)
		}
		if !strings.Contains(c.sql, "org_id = $") {
			t.Errorf("org предикат алга: %s", c.sql)
		}
	}
}

func TestResource_ListUsesJWTOrgOnly(t *testing.T) {
	r, log := testResource(t, true)
	app := newApp(r, true)
	// DryRun нь Scan хийж чаддаггүй тул data алхам 500 — SQL нь count+data хоёулаа баригдана.
	do(t, app, "GET", "/rooms/?org_id="+orgB+"&search=x", "")
	assertOnlyOrgA(t, log, 2)
}

func TestResource_PostQueryUsesJWTOrgOnly(t *testing.T) {
	r, log := testResource(t, true)
	app := newApp(r, true)
	do(t, app, "POST", "/rooms/query?org_id="+orgB, `{"page":2,"limit":5,"org_id":"`+orgB+`","filters":{"room_name":{"op":"contains","v":"а"}}}`)
	assertOnlyOrgA(t, log, 2)
	found := false
	for _, c := range log.all() {
		if strings.Contains(c.sql, "r.room_name ILIKE") {
			found = true
		}
	}
	if !found {
		t.Fatal("POST body-ийн filters хэрэгжээгүй")
	}
}

func TestResource_ExportUsesJWTOrgAndCap(t *testing.T) {
	r, log := testResource(t, true)
	app := newApp(r, true)
	do(t, app, "GET", "/rooms/export.xlsx?org_id="+orgB, "")
	assertOnlyOrgA(t, log, 1)
	c := log.all()[0]
	if !strings.Contains(c.sql, "LIMIT") || c.vars[len(c.vars)-1] != 11 {
		t.Fatalf("export LIMIT cap+1 (11) хүлээсэн: %s %#v", c.sql, c.vars)
	}
}

func TestResource_BulkUsesJWTOrgOnly(t *testing.T) {
	r, log := testResource(t, true)
	app := newApp(r, true)
	resp := do(t, app, "POST", "/rooms/bulk?org_id="+orgB, `{"action":"delete","ids":["`+idA+`"]}`)
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, b)
	}
	var out map[string]interface{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if _, ok := out["affected"]; !ok {
		t.Fatalf("affected алга: %v", out)
	}
	assertOnlyOrgA(t, log, 1)
	if !strings.HasPrefix(log.all()[0].sql, "DELETE FROM organization.conference_room WHERE org_id = $1 AND id IN ($2)") {
		t.Fatalf("bulk SQL: %s", log.all()[0].sql)
	}
}

func TestResource_NoJWTIs401(t *testing.T) {
	r, log := testResource(t, true)
	app := newApp(r, false)
	for _, tc := range [][3]string{
		{"GET", "/rooms/", ""},
		{"POST", "/rooms/query", `{}`},
		{"GET", "/rooms/export.xlsx", ""},
		{"POST", "/rooms/bulk", `{"action":"delete","ids":["` + idA + `"]}`},
	} {
		if resp := do(t, app, tc[0], tc[1], tc[2]); resp.StatusCode != 401 {
			t.Errorf("%s %s: 401 хүлээсэн, got %d", tc[0], tc[1], resp.StatusCode)
		}
	}
	if len(log.all()) != 0 {
		t.Fatalf("JWT-гүй үед SQL бүтээгдэх ёсгүй: %#v", log.all())
	}
}

func TestResource_BulkWithoutWriteIs403(t *testing.T) {
	r, log := testResource(t, false)
	app := newApp(r, true)
	if resp := do(t, app, "POST", "/rooms/bulk", `{"action":"delete","ids":["`+idA+`"]}`); resp.StatusCode != 403 {
		t.Fatalf("403 хүлээсэн, got %d", resp.StatusCode)
	}
	r.CanWrite = nil
	app = newApp(r, true)
	if resp := do(t, app, "POST", "/rooms/bulk", `{"action":"delete","ids":["`+idA+`"]}`); resp.StatusCode != 403 {
		t.Fatalf("CanWrite=nil → 403 хүлээсэн, got %d", resp.StatusCode)
	}
	if len(log.all()) != 0 {
		t.Fatal("эрхгүй үед SQL бүтээгдэх ёсгүй")
	}
}

func TestResource_BadFilterIs400(t *testing.T) {
	r, log := testResource(t, true)
	app := newApp(r, true)
	bad := "filters=%7B%22org_id%22%3A%7B%22op%22%3A%22eq%22%2C%22v%22%3A%22x%22%7D%7D"
	for _, url := range []string{"/rooms/?" + bad, "/rooms/export.xlsx?" + bad} {
		if resp := do(t, app, "GET", url, ""); resp.StatusCode != 400 {
			t.Errorf("%s: 400 хүлээсэн, got %d", url, resp.StatusCode)
		}
	}
	if resp := do(t, app, "POST", "/rooms/bulk", `{"action":"update","ids":["`+idA+`"],"set":{"org_id":"`+orgB+`"}}`); resp.StatusCode != 400 {
		t.Errorf("bulk set org_id: 400 хүлээсэн, got %d", resp.StatusCode)
	}
	if len(log.all()) != 0 {
		t.Fatalf("буруу хүсэлтэд SQL бүтээгдэх ёсгүй: %#v", log.all())
	}
}

func TestResource_NilExportAndBulkAre404(t *testing.T) {
	r, _ := testResource(t, true)
	r.Export, r.Bulk = nil, nil
	app := newApp(r, true)
	if resp := do(t, app, "GET", "/rooms/export.xlsx", ""); resp.StatusCode != 404 {
		t.Errorf("export 404 хүлээсэн, got %d", resp.StatusCode)
	}
	if resp := do(t, app, "POST", "/rooms/bulk", `{}`); resp.StatusCode != 404 {
		t.Errorf("bulk 404 хүлээсэн, got %d", resp.StatusCode)
	}
}

// Org override (production-д orgctx.Require) дуудагдана.
func TestResource_OrgOverride(t *testing.T) {
	r, log := testResource(t, true)
	called := false
	r.Org = func(c fiber.Ctx) (string, bool) { called = true; return orgA, true }
	app := newApp(r, false)
	do(t, app, "GET", "/rooms/", "")
	if !called {
		t.Fatal("Resource.Org дуудагдаагүй")
	}
	assertOnlyOrgA(t, log, 2)
}
