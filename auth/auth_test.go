package auth

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/golang-jwt/jwt/v5"
	"github.com/khan-vision/khan/config"
	"github.com/khan-vision/khan/tenant"
)

var (
	secretA = strings.Repeat("a", 32)
	secretB = strings.Repeat("b", 32)
	secretC = strings.Repeat("c", 32)
)

func mustRing(t *testing.T, active Key, prev ...Key) *Keyring {
	t.Helper()
	kr, err := NewKeyring(active, prev...)
	if err != nil {
		t.Fatal(err)
	}
	return kr
}

func claims() jwt.MapClaims {
	return jwt.MapClaims{"id": "u1", "login": "bat", "role": 3, "org_id": "org-1"}
}

func TestNewKeyringValidation(t *testing.T) {
	if _, err := NewKeyring(Key{ID: "k1", Secret: []byte("short")}); err == nil {
		t.Fatal("богино secret хүлээн авав")
	}
	if _, err := NewKeyring(Key{ID: "k1", Secret: []byte(secretA)}, Key{ID: "k1", Secret: []byte(secretB)}); err == nil {
		t.Fatal("давхар id хүлээн авав")
	}
	if _, err := NewKeyring(Key{ID: "bad id", Secret: []byte(secretA)}); err == nil {
		t.Fatal("буруу id хүлээн авав")
	}
}

func TestRotation(t *testing.T) {
	old := mustRing(t, Key{ID: "k1", Secret: []byte(secretA)})
	oldTok, err := Issue(old, claims(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	rotated := mustRing(t, Key{ID: "k2", Secret: []byte(secretB)}, Key{ID: "k1", Secret: []byte(secretA)})
	if _, err := rotated.Parse(oldTok); err != nil {
		t.Fatalf("өмнөх түлхүүрийн token хүчинтэй байх ёстой: %v", err)
	}
	newTok, _ := Issue(rotated, claims(), time.Hour)
	tok, err := rotated.Parse(newTok)
	if err != nil || tok.Header["kid"] != "k2" {
		t.Fatalf("шинэ token: %v %v", err, tok)
	}
	// Эргүүлэлтээс өмнөх keyring шинэ token-ийг мэдэхгүй (kid үл мэдэгдэх).
	if _, err := old.Parse(newTok); err == nil {
		t.Fatal("үл мэдэгдэх kid татгалзаагүй")
	}
}

func TestUnknownKidRejectedEvenIfSecretMatches(t *testing.T) {
	signer := mustRing(t, Key{ID: "ghost", Secret: []byte(secretA)})
	raw, _ := Issue(signer, claims(), time.Hour)
	kr := mustRing(t, Key{ID: "k1", Secret: []byte(secretA)})
	_, err := kr.Parse(raw)
	if err == nil || !strings.Contains(err.Error(), "kid") {
		t.Fatalf("ErrUnknownKey хүлээсэн: %v", err)
	}
}

func TestKidPinsKey(t *testing.T) {
	// kid=k1 гэсэн боловч k2-ийн secret-ээр гарын үсэглэсэн ⇒ k2-оор оролдохгүй.
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"exp": time.Now().Add(time.Hour).Unix()})
	tok.Header["kid"] = "k1"
	raw, _ := tok.SignedString([]byte(secretB))
	kr := mustRing(t, Key{ID: "k1", Secret: []byte(secretA)}, Key{ID: "k2", Secret: []byte(secretB)})
	if _, err := kr.Parse(raw); err == nil {
		t.Fatal("kid-ийн түлхүүрээс өөрөөр шалгагдлаа")
	}
}

