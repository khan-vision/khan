package tenant

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v5"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestOrgID_OnlyFromJWT(t *testing.T) {
	cases := []struct {
		name   string
		claims jwt.MapClaims
		want   string
		status int
	}{
		{"jwt org", jwt.MapClaims{"org_id": "org-A"}, "org-A", 200},
		{"no token", nil, "", 401},
		{"empty org", jwt.MapClaims{"org_id": ""}, "", 401},
		{"zero uuid", jwt.MapClaims{"org_id": ZeroUUID}, "", 401},
		{"non-string org", jwt.MapClaims{"org_id": 5.0}, "", 401},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := fiber.New()
			var got string
			app.Use(func(c *fiber.Ctx) error {
				if tc.claims != nil {
					c.Locals("user", &jwt.Token{Claims: tc.claims})
				}
				return c.Next()
			})
			app.Get("/", func(c *fiber.Ctx) error {
				org, ok := Require(c)
				if !ok {
					return nil
				}
				got = org
				return c.SendStatus(200)
			})
			req := httptest.NewRequest("GET", "/?org_id=org-B", nil)
			req.Header.Set("X-Org-ID", "org-B")
			resp, err := app.Test(req)
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != tc.status || got != tc.want {
				t.Fatalf("status=%d org=%q, want %d %q", resp.StatusCode, got, tc.status, tc.want)
			}
		})
	}
}

func dryDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(
		postgres.New(postgres.Config{DSN: "host=/nonexistent-khan user=x dbname=x", WithoutQuotingCheck: true}),
		&gorm.Config{DryRun: true, DisableAutomaticPing: true, Logger: logger.Discard},
	)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func TestScope(t *testing.T) {
	type row struct{ ID string }
	db := dryDB(t)

	stmt := db.Table("finance.invoice i").Scopes(Scope("i", "org-A")).Find(&[]row{}).Statement
	if sql := stmt.SQL.String(); !strings.Contains(sql, "i.org_id = $1") || stmt.Vars[0] != "org-A" {
		t.Fatalf("scope sql=%q vars=%v", sql, stmt.Vars)
	}

	for name, tc := range map[string]struct{ alias, org string }{
		"empty org":  {"i", ""},
		"zero uuid":  {"i", ZeroUUID},
		"bad alias":  {"i; DROP", "org-A"},
		"upper case": {"I", "org-A"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := dryDB(t).Table("x").Scopes(Scope(tc.alias, tc.org)).Find(&[]row{}).Error; err == nil {
				t.Fatal("fail-closed алдаа хүлээсэн")
			}
		})
	}
}

func TestMiddleware(t *testing.T) {
	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error {
		if c.Get("X-Test-Token") == "1" {
			c.Locals(LocalsKey, &jwt.Token{Claims: jwt.MapClaims{"org_id": "org-A"}})
		}
		return c.Next()
	}, Middleware())
	app.Get("/", func(c *fiber.Ctx) error { return c.SendString(c.Locals(LocalsOrgID).(string)) })

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Org-ID", "org-B")
	if resp, _ := app.Test(req); resp.StatusCode != 401 {
		t.Fatalf("token-гүй = %d, want 401", resp.StatusCode)
	}
	req = httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Test-Token", "1")
	resp, _ := app.Test(req)
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(body) != "org-A" {
		t.Fatalf("status=%d body=%q", resp.StatusCode, body)
	}
}
