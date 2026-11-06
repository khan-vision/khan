package obs

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/golang-jwt/jwt/v5"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func newLogger(level slog.Level) (*slog.Logger, *syncBuf) {
	buf := &syncBuf{}
	return slog.New(slog.NewJSONHandler(buf, HandlerOptions(&slog.HandlerOptions{Level: level}))), buf
}

func lines(t *testing.T, buf *syncBuf) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, ln := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if ln == "" {
			continue
		}
		m := map[string]any{}
		if err := json.Unmarshal([]byte(ln), &m); err != nil {
			t.Fatalf("%q: %v", ln, err)
		}
		out = append(out, m)
	}
	return out
}

func do(t *testing.T, app *fiber.App, req *http.Request) (*http.Response, string) {
	t.Helper()
	resp, err := app.Test(req, fiber.TestConfig{Timeout: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func TestValidRequestID(t *testing.T) {
	ok := []string{"a", "abc-123_X.y", strings.Repeat("a", 64)}
	bad := []string{"", strings.Repeat("a", 65), "a b", "a\nb", "а", "a/b", "a;b"}
	for _, s := range ok {
		if !ValidRequestID(s) {
			t.Errorf("хүчинтэй байх ёстой: %q", s)
		}
	}
	for _, s := range bad {
		if ValidRequestID(s) {
			t.Errorf("хүчингүй байх ёстой: %q", s)
		}
	}
}

func TestRequestIDMiddleware(t *testing.T) {
	app := fiber.New()
	app.Use(RequestID())
	app.Get("/", func(c fiber.Ctx) error { return c.SendString(RequestIDFrom(c.Context())) })

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Request-ID", "client-id.1")
	resp, body := do(t, app, req)
	if resp.Header.Get("X-Request-ID") != "client-id.1" || body != "client-id.1" {
		t.Fatalf("%v %q", resp.Header, body)
	}

	req = httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Request-ID", "bad id!")
	resp, body = do(t, app, req)
	id := resp.Header.Get("X-Request-ID")
	if id == "bad id!" || len(id) != 32 || !ValidRequestID(id) || body != id {
		t.Fatalf("үүсгэсэн id: %q %q", id, body)
	}
	if NewRequestID() == NewRequestID() {
		t.Fatal("id давхцлаа")
	}
}

func TestMiddlewareFromWith(t *testing.T) {
	l, buf := newLogger(slog.LevelInfo)
	app := fiber.New()
	app.Use(RequestID(), Middleware(l))
	app.Use(func(c fiber.Ctx) error {
		c.Locals("user", &jwt.Token{Claims: jwt.MapClaims{"org_id": "org-9", "id": "u-1"}, Valid: true})
		return c.Next()
	})
	app.Get("/", func(c fiber.Ctx) error {
		With(c).Info("hello")
		return nil
	})
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Request-ID", "rid-1")
	do(t, app, req)
	ls := lines(t, buf)
	if len(ls) != 1 || ls[0]["request_id"] != "rid-1" || ls[0]["org_id"] != "org-9" || ls[0]["user_id"] != "u-1" {
		t.Fatalf("%v", ls)
	}
	if From(context.Background()) != slog.Default() {
		t.Fatal("fallback slog.Default() биш")
	}
}

func TestAccessLog(t *testing.T) {
	l, buf := newLogger(slog.LevelDebug)
	app := fiber.New()
	app.Use(RequestID(), Middleware(l), AccessLog(WithSkipPaths("/healthz")))
	app.Get("/users/:id", func(c fiber.Ctx) error { return c.SendString("hello") })
	app.Get("/missing", func(c fiber.Ctx) error { return fiber.ErrNotFound })
	app.Get("/boom", func(c fiber.Ctx) error { return errors.New("db: password=hunter2") })
	app.Get("/healthz", func(c fiber.Ctx) error { return nil })

	do(t, app, httptest.NewRequest("GET", "/users/42?access_token=SECRETVALUE", nil))
	do(t, app, httptest.NewRequest("GET", "/missing", nil))
	do(t, app, httptest.NewRequest("GET", "/boom", nil))
	do(t, app, httptest.NewRequest("GET", "/healthz", nil))

	if strings.Contains(buf.String(), "SECRETVALUE") || strings.Contains(buf.String(), "hunter2") {
		t.Fatalf("нууц гоожсон: %s", buf.String())
	}
	ls := lines(t, buf)
	if len(ls) != 3 {
		t.Fatalf("3 мөр хүлээсэн (healthz алгасна): %v", ls)
	}
	a := ls[0]
	if a["method"] != "GET" || a["path"] != "/users/:id" || a["status"] != float64(200) || a["level"] != "INFO" ||
		a["bytes"] != float64(5) || a["ip"] == "" || a["duration_ms"] == nil || a["request_id"] == nil {
		t.Fatalf("%v", a)
	}
	if ls[1]["level"] != "WARN" || ls[1]["status"] != float64(404) {
		t.Fatalf("%v", ls[1])
	}
	if ls[2]["level"] != "ERROR" || ls[2]["status"] != float64(500) {
		t.Fatalf("%v", ls[2])
	}
}

func TestRedactString(t *testing.T) {
	tok := "EAAeJQrKmCF0BSiz3" + strings.Repeat("x", 120)
	cases := []struct{ in, want string }{
		{`non-retryable err=Post "https://graph.facebook.com/v23.0/1_2/comments?access_token=` + tok + `": context deadline exceeded`,
			`non-retryable err=Post "https://graph.facebook.com/v23.0/1_2/comments?access_token=***": context deadline exceeded`},
		{"GET /me?fields=id&access_token=abc123&appsecret_proof=deadbeef x",
			"GET /me?fields=id&access_token=***&appsecret_proof=*** x"},
		{"Authorization: Bearer abcdefghijklmnopqrstuvwxyz0123", "Authorization: Bearer ***"},
		{`{"token":"` + tok + `"}`, `{"token":"EAA***"}`},
		{"login?password=pw1&user=bat", "login?password=***&user=bat"},
		{"jwt eyJhbGciOiJIUzI1NiJ9.eyJpZCI6InUxIn0.abcdefghijk done", "jwt eyJ*** done"},
		// Хөндөхгүй.
		{"comment-throttle: ok page=1793847014001733 attempt=1", "comment-throttle: ok page=1793847014001733 attempt=1"},
		{"Bearer token хоосон", "Bearer token хоосон"},
	}
	for _, c := range cases {
		if got := RedactString(c.in); got != c.want {
			t.Errorf("\n in: %q\ngot: %q\nwant %q", c.in, got, c.want)
		}
	}
}

func TestReplaceAttrAndHandlerOptions(t *testing.T) {
	buf := &syncBuf{}
	prevCalled := false
	h := slog.NewJSONHandler(buf, HandlerOptions(&slog.HandlerOptions{
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr { prevCalled = true; return a },
	}))
	l := slog.New(h)
	l.Info("url ?access_token=abc", "password", "hunter2", "Authorization", "Bearer x",
		slog.Group("req", slog.String("api_key", "k"), slog.String("note", "tok=1 ?token=zzz")),
		"err", errors.New("fail ?client_secret=s3"), "n", 5)
	out := buf.String()
	for _, leak := range []string{"hunter2", "abc", "zzz", "s3", `"k"`} {
		if strings.Contains(out, leak) {
			t.Errorf("%q гоожсон: %s", leak, out)
		}
	}
	if !prevCalled || !strings.Contains(out, `"n":5`) || !strings.Contains(out, "token=***") {
		t.Fatalf("%s", out)
	}
	if !SensitiveKey("X-Api_Key") || SensitiveKey("user_id") {
		t.Fatal("SensitiveKey")
	}
}

// fakeDriver — Ping-ийг удирдах database/sql driver (DB-гүй DBCheck тест).
type fakeConnector struct{ pingErr error }

func (f fakeConnector) Connect(context.Context) (driver.Conn, error) { return fakeConn{f.pingErr}, nil }
func (f fakeConnector) Driver() driver.Driver                        { return nil }

type fakeConn struct{ pingErr error }

func (fakeConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unsupported") }
func (fakeConn) Close() error                        { return nil }
func (fakeConn) Begin() (driver.Tx, error)           { return nil, errors.New("unsupported") }
func (c fakeConn) Ping(context.Context) error        { return c.pingErr }

func fakeGorm(t *testing.T, pingErr error) *gorm.DB {
	t.Helper()
	sdb := sql.OpenDB(fakeConnector{pingErr})
	t.Cleanup(func() { _ = sdb.Close() })
	g, err := gorm.Open(postgres.New(postgres.Config{Conn: sdb}), &gorm.Config{
		DisableAutomaticPing: true, SkipDefaultTransaction: true, Logger: logger.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func TestDBCheck(t *testing.T) {
	if err := DBCheck(fakeGorm(t, nil))(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := DBCheck(fakeGorm(t, errors.New("down")))(context.Background()); err == nil {
		t.Fatal("ping алдаа хүлээсэн")
	}
}

func TestHealth(t *testing.T) {
	app := fiber.New()
	Health(app, map[string]Check{
		"ok":    func(context.Context) error { return nil },
		"slow":  func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() },
		"panic": func(context.Context) error { panic("boom") },
		"err":   func(context.Context) error { return errors.New("dial tcp 10.0.0.1: password=hunter2") },
	}, WithCheckTimeout(50*time.Millisecond))

	resp, body := do(t, app, httptest.NewRequest("GET", "/healthz", nil))
	if resp.StatusCode != 200 || body != `{"status":"ok"}` {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	start := time.Now()
	resp, body = do(t, app, httptest.NewRequest("GET", "/readyz", nil))
	if time.Since(start) > time.Second {
		t.Fatal("шалгалтууд зэрэг ажиллаагүй / timeout ажиллаагүй")
	}
	var out struct {
		Status string
		Checks map[string]struct{ Status, Error string }
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 503 || out.Status != "unavailable" || out.Checks["ok"].Status != "ok" ||
		out.Checks["slow"].Status != "fail" || out.Checks["panic"].Status != "fail" || out.Checks["err"].Status != "fail" {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if strings.Contains(body, "hunter2") || strings.Contains(body, "10.0.0.1") {
		t.Fatalf("алдааны дэлгэрэнгүй гоожсон: %s", body)
	}

	app2 := fiber.New()
	Health(app2, map[string]Check{"db": DBCheck(fakeGorm(t, nil))})
	resp, body = do(t, app2, httptest.NewRequest("GET", "/readyz", nil))
	if resp.StatusCode != 200 || !strings.Contains(body, `"status":"ok"`) {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}

	app3 := fiber.New()
	Health(app3, map[string]Check{"err": func(context.Context) error { return errors.New("x password=hunter2") }}, WithErrorDetails())
	_, body = do(t, app3, httptest.NewRequest("GET", "/readyz", nil))
	if !strings.Contains(body, "password=***") || strings.Contains(body, "hunter2") {
		t.Fatalf("%s", body)
	}
}

func TestGormLogger(t *testing.T) {
	l, buf := newLogger(slog.LevelDebug)
	gl := GormLogger(l, 100*time.Millisecond)
	ctx := context.WithValue(context.Background(), requestIDKey, "rid-7")
	fc := func() (string, int64) { return "SELECT * FROM t WHERE token='x' ?access_token=abc", 3 }

	gl.Trace(ctx, time.Now(), fc, nil)                                   // хурдан ⇒ лог алга
	gl.Trace(ctx, time.Now(), fc, gorm.ErrRecordNotFound)                // үл тооно
	gl.Trace(ctx, time.Now().Add(-time.Second), fc, nil)                 // удаан
	gl.Trace(ctx, time.Now(), fc, errors.New("syntax password=hunter2")) // алдаа
	long := func() (string, int64) { return strings.Repeat("я", 2000), 0 }
	gl.Trace(ctx, time.Now(), long, errors.New("e"))

	ls := lines(t, buf)
	if len(ls) != 3 {
		t.Fatalf("3 мөр хүлээсэн: %s", buf.String())
	}
	if ls[0]["level"] != "WARN" || ls[0]["request_id"] != "rid-7" {
		t.Fatalf("%v", ls[0])
	}
	if ls[1]["level"] != "ERROR" || strings.Contains(buf.String(), "hunter2") || strings.Contains(buf.String(), "abc") {
		t.Fatalf("%v", ls[1])
	}
	if s := ls[2]["sql"].(string); len([]rune(s)) != MaxSQLLogLen+1 {
		t.Fatalf("SQL тайрагдаагүй: %d", len([]rune(s)))
	}

	// Info горим: хурдан query ч Debug-аар.
	l2, buf2 := newLogger(slog.LevelDebug)
	GormLogger(l2, 0).LogMode(logger.Info).Trace(ctx, time.Now(), fc, nil)
	if len(lines(t, buf2)) != 1 {
		t.Fatal("Info горимд query лог алга")
	}
	// Silent
	l3, buf3 := newLogger(slog.LevelDebug)
	GormLogger(l3, 0).LogMode(logger.Silent).Trace(ctx, time.Now(), fc, errors.New("x"))
	if buf3.String() != "" {
		t.Fatal("Silent лог бичлээ")
	}
}