func legacyToken(t *testing.T, secret string, c jwt.MapClaims) string {
	t.Helper()
	raw, err := jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString([]byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestLegacyNoKid(t *testing.T) {
	kr := mustRing(t, Key{ID: "k2", Secret: []byte(secretB)}, Key{ID: "k1", Secret: []byte(secretA)})
	exp := time.Now().Add(time.Hour).Unix()
	for name, secret := range map[string]string{"previous": secretA, "active": secretB} {
		raw := legacyToken(t, secret, jwt.MapClaims{"id": "u1", "role": float64(1), "exp": exp})
		if _, err := kr.Parse(raw); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if _, err := kr.Parse(legacyToken(t, secretC, jwt.MapClaims{"exp": exp})); err == nil {
		t.Fatal("үл мэдэгдэх secret хүлээн авав")
	}
}

func TestExpiredAndMissingExp(t *testing.T) {
	kr := mustRing(t, Key{ID: "k1", Secret: []byte(secretA)})
	expired := legacyToken(t, secretA, jwt.MapClaims{"exp": time.Now().Add(-time.Minute).Unix()})
	if _, err := kr.Parse(expired); err == nil {
		t.Fatal("хугацаа дууссан token хүлээн авав")
	}
	if _, err := kr.Parse(expired, WithLeeway(time.Hour)); err != nil {
		t.Fatalf("leeway дотор хүчинтэй байх ёстой: %v", err)
	}
	if _, err := kr.Parse(legacyToken(t, secretA, jwt.MapClaims{"id": "u1"})); err == nil {
		t.Fatal("exp-гүй token хүлээн авав")
	}
	if _, err := Issue(kr, claims(), 0); err == nil {
		t.Fatal("ttl=0 хүлээн авав")
	}
}

func TestWrongAlgorithms(t *testing.T) {
	kr := mustRing(t, Key{ID: "k1", Secret: []byte(secretA)})
	exp := time.Now().Add(time.Hour).Unix()

	none, _ := jwt.NewWithClaims(jwt.SigningMethodNone, jwt.MapClaims{"exp": exp}).SignedString(jwt.UnsafeAllowNoneSignatureType)
	if _, err := kr.Parse(none); err == nil {
		t.Fatal("alg=none хүлээн авав")
	}
	for _, m := range []*jwt.SigningMethodHMAC{jwt.SigningMethodHS384, jwt.SigningMethodHS512} {
		raw, _ := jwt.NewWithClaims(m, jwt.MapClaims{"exp": exp}).SignedString([]byte(secretA))
		if _, err := kr.Parse(raw); err == nil {
			t.Fatalf("%s хүлээн авав", m.Alg())
		}
	}
	// RS256 header-тэй, HMAC secret-ээр "гарын үсэглэсэн" (alg confusion) гар хийц token.
	hdr := "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9" // {"alg":"RS256","typ":"JWT"}
	if _, err := kr.Parse(hdr + ".e30.AAAA"); err == nil {
		t.Fatal("alg=RS256 хүлээн авав")
	}
	if _, err := kr.Parse("garbage"); err == nil {
		t.Fatal("хог хүлээн авав")
	}
}

func TestKeyringFromConfig(t *testing.T) {
	kr, err := KeyringFromConfig(config.JWT{
		Secret: secretB, KeyID: "k2",
		PreviousSecrets: []string{"k1:" + secretA, secretC},
	})
	if err != nil {
		t.Fatal(err)
	}
	if kr.ActiveID() != "k2" {
		t.Fatal(kr.ActiveID())
	}
	for _, s := range []string{secretA, secretC} {
		if _, err := kr.Parse(legacyToken(t, s, jwt.MapClaims{"exp": time.Now().Add(time.Hour).Unix()})); err != nil {
			t.Fatal(err)
		}
	}
	// Алдааны мессежид secret орохгүй.
	_, err = KeyringFromConfig(config.JWT{Secret: secretA, KeyID: "k1", PreviousSecrets: []string{"short-secret"}})
	if err == nil || strings.Contains(err.Error(), "short-secret") {
		t.Fatalf("алдаа: %v", err)
	}
	if _, err := KeyringFromConfig(config.JWT{Secret: secretA, KeyID: "k1", PreviousSecrets: []string{"k1:" + secretB}}); err == nil {
		t.Fatal("давхар id хүлээн авав")
	}
}

func newApp(kr *Keyring, opts ...Option) *fiber.App {
	app := fiber.New()
	app.Get("/me", Required(kr, opts...), func(c fiber.Ctx) error {
		org, ok := tenant.Require(c)
		if !ok {
			return nil
		}
		u, _ := User(c)
		return c.JSON(fiber.Map{"org": org, "user": u})
	})
	app.Get("/opt", Optional(kr, opts...), func(c fiber.Ctx) error {
		_, ok := User(c)
		return c.JSON(fiber.Map{"auth": ok})
	})
	return app
}

func do(t *testing.T, app *fiber.App, req *http.Request) (int, string, http.Header) {
	t.Helper()
	resp, err := app.Test(req, fiber.TestConfig{Timeout: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header
}

func TestMiddlewareHeaderCookieAndTenant(t *testing.T) {
	kr := mustRing(t, Key{ID: "k1", Secret: []byte(secretA)})
	app := newApp(kr)
	raw, _ := Issue(kr, jwt.MapClaims{"id": 7.0, "login": "bat", "role": "3", "org_id": "org-1", "portal_only": true, "sid": "s1"}, time.Hour)

	req := httptest.NewRequest("GET", "/me", nil)
	req.Header.Set("Authorization", "bearer "+raw)
	code, body, _ := do(t, app, req)
	if code != 200 {
		t.Fatalf("header: %d %s", code, body)
	}
	var out struct {
		Org  string
		User Principal
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	want := Principal{ID: "7", Login: "bat", Role: 3, OrgID: "org-1", PortalOnly: true, SessionID: "s1"}
	if out.Org != "org-1" || out.User != want {
		t.Fatalf("%+v", out)
	}

	req = httptest.NewRequest("GET", "/me", nil)
	req.AddCookie(&http.Cookie{Name: "token", Value: raw})
	if code, body, _ = do(t, app, req); code != 200 {
		t.Fatalf("cookie: %d %s", code, body)
	}

	// Header байвал cookie-д шилжихгүй: буруу header + зөв cookie ⇒ 401.
	req = httptest.NewRequest("GET", "/me", nil)
	req.Header.Set("Authorization", "Bearer junk")
	req.AddCookie(&http.Cookie{Name: "token", Value: raw})
	if code, _, _ = do(t, app, req); code != 401 {
		t.Fatalf("code=%d", code)
	}

	code, body, _ = do(t, app, httptest.NewRequest("GET", "/me", nil))
	if code != 401 || body != `{"error":"Зөвшөөрөлгүй"}` {
		t.Fatalf("%d %s", code, body)
	}
}

func TestTenantRequireRejectsTokenWithoutOrg(t *testing.T) {
	kr := mustRing(t, Key{ID: "k1", Secret: []byte(secretA)})
	raw, _ := Issue(kr, jwt.MapClaims{"id": "u1"}, time.Hour)
	req := httptest.NewRequest("GET", "/me", nil)
	req.Header.Set("Authorization", "Bearer "+raw)
	if code, _, _ := do(t, newApp(kr), req); code != 401 {
		t.Fatalf("code=%d", code)
	}
}

func TestOptionalAndRedirect(t *testing.T) {
	kr := mustRing(t, Key{ID: "k1", Secret: []byte(secretA)})
	app := newApp(kr, WithUnauthorized(RedirectToLogin("/login")))

	code, body, _ := do(t, app, httptest.NewRequest("GET", "/opt", nil))
	if code != 200 || !strings.Contains(body, `"auth":false`) {
		t.Fatalf("%d %s", code, body)
	}
	req := httptest.NewRequest("GET", "/me", nil)
	req.Header.Set("Accept", "text/html")
	code, _, h := do(t, app, req)
	if code != 302 || h.Get("Location") != "/login" {
		t.Fatalf("%d %v", code, h)
	}
	req = httptest.NewRequest("GET", "/me", nil)
	req.Header.Set("Accept", "text/html")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	if code, _, _ = do(t, app, req); code != 401 {
		t.Fatalf("ajax code=%d", code)
	}
}

func TestUserMapIsCopy(t *testing.T) {
	kr := mustRing(t, Key{ID: "k1", Secret: []byte(secretA)})
	app := fiber.New()
	app.Get("/", Required(kr), func(c fiber.Ctx) error {
		m := UserMap(c)
		m["role"] = 99
		cl, _ := tenant.Claims(c)
		return c.JSON(fiber.Map{"map": m["login"], "orig": cl["role"]})
	})
	raw, _ := Issue(kr, claims(), time.Hour)
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+raw)
	_, body, _ := do(t, app, req)
	if !strings.Contains(body, `"orig":3`) || !strings.Contains(body, `"map":"bat"`) {
		t.Fatal(body)
	}
	app2 := fiber.New()
	app2.Get("/", func(c fiber.Ctx) error {
		if _, ok := User(c); ok || UserMap(c) == nil || len(UserMap(c)) != 0 {
			t.Error("нэвтрээгүй үед хоосон map хүлээсэн")
		}
		return nil
	})
	do(t, app2, httptest.NewRequest("GET", "/", nil))
}

func TestPassword(t *testing.T) {
	h, err := HashPassword("нууц-123", WithCost(4))
	if err != nil {
		t.Fatal(err)
	}
	if !CheckPassword(h, "нууц-123") || CheckPassword(h, "буруу") {
		t.Fatal("password шалгалт")
	}
	if CheckPassword("", "x") || CheckPassword("not-a-hash", "x") {
		t.Fatal("хоосон/буруу hash true буцаав")
	}
	if _, err := HashPassword(strings.Repeat("x", 100), WithCost(4)); err == nil {
		t.Fatal("72 байтаас урт хүлээн авав")
	}
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (f *fakeClock) Now() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return f.t }
func (f *fakeClock) Add(d time.Duration) {
	f.mu.Lock()
	f.t = f.t.Add(d)
	f.mu.Unlock()
}

func TestLimiterEscalatingLockout(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_000_000, 0)}
	l := NewLimiter(LimiterConfig{Steps: DefaultLoginSteps(), Now: clk.Now})
	k := "bat"
	for i := 0; i < 2; i++ {
		if ok, _ := l.Allow(k); !ok {
			t.Fatal("түрүүлж түгжигдэв")
		}
		l.Fail(k)
	}
	if d := l.Fail(k); d != 10*time.Minute {
		t.Fatalf("3 дахь алдаа: %v", d)
	}
	if ok, ra := l.Allow(k); ok || ra != 10*time.Minute {
		t.Fatalf("%v %v", ok, ra)
	}
	clk.Add(10*time.Minute + time.Second)
	if ok, _ := l.Allow(k); !ok {
		t.Fatal("түгжээ дууссан")
	}
	if d := l.Fail(k); d != 0 { // 4
		t.Fatalf("4: %v", d)
	}
	l.Fail(k) // 5
	if d := l.Fail(k); d != time.Hour {
		t.Fatalf("6: %v", d)
	}
	clk.Add(time.Hour + time.Second)
	l.Fail(k)
	l.Fail(k)
	if d := l.Fail(k); d != 24*time.Hour { // 9
		t.Fatalf("9: %v", d)
	}
	if d := func() time.Duration { clk.Add(25 * time.Hour); return l.Fail(k) }(); d != 0 {
		// 25ц өнгөрсөн ⇒ FailureTTL (24ц) дууссан тул тоолуур 1-ээс эхэлнэ.
		t.Fatalf("TTL-ийн дараа: %v", d)
	}
	l.Reset(k)
	if l.Len() != 0 {
		t.Fatal("Reset")
	}
}

func TestLimiterSlidingWindowAndSweep(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_000_000, 0)}
	l := NewLimiter(LimiterConfig{Window: time.Minute, MaxRequests: 2, Now: clk.Now})
	l.Allow("ip")
	clk.Add(20 * time.Second)
	l.Allow("ip")
	ok, ra := l.Allow("ip")
	if ok || ra != 40*time.Second {
		t.Fatalf("%v %v", ok, ra)
	}
	clk.Add(41 * time.Second) // эхний хүсэлт цонхноос гарсан
	if ok, _ := l.Allow("ip"); !ok {
		t.Fatal("цонх гулсаагүй")
	}
	clk.Add(2 * time.Minute)
	l.Sweep()
	if l.Len() != 0 {
		t.Fatalf("Sweep: %d", l.Len())
	}
}

func TestLimiterConcurrent(t *testing.T) {
	l := NewLimiter(LimiterConfig{Window: time.Minute, MaxRequests: 50, Steps: DefaultLoginSteps(), SweepEvery: time.Millisecond})
	var wg sync.WaitGroup
	var allowed sync.Map
	for g := 0; g < 32; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				key := []string{"a", "b", "c"}[i%3]
				if ok, _ := l.Allow(key); ok {
					allowed.Store(key, true)
				}
				if i%7 == 0 {
					l.Fail(key)
				}
				if i%50 == 0 {
					l.Reset("c")
					l.Sweep()
				}
				_ = l.Len()
			}
		}()
	}
	wg.Wait()
}

func TestLimiterMaxRequestsExact(t *testing.T) {
	l := NewLimiter(LimiterConfig{Window: time.Hour, MaxRequests: 100})
	var wg sync.WaitGroup
	var mu sync.Mutex
	n := 0
	for g := 0; g < 20; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				if ok, _ := l.Allow("k"); ok {
					mu.Lock()
					n++
					mu.Unlock()
				}
			}
		}()
	}
	wg.Wait()
	if n != 100 {
		t.Fatalf("зөвшөөрсөн=%d, хүлээсэн 100", n)
	}
}
