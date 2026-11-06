package idempotency

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/khan-vision/khan/testkit"
	"gorm.io/gorm"
)

const (
	orgA = "11111111-1111-1111-1111-111111111111"
	orgB = "22222222-2222-2222-2222-222222222222"
)

func ensureSchema(t *testing.T, tx *gorm.DB) {
	t.Helper()
	if err := tx.Exec(`SELECT pg_advisory_xact_lock(1802398062)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := tx.Exec(SchemaSQL).Error; err != nil {
		t.Fatal(err)
	}
}

type env struct {
	tx    *gorm.DB
	app   *fiber.App
	calls int
	// next — handler-ийн хариуг тохируулна.
	status int
}

// newEnv — claims-ийг per-request header-ээр (X-Test-User/X-Test-Org) тавьдаг
// тест app. (Эдгээр header нь зөвхөн тестийн claims middleware-д; idempotency
// өөрөө header-ээс scope авдаггүй.)
func newEnv(t *testing.T, cfg Config) *env {
	t.Helper()
	tx := testkit.TxDB(t)
	ensureSchema(t, tx)
	cfg.DB = tx
	mw, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	e := &env{tx: tx, status: 201}
	app := fiber.New()
	app.Use(func(c fiber.Ctx) error {
		if u := c.Get("X-Test-User"); u != "" {
			return testkit.WithClaims(testkit.Claims(u, c.Get("X-Test-Org"), 1))(c)
		}
		return c.Next()
	})
	app.Use(mw)
	app.All("/pay", func(c fiber.Ctx) error {
		e.calls++
		return c.Status(e.status).JSON(fiber.Map{"n": e.calls, "body": string(c.Body())})
	})
	e.app = app
	return e
}

func (e *env) do(t *testing.T, method, key, user, org, body string) (*http.Response, string) {
	t.Helper()
	req := httptest.NewRequest(method, "/pay", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set(Header, key)
	}
	if user != "" {
		req.Header.Set("X-Test-User", user)
		req.Header.Set("X-Test-Org", org)
	}
	resp, err := e.app.Test(req, fiber.TestConfig{Timeout: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func TestKeyRegexAndHash(t *testing.T) {
	for _, k := range []string{"a", "A-b_c:d.e", strings.Repeat("x", 128)} {
		if !keyRe.MatchString(k) {
			t.Errorf("%q зөв байх ёстой", k)
		}
	}
	for _, k := range []string{"", strings.Repeat("x", 129), "a b", "a/b", "а", "a;b"} {
		if keyRe.MatchString(k) {
			t.Errorf("%q буруу байх ёстой", k)
		}
	}
	if hashRequest("POST", "/a", []byte("bc")) == hashRequest("POST", "/ab", []byte("c")) {
		t.Error("hash талбарын хилийг ялгах ёстой")
	}
}

func TestNewRequiresDB(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("DB nil → алдаа")
	}
}

func TestReplay(t *testing.T) {
	e := newEnv(t, Config{})
	r1, b1 := e.do(t, "POST", "k1", "u1", orgA, `{"a":1}`)
	if r1.StatusCode != 201 || r1.Header.Get(ReplayedHeader) != "" {
		t.Fatalf("1: %d %v", r1.StatusCode, r1.Header)
	}
	r2, b2 := e.do(t, "POST", "k1", "u1", orgA, `{"a":1}`)
	if r2.StatusCode != 201 || r2.Header.Get(ReplayedHeader) != "true" || b2 != b1 {
		t.Fatalf("2: %d %q vs %q %v", r2.StatusCode, b2, b1, r2.Header)
	}
	if !strings.HasPrefix(r2.Header.Get("Content-Type"), "application/json") {
		t.Fatalf("content-type = %q", r2.Header.Get("Content-Type"))
	}
	if e.calls != 1 {
		t.Fatalf("handler %d удаа дуудагдсан", e.calls)
	}
}

func TestClientErrorIsStoredAndReplayed(t *testing.T) {
	e := newEnv(t, Config{})
	e.status = 400
	e.do(t, "POST", "k400", "u1", orgA, "x")
	e.status = 201
	r, _ := e.do(t, "POST", "k400", "u1", orgA, "x")
	if r.StatusCode != 400 || r.Header.Get(ReplayedHeader) != "true" || e.calls != 1 {
		t.Fatalf("%d calls=%d", r.StatusCode, e.calls)
	}
}

func TestDifferentBodyIs422(t *testing.T) {
	e := newEnv(t, Config{})
	e.do(t, "POST", "k2", "u1", orgA, "one")
	r, b := e.do(t, "POST", "k2", "u1", orgA, "two")
	if r.StatusCode != 422 || !strings.Contains(b, "error") || e.calls != 1 {
		t.Fatalf("%d %s calls=%d", r.StatusCode, b, e.calls)
	}
}

func TestInFlightIs409(t *testing.T) {
	e := newEnv(t, Config{})
	err := e.tx.Exec(`INSERT INTO khan.idempotency_keys (org_id, user_id, key, method, path, request_hash, expires_at)
		VALUES (?, 'u1', 'k3', 'POST', '/pay', ?, now() + interval '1 hour')`,
		orgA, hashRequest("POST", "/pay", []byte("x"))).Error
	if err != nil {
		t.Fatal(err)
	}
	r, _ := e.do(t, "POST", "k3", "u1", orgA, "x")
	if r.StatusCode != 409 || e.calls != 0 {
		t.Fatalf("%d calls=%d", r.StatusCode, e.calls)
	}
	// Өөр агуулгатай бол 422 (409 биш).
	if r, _ := e.do(t, "POST", "k3", "u1", orgA, "y"); r.StatusCode != 422 {
		t.Fatalf("%d", r.StatusCode)
	}
}

func TestServerErrorNotStored(t *testing.T) {
	e := newEnv(t, Config{})
	e.status = 500
	r, _ := e.do(t, "POST", "k5", "u1", orgA, "x")
	if r.StatusCode != 500 {
		t.Fatalf("%d", r.StatusCode)
	}
	var n int64
	e.tx.Table("khan.idempotency_keys").Where("key = ?", "k5").Count(&n)
	if n != 0 {
		t.Fatalf("5xx мөр хадгалагдсан: %d", n)
	}
	e.status = 201
	r, _ = e.do(t, "POST", "k5", "u1", orgA, "x")
	if r.StatusCode != 201 || r.Header.Get(ReplayedHeader) != "" || e.calls != 2 {
		t.Fatalf("retry: %d calls=%d", r.StatusCode, e.calls)
	}
}

func TestHandlerErrorReleasesKey(t *testing.T) {
	tx := testkit.TxDB(t)
	ensureSchema(t, tx)
	mw, _ := New(Config{DB: tx})
	app := fiber.New()
	app.Use(testkit.WithClaims(testkit.Claims("u1", orgA, 1)))
	app.Use(mw)
	app.Post("/pay", func(c fiber.Ctx) error { return fiber.NewError(502, "gateway") })
	req := httptest.NewRequest("POST", "/pay", nil)
	req.Header.Set(Header, "k6")
	if _, err := app.Test(req, fiber.TestConfig{Timeout: 0}); err != nil {
		t.Fatal(err)
	}
	var n int64
	tx.Table("khan.idempotency_keys").Where("key = ?", "k6").Count(&n)
	if n != 0 {
		t.Fatalf("алдааны дараа мөр үлдсэн: %d", n)
	}
}

func TestScopeIsolation(t *testing.T) {
	e := newEnv(t, Config{})
	e.do(t, "POST", "shared", "u1", orgA, "x")
	for _, c := range []struct{ user, org string }{{"u2", orgA}, {"u1", orgB}} {
		r, _ := e.do(t, "POST", "shared", c.user, c.org, "x")
		if r.StatusCode != 201 || r.Header.Get(ReplayedHeader) != "" {
			t.Fatalf("%v: %d %v", c, r.StatusCode, r.Header)
		}
	}
	if e.calls != 3 {
		t.Fatalf("calls = %d", e.calls)
	}
}

func TestInvalidKeyAndRequired(t *testing.T) {
	e := newEnv(t, Config{})
	for _, k := range []string{"bad key", "a/b", strings.Repeat("x", 129)} {
		if r, _ := e.do(t, "POST", k, "u1", orgA, "x"); r.StatusCode != 400 {
			t.Errorf("%q: %d", k, r.StatusCode)
		}
	}
	if r, _ := e.do(t, "POST", "", "u1", orgA, "x"); r.StatusCode != 201 {
		t.Errorf("толгойгүй: %d", r.StatusCode)
	}
	if e.calls != 1 {
		t.Errorf("calls = %d", e.calls)
	}
}

func TestRequired(t *testing.T) {
	e := newEnv(t, Config{Required: true})
	if r, _ := e.do(t, "POST", "", "u1", orgA, "x"); r.StatusCode != 400 || e.calls != 0 {
		t.Errorf("required: %d", r.StatusCode)
	}
}

func TestMethodsAndNoClaims(t *testing.T) {
	e := newEnv(t, Config{})
	// GET дамжина (key байсан ч хадгалахгүй).
	e.do(t, "GET", "kg", "u1", orgA, "")
	r, _ := e.do(t, "GET", "kg", "u1", orgA, "")
	if r.Header.Get(ReplayedHeader) != "" || e.calls != 2 {
		t.Fatalf("GET replay хийгдсэн: calls=%d", e.calls)
	}
	// Claims-гүй → 401, handler ажиллахгүй.
	if r, _ := e.do(t, "POST", "kn", "", "", "x"); r.StatusCode != 401 || e.calls != 2 {
		t.Fatalf("claims-гүй: %d calls=%d", r.StatusCode, e.calls)
	}
	// DELETE хамрагдана.
	e.do(t, "DELETE", "kd", "u1", orgA, "")
	if r, _ := e.do(t, "DELETE", "kd", "u1", orgA, ""); r.Header.Get(ReplayedHeader) != "true" {
		t.Fatalf("DELETE replay хийгдээгүй")
	}
}

func TestCustomMethods(t *testing.T) {
	e := newEnv(t, Config{Methods: []string{"POST"}})
	e.do(t, "PUT", "kp", "u1", orgA, "")
	if r, _ := e.do(t, "PUT", "kp", "u1", orgA, ""); r.Header.Get(ReplayedHeader) != "" {
		t.Fatalf("PUT хамрагдсан")
	}
}

func TestHeaderOrgSpoofIgnored(t *testing.T) {
	// X-Org-ID header scope-д нөлөөлөхгүй: ижил JWT claims → ижил scope.
	e := newEnv(t, Config{})
	req := func(spoof string) *http.Response {
		r := httptest.NewRequest("POST", "/pay", strings.NewReader("x"))
		r.Header.Set(Header, "spoof")
		r.Header.Set("X-Test-User", "u1")
		r.Header.Set("X-Test-Org", orgA)
		r.Header.Set("X-Org-ID", spoof)
		resp, err := e.app.Test(r, fiber.TestConfig{Timeout: 0})
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	req(orgA)
	if r := req(orgB); r.Header.Get(ReplayedHeader) != "true" {
		t.Fatal("X-Org-ID scope өөрчилсөн")
	}
}

func TestExpiredKeyTreatedAbsentAndPurge(t *testing.T) {
	e := newEnv(t, Config{})
	err := e.tx.Exec(`INSERT INTO khan.idempotency_keys
		(org_id, user_id, key, method, path, request_hash, status, response, completed_at, expires_at)
		VALUES (?, 'u1', 'kx', 'POST', '/pay', 'other', 200, 'old', now(), now() - interval '1 second')`, orgA).Error
	if err != nil {
		t.Fatal(err)
	}
	r, _ := e.do(t, "POST", "kx", "u1", orgA, "x")
	if r.StatusCode != 201 || r.Header.Get(ReplayedHeader) != "" || e.calls != 1 {
		t.Fatalf("%d calls=%d", r.StatusCode, e.calls)
	}
	// Purge: хугацаа дууссан 1 мөр нэм.
	if err := e.tx.Exec(`INSERT INTO khan.idempotency_keys (org_id, user_id, key, method, path, request_hash, expires_at)
		VALUES ('', 'u9', 'old', 'POST', '/', 'h', now() - interval '1 hour')`).Error; err != nil {
		t.Fatal(err)
	}
	n, err := Purge(context.Background(), e.tx)
	if err != nil || n < 1 {
		t.Fatalf("purge n=%d err=%v", n, err)
	}
	var left int64
	e.tx.Table("khan.idempotency_keys").Where("key = ?", "old").Count(&left)
	if left != 0 {
		t.Fatal("purge устгаагүй")
	}
}

func TestTTLApplied(t *testing.T) {
	e := newEnv(t, Config{TTL: time.Hour})
	e.do(t, "POST", "kttl", "u1", orgA, "x")
	var ok bool
	e.tx.Raw(`SELECT expires_at BETWEEN now() + interval '59 minutes' AND now() + interval '61 minutes'
		FROM khan.idempotency_keys WHERE key = 'kttl'`).Scan(&ok)
	if !ok {
		t.Fatal("TTL тохируулагдаагүй")
	}
}
