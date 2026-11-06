package upload

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/golang-jwt/jwt/v5"
	"github.com/khan-vision/khan/tenant"
)

const (
	secretA = "0123456789abcdef0123456789abcdef-A" // ≥ 32 байт
	secretB = "fedcba9876543210fedcba9876543210-B"
	orgA    = "11111111-1111-1111-1111-111111111111"
	orgB    = "22222222-2222-2222-2222-222222222222"
)

type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *testClock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func mustSigner(t testing.TB, clk *testClock, active SignKey, prev []SignKey, opts ...SignerOption) *Signer {
	t.Helper()
	opts = append([]SignerOption{WithSignClock(clk.Now)}, opts...)
	s, err := NewSigner(active, prev, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

type privEnv struct {
	u      *Uploader
	app    *fiber.App
	clk    *testClock
	signer *Signer
}

// newPrivEnv — Handler(POST /api/upload), PrivateHandler(/api/upload/private),
// ServePrivate(/files/private/*), ServePrivateAuth(/api/files/*), SignHandler(/api/sign),
// public Serve. Principal-ийг X-Test-User / X-Test-Org header-ээс (JWT middleware-ийн
// оронд) тавина; header байхгүй бол нэвтрээгүй.
func newPrivEnv(t testing.TB, authz AuthzFunc, mut ...func(*Config)) *privEnv {
	t.Helper()
	clk := &testClock{t: testNow}
	signer := mustSigner(t, clk, SignKey{"k1", []byte(secretA)}, nil)
	u := newTestUploader(t, append([]func(*Config){func(c *Config) {
		c.Signer = signer
		c.Now = clk.Now
	}}, mut...)...)
	app := fiber.New()
	app.Use(func(c fiber.Ctx) error {
		if uid := c.Get("X-Test-User"); uid != "" {
			claims := jwt.MapClaims{"id": uid}
			if org := c.Get("X-Test-Org"); org != "" {
				claims["org_id"] = org
			}
			c.Locals(tenant.LocalsKey, &jwt.Token{Claims: claims})
		}
		return c.Next()
	})
	app.Post("/api/upload", u.Handler())
	app.Post("/api/upload/private", u.PrivateHandler())
	u.MountPrivateServe(app)
	app.Get("/api/files/*", u.ServePrivateAuth(authz))
	app.Head("/api/files/*", u.ServePrivateAuth(authz))
	app.Get("/api/sign", u.SignHandler(authz))
	app.Post("/api/sign", u.SignHandler(authz))
	u.MountServe(app)
	return &privEnv{u: u, app: app, clk: clk, signer: signer}
}

func (e *privEnv) req(t testing.TB, method, target, user, org string, body io.Reader, hdr ...string) (*http.Response, string) {
	t.Helper()
	r := httptest.NewRequest(method, target, body)
	if user != "" {
		r.Header.Set("X-Test-User", user)
	}
	if org != "" {
		r.Header.Set("X-Test-Org", org)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Set(hdr[i], hdr[i+1])
	}
	return do(t, e.app, r)
}

func (e *privEnv) get(t testing.TB, target string) (*http.Response, string) {
	t.Helper()
	return e.req(t, http.MethodGet, target, "", "", nil)
}

func privUploadReq(t testing.TB, target string, fields map[string]string, filename, ct string, data []byte, user, org string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for k, v := range fields {
		if err := mw.WriteField(k, v); err != nil {
			t.Fatal(err)
		}
	}
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="file"; filename="`+filename+`"`)
	h.Set("Content-Type", ct)
	p, err := mw.CreatePart(h)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = p.Write(data)
	_ = mw.Close()
	r := httptest.NewRequest(http.MethodPost, target, &body)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	if user != "" {
		r.Header.Set("X-Test-User", user)
	}
	if org != "" {
		r.Header.Set("X-Test-Org", org)
	}
	return r
}

// upload — private pdf-ийг org-д оруулж Result буцаана.
func (e *privEnv) upload(t testing.TB, org string, data []byte) Result {
	t.Helper()
	resp, body := do(t, e.app, privUploadReq(t, "/api/upload", map[string]string{"visibility": "private"},
		"Geree.pdf", "application/pdf", data, "u1", org))
	if resp.StatusCode != 200 {
		t.Fatalf("upload status=%d %s", resp.StatusCode, body)
	}
	var r Result
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		t.Fatal(err)
	}
	return r
}

var pdfData = []byte("%PDF-1.4\n1 0 obj<<>>endobj\ntrailer<<>>\n%%EOF\n")

// putPrivate — private storage-д шууд файл тавина (opaque зам буцаана).
func putPrivate(t testing.TB, e *privEnv, org, cat, name string, data []byte) string {
	t.Helper()
	rel := org + "/" + cat + "/2026/11/" + name
	p := filepath.Join(e.u.cfg.Root, "private", filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return "private/" + rel
}

func (e *privEnv) sign(t testing.TB, opaque string, ttl time.Duration, o SignOptions) string {
	t.Helper()
	s, err := e.signer.SignURL(opaque, ttl, o)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// ---- Upload ----

func TestPrivateUploadStoredUnderOrgAndSigned(t *testing.T) {
	e := newPrivEnv(t, nil, func(c *Config) { c.BaseURL = "https://cdn.test" })
	r := e.upload(t, orgA, pdfData)
	if r.Visibility != VisibilityPrivate || !strings.HasPrefix(r.Path, "private/"+orgA+"/documents/2026/11/geree-") {
		t.Fatalf("result=%+v", r)
	}
	if strings.HasPrefix(r.Path, "/") || r.Thumb != "" {
		t.Fatalf("path opaque байх ёстой: %+v", r)
	}
	if !strings.HasPrefix(r.URL, "https://cdn.test/files/private/"+orgA+"/") && !strings.HasPrefix(r.URL, "https://cdn.test/files/private/private/"+orgA+"/") {
		t.Fatalf("url=%q", r.URL)
	}
	if r.ExpiresAt != testNow.Add(5*time.Minute).Unix() {
		t.Fatalf("expires_at=%d", r.ExpiresAt)
	}
	// Диск: <Root>/private/<org>/documents/2026/11/<name>, 0600/0700, public root-д БАЙХГҮЙ.
	disk := filepath.Join(e.u.cfg.Root, "private", orgA, "documents", "2026", "11", r.Name)
	st, err := os.Stat(disk)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("disk=%v %v", err, st)
	}
	if d, _ := os.Stat(filepath.Dir(disk)); d.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode=%v", d.Mode())
	}
	if _, err := os.Stat(filepath.Join(e.u.cfg.Root, "uploaded", "documents")); err == nil {
		t.Fatal("private файл public хавтсанд орсон")
	}
	// Signed URL ажиллана.
	resp, body := e.get(t, strings.TrimPrefix(r.URL, "https://cdn.test"))
	if resp.StatusCode != 200 || body != string(pdfData) {
		t.Fatalf("fetch status=%d", resp.StatusCode)
	}
	// Хариуны Cache-Control: no-store (token-тэй URL кэшлэхгүй).
	resp, _ = do(t, e.app, privUploadReq(t, "/api/upload/private", nil, "a.pdf", "application/pdf", pdfData, "u1", orgA))
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("upload cache=%q", resp.Header.Get("Cache-Control"))
	}
}

func TestPrivateUploadImageThumb(t *testing.T) {
	e := newPrivEnv(t, nil)
	resp, body := do(t, e.app, privUploadReq(t, "/api/upload/private", nil, "id.png", "image/png", pngBytes(t, 400, 400), "u1", orgA))
	if resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode, body)
	}
	var r Result
	_ = json.Unmarshal([]byte(body), &r)
	if !strings.HasPrefix(r.Thumb, "private/"+orgA+"/images/2026/11/thumb_") || r.ThumbURL == "" {
		t.Fatalf("thumb=%+v", r)
	}
	if resp, _ := e.get(t, r.ThumbURL); resp.StatusCode != 200 {
		t.Fatalf("thumb status=%d", resp.StatusCode)
	}
	// Public Serve-ээр thumbnail-д ч хүрэхгүй.
	if resp, _ := e.get(t, "/uploaded/images/2026/11/"+r.Name); resp.StatusCode == 200 {
		t.Fatal("public Serve private зургийг өгсөн")
	}
}

func TestPrivateUploadRequiresAuthenticatedOrgPrincipal(t *testing.T) {
	for _, anon := range []bool{false, true} {
		e := newPrivEnv(t, nil, func(c *Config) { c.AllowAnonymous = anon })
		fields := map[string]string{"visibility": "private"}
		// nэвтрээгүй
		resp, _ := do(t, e.app, privUploadReq(t, "/api/upload", fields, "a.pdf", "application/pdf", pdfData, "", ""))
		if resp.StatusCode != 401 {
			t.Errorf("anon=%v: нэвтрээгүй status=%d", anon, resp.StatusCode)
		}
		resp, _ = do(t, e.app, privUploadReq(t, "/api/upload/private", nil, "a.pdf", "application/pdf", pdfData, "", ""))
		if resp.StatusCode != 401 {
			t.Errorf("anon=%v: PrivateHandler нэвтрээгүй status=%d", anon, resp.StatusCode)
		}
		// org-гүй / буруу org
		for _, org := range []string{"", tenant.ZeroUUID, "../x", "a/b"} {
			resp, _ = do(t, e.app, privUploadReq(t, "/api/upload", fields, "a.pdf", "application/pdf", pdfData, "u1", org))
			if resp.StatusCode != 403 {
				t.Errorf("anon=%v org=%q: status=%d", anon, org, resp.StatusCode)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(newPrivEnv(t, nil).u.cfg.Root, "private", "..")); err != nil {
		t.Fatal(err)
	}
}

func TestPrivateUploadOrgFromJWTOnly(t *testing.T) {
	e := newPrivEnv(t, nil)
	r := do2(t, e, privUploadReq(t, "/api/upload", map[string]string{"visibility": "private", "org_id": orgB, "orgId": orgB},
		"a.pdf", "application/pdf", pdfData, "u1", orgA))
	if org, _ := PrivateOrg(r.Path); org != orgA {
		t.Fatalf("org=%q", org)
	}
	// X-Org-ID header / ?org_id= query үл тооно.
	req := privUploadReq(t, "/api/upload?org_id="+orgB, map[string]string{"visibility": "private"}, "b.pdf", "application/pdf", pdfData, "u1", orgA)
	req.Header.Set("X-Org-ID", orgB)
	r = do2(t, e, req)
	if org, _ := PrivateOrg(r.Path); org != orgA {
		t.Fatalf("org=%q", org)
	}
}

func do2(t testing.TB, e *privEnv, req *http.Request) Result {
	t.Helper()
	resp, body := do(t, e.app, req)
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d %s", resp.StatusCode, body)
	}
	var r Result
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestVisibilityAllowlist(t *testing.T) {
	e := newPrivEnv(t, nil)
	for _, v := range []string{"secret", "PRIVATE ", "../x", "public;private", "1"} {
		resp, _ := do(t, e.app, privUploadReq(t, "/api/upload", map[string]string{"visibility": v}, "a.pdf", "application/pdf", pdfData, "u1", orgA))
		// "PRIVATE " нь trim+lower-ээр private болно (зөвшөөрөгдөнө); бусад 400.
		want := 400
		if strings.EqualFold(strings.TrimSpace(v), "private") {
			want = 200
		}
		if resp.StatusCode != want {
			t.Errorf("visibility=%q status=%d want %d", v, resp.StatusCode, want)
		}
	}
	// Хоосон → default public.
	r := do2(t, e, privUploadReq(t, "/api/upload", nil, "a.png", "image/png", pngBytes(t, 4, 4), "u1", orgA))
	if r.Visibility != "" || !strings.HasPrefix(r.Path, "/uploaded/") {
		t.Fatalf("default public байх ёстой: %+v", r)
	}

	// Default private: талбаргүй → private.
	e2 := newPrivEnv(t, nil, func(c *Config) { c.DefaultVisibility = VisibilityPrivate })
	r = do2(t, e2, privUploadReq(t, "/api/upload", nil, "a.pdf", "application/pdf", pdfData, "u1", orgA))
	if r.Visibility != VisibilityPrivate {
		t.Fatalf("%+v", r)
	}
	// Зөвхөн private зөвшөөрсөн: клиент public хүсэж чадахгүй.
	e3 := newPrivEnv(t, nil, func(c *Config) {
		c.DefaultVisibility = VisibilityPrivate
		c.AllowedVisibilities = []Visibility{VisibilityPrivate}
	})
	resp, _ := do(t, e3.app, privUploadReq(t, "/api/upload", map[string]string{"visibility": "public"}, "a.pdf", "application/pdf", pdfData, "u1", orgA))
	if resp.StatusCode != 400 {
		t.Fatalf("public хүссэн: status=%d", resp.StatusCode)
	}
	// PrivateHandler клиентийн public-ийг үл тооно.
	r = do2(t, e, privUploadReq(t, "/api/upload/private", map[string]string{"visibility": "public"}, "a.pdf", "application/pdf", pdfData, "u1", orgA))
	if r.Visibility != VisibilityPrivate {
		t.Fatalf("%+v", r)
	}
	// Зөвхөн public зөвшөөрсөн ⇒ private хавтас огт үүсэхгүй, private хүсэлт 400.
	e4 := newPrivEnv(t, nil, func(c *Config) { c.AllowedVisibilities = []Visibility{VisibilityPublic} })
	resp, _ = do(t, e4.app, privUploadReq(t, "/api/upload", map[string]string{"visibility": "private"}, "a.pdf", "application/pdf", pdfData, "u1", orgA))
	if resp.StatusCode != 400 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(e4.u.cfg.Root, "private")); err == nil {
		t.Fatal("private хавтас үүссэн")
	}
}

// ---- Signature ----

func splitURL(t testing.TB, raw string) (string, url.Values) {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u.EscapedPath(), u.Query()
}

func TestSignatureTamper(t *testing.T) {
	e := newPrivEnv(t, nil)
	opaque := putPrivate(t, e, orgA, "documents", "a-0123456789abcdef.pdf", pdfData)
	other := putPrivate(t, e, orgA, "documents", "b-0123456789abcdef.pdf", []byte("%PDF-other"))
	otherOrg := putPrivate(t, e, orgB, "documents", "a-0123456789abcdef.pdf", []byte("%PDF-orgB"))
	good := e.sign(t, opaque, time.Minute, SignOptions{})
	if resp, _ := e.get(t, good); resp.StatusCode != 200 {
		t.Fatalf("эх холбоос status=%d", resp.StatusCode)
	}
	p, q := splitURL(t, good)
	build := func(path string, mut func(url.Values)) string {
		v := url.Values{}
		for k, vs := range q {
			v[k] = append([]string(nil), vs...)
		}
		if mut != nil {
			mut(v)
		}
		return path + "?" + v.Encode()
	}
	flip := func(s string) string {
		b := []byte(s)
		if b[0] == 'A' {
			b[0] = 'B'
		} else {
			b[0] = 'A'
		}
		return string(b)
	}
	cases := map[string]string{
		"path өөр файл":   build(e.signer.Prefix()+"/"+other, nil),
		"path өөр org":    build(e.signer.Prefix()+"/"+otherOrg, nil),
		"org сегмент":     build(strings.Replace(p, orgA, orgB, 1), nil),
		"exp +1":          build(p, func(v url.Values) { v.Set("exp", incr(v.Get("exp"))) }),
		"exp хоосон":      build(p, func(v url.Values) { v.Del("exp") }),
		"exp +":           build(p, func(v url.Values) { v.Set("exp", "+"+v.Get("exp")) }),
		"exp тэмдэггүй 0": build(p, func(v url.Values) { v.Set("exp", "0"+v.Get("exp")) }),
		"kid өөр":         build(p, func(v url.Values) { v.Set("kid", "k2") }),
		"kid хоосон":      build(p, func(v url.Values) { v.Del("kid") }),
		"sig bit":         build(p, func(v url.Values) { v.Set("sig", flip(v.Get("sig"))) }),
		"sig хоосон":      build(p, func(v url.Values) { v.Del("sig") }),
		"sig богино":      build(p, func(v url.Values) { v.Set("sig", v.Get("sig")[:20]) }),
		"sig padding":     build(p, func(v url.Values) { v.Set("sig", v.Get("sig")+"=") }),
		"uid нэмсэн":      build(p, func(v url.Values) { v.Set("uid", "u1") }),
		"disp нэмсэн":     build(p, func(v url.Values) { v.Set("disp", "inline") }),
		"disp буруу":      build(p, func(v url.Values) { v.Set("disp", "evil") }),
		"давхар sig":      p + "?sig=AAAA&" + q.Encode(),
		"query байхгүй":   p,
	}
	for name, target := range cases {
		resp, body := e.get(t, target)
		if resp.StatusCode != 403 {
			t.Errorf("%s: status=%d body=%s", name, resp.StatusCode, body)
		}
		if strings.Contains(body, "%PDF") {
			t.Errorf("%s: агуулга алдагдсан", name)
		}
	}

	// uid-тэй холбоосоос uid-г хасах / өөрчлөх.
	bound := e.sign(t, opaque, time.Minute, SignOptions{UserID: "u1"})
	bp, bq := splitURL(t, bound)
	for name, mut := range map[string]func(url.Values){
		"uid хассан":    func(v url.Values) { v.Del("uid") },
		"uid өөрчилсөн": func(v url.Values) { v.Set("uid", "u2") },
	} {
		for k := range q {
			_ = k
		}
		v := url.Values{}
		for k, vs := range bq {
			v[k] = vs
		}
		mut(v)
		resp, _ := e.req(t, "GET", bp+"?"+v.Encode(), "u1", orgA, nil)
		if resp.StatusCode != 403 {
			t.Errorf("%s: status=%d", name, resp.StatusCode)
		}
	}
}

func incr(s string) string {
	b := []byte(s)
	b[len(b)-1]++
	return string(b)
}

func TestSignatureNoExistenceOracle(t *testing.T) {
	e := newPrivEnv(t, nil)
	real := putPrivate(t, e, orgA, "documents", "a-0123456789abcdef.pdf", pdfData)
	ghost := "private/" + orgA + "/documents/2026/11/ghost-0123456789abcdef.pdf"
	_, q := splitURL(t, e.sign(t, real, time.Minute, SignOptions{}))
	enc := "?" + q.Encode()
	r1, b1 := e.get(t, e.signer.Prefix()+"/"+real+enc)
	_ = r1
	// Гарын үсэггүй/буруу: байгаа ба байхгүй файл ИЖИЛ хариу.
	bad1, bb1 := e.get(t, e.signer.Prefix()+"/"+real)
	bad2, bb2 := e.get(t, e.signer.Prefix()+"/"+ghost)
	if bad1.StatusCode != 403 || bad2.StatusCode != 403 || bb1 != bb2 {
		t.Fatalf("оракул: %d %q / %d %q", bad1.StatusCode, bb1, bad2.StatusCode, bb2)
	}
	if bad1.Header.Get("Content-Type") != bad2.Header.Get("Content-Type") || bad1.Header.Get("Cache-Control") != "private, no-store" {
		t.Fatal("header ялгаатай")
	}
	// Зөв гарын үсэг + байхгүй файл → 404 (зөвхөн дараа нь).
	gurl := e.sign(t, ghost, time.Minute, SignOptions{})
	if resp, _ := e.get(t, gurl); resp.StatusCode != 404 {
		t.Fatalf("ghost status=%d", resp.StatusCode)
	}
	_ = b1
}

func TestSignatureExpiryAndSkew(t *testing.T) {
	e := newPrivEnv(t, nil)
	opaque := putPrivate(t, e, orgA, "documents", "a-0123456789abcdef.pdf", pdfData)
	r, err := e.signer.Sign(opaque, time.Minute, SignOptions{})
	if err != nil {
		t.Fatal(err)
	}
	exp := r.ExpiresAt
	for _, tc := range []struct {
		at   time.Time
		want int
	}{
		{testNow, 200},
		{exp.Add(-time.Second), 200},
		{exp, 200},
		{exp.Add(DefaultSignSkew), 200}, // skew дотор
		{exp.Add(DefaultSignSkew + 2*time.Second), 403}, // skew-ээс хойш
		{exp.Add(time.Hour), 403},
	} {
		e.clk.mu.Lock()
		e.clk.t = tc.at
		e.clk.mu.Unlock()
		if resp, _ := e.get(t, r.URL); resp.StatusCode != tc.want {
			t.Errorf("at=%s status=%d want %d", tc.at.Sub(testNow), resp.StatusCode, tc.want)
		}
	}
	// Verify нь ErrExpired / ErrBadSignature-ийг ялгана.
	p, q := splitURL(t, r.URL)
	sp := SignedParams{Exp: q.Get("exp"), KID: q.Get("kid"), Sig: q.Get("sig")}
	if _, err := e.signer.Verify(strings.TrimPrefix(p, e.signer.Prefix()+"/"), sp); err != ErrExpired {
		t.Fatalf("err=%v", err)
	}
	sp.Sig = flip64(sp.Sig)
	if _, err := e.signer.Verify(strings.TrimPrefix(p, e.signer.Prefix()+"/"), sp); err != ErrBadSignature {
		t.Fatalf("err=%v", err)
	}
}

func flip64(s string) string {
	if s[0] == 'A' {
		return "B" + s[1:]
	}
	return "A" + s[1:]
}

func TestSignatureTTLCapAndFarFuture(t *testing.T) {
	clk := &testClock{t: testNow}
	long := mustSigner(t, clk, SignKey{"k1", []byte(secretA)}, nil, WithSignMaxTTL(48*time.Hour))
	short := mustSigner(t, clk, SignKey{"k1", []byte(secretA)}, nil, WithSignMaxTTL(time.Hour))
	opaque := "private/" + orgA + "/documents/2026/11/a-0123456789abcdef.pdf"
	if _, err := short.SignURL(opaque, 2*time.Hour, SignOptions{}); err != ErrTTLTooLong {
		t.Fatalf("err=%v", err)
	}
	for _, d := range []time.Duration{0, -time.Second} {
		if _, err := short.SignURL(opaque, d, SignOptions{}); err == nil {
			t.Fatalf("ttl=%v зөвшөөрөгдсөн", d)
		}
	}
	// Урт настай (өөр тохиргоотой) эсвэл хулгайлагдсан түлхүүрээр гарсан холбоос
	// MaxTTL-ээс урт бол зөв гарын үсэгтэй ч татгалзагдана.
	u, _ := long.SignURL(opaque, 24*time.Hour, SignOptions{})
	p, q := splitURL(t, u)
	_, err := short.Verify(strings.TrimPrefix(p, "/files/private/"), SignedParams{Exp: q.Get("exp"), KID: "k1", Sig: q.Get("sig")})
	if err != ErrBadSignature {
		t.Fatalf("far-future err=%v", err)
	}
	if _, err := long.Verify(strings.TrimPrefix(p, "/files/private/"), SignedParams{Exp: q.Get("exp"), KID: "k1", Sig: q.Get("sig")}); err != nil {
		t.Fatal(err)
	}
}

func TestSignerKeyRotation(t *testing.T) {
	clk := &testClock{t: testNow}
	opaque := "private/" + orgA + "/documents/2026/11/a-0123456789abcdef.pdf"
	k1 := SignKey{"k1", []byte(secretA)}
	k2 := SignKey{"k2", []byte(secretB)}
	old := mustSigner(t, clk, k1, nil)
	oldURL, _ := old.SignURL(opaque, time.Minute, SignOptions{})

	verify := func(s *Signer, raw string) error {
		p, q := splitURL(t, raw)
		_, err := s.Verify(strings.TrimPrefix(p, "/files/private/"), SignedParams{Exp: q.Get("exp"), KID: q.Get("kid"), Sig: q.Get("sig")})
		return err
	}
	rotated := mustSigner(t, clk, k2, []SignKey{k1})
	if err := verify(rotated, oldURL); err != nil {
		t.Fatalf("эргүүлсний дараа хуучин kid хүчинтэй байх ёстой: %v", err)
	}
	newURL, _ := rotated.SignURL(opaque, time.Minute, SignOptions{})
	if !strings.Contains(newURL, "kid=k2") {
		t.Fatalf("шинэ холбоос идэвхтэй kid-ээр: %s", newURL)
	}
	if err := verify(rotated, newURL); err != nil {
		t.Fatal(err)
	}
	// Өмнөх түлхүүрийг хассан ⇒ хуучин холбоос татгалзагдана.
	dropped := mustSigner(t, clk, k2, nil)
	if err := verify(dropped, oldURL); err != ErrBadSignature {
		t.Fatalf("err=%v", err)
	}
	// Хуучин signer шинэ kid-г мэдэхгүй.
	if err := verify(old, newURL); err != ErrBadSignature {
		t.Fatalf("err=%v", err)
	}
	// Ижил kid, өөр secret ⇒ татгалзана.
	forged := mustSigner(t, clk, SignKey{"k1", []byte(secretB)}, nil)
	if err := verify(forged, oldURL); err != ErrBadSignature {
		t.Fatalf("err=%v", err)
	}
	// kid-г k2 болгож сольсон ⇒ татгалзана (kid MAC-д орсон).
	swapped := strings.Replace(oldURL, "kid=k1", "kid=k2", 1)
	if err := verify(rotated, swapped); err != ErrBadSignature {
		t.Fatalf("err=%v", err)
	}
}

func TestNewSignerValidation(t *testing.T) {
	good := SignKey{"k1", []byte(secretA)}
	for name, tc := range map[string]struct {
		active SignKey
		prev   []SignKey
		opts   []SignerOption
	}{
		"богино secret": {SignKey{"k1", []byte("short")}, nil, nil},
		"31 байт":       {SignKey{"k1", bytes.Repeat([]byte("a"), 31)}, nil, nil},
		"хоосон id":     {SignKey{"", []byte(secretA)}, nil, nil},
		"буруу id":      {SignKey{"k 1", []byte(secretA)}, nil, nil},
		"давхцсан id":   {good, []SignKey{{"k1", []byte(secretB)}}, nil},
		"prev богино":   {good, []SignKey{{"k0", []byte("x")}}, nil},
		"prefix buруу":  {good, nil, []SignerOption{WithSignPrefix("/a/../b")}},
		"ttl 0":         {good, nil, []SignerOption{WithSignMaxTTL(0)}},
		"skew их":       {good, nil, []SignerOption{WithSignSkew(time.Hour)}},
	} {
		s, err := NewSigner(tc.active, tc.prev, tc.opts...)
		if err == nil || s != nil {
			t.Errorf("%s: алдаа хүлээсэн", name)
		}
		if err != nil && (strings.Contains(err.Error(), secretA) || strings.Contains(err.Error(), secretB)) {
			t.Errorf("%s: алдаанд secret орсон", name)
		}
	}
	if _, err := NewSigner(SignKey{"k.1_x-2", bytes.Repeat([]byte("a"), 32)}, nil); err != nil {
		t.Fatal(err)
	}
	// Secret-г дуудагч хойно өөрчилбөл Signer-т нөлөөлөхгүй.
	sec := []byte(secretA)
	s, _ := NewSigner(SignKey{"k1", sec}, nil)
	opaque := "private/" + orgA + "/documents/2026/11/a-0123456789abcdef.pdf"
	u1, _ := s.SignURL(opaque, time.Minute, SignOptions{})
	sec[0] ^= 0xff
	p, q := splitURL(t, u1)
	if _, err := s.Verify(strings.TrimPrefix(p, "/files/private/"), SignedParams{Exp: q.Get("exp"), KID: "k1", Sig: q.Get("sig")}); err != nil {
		t.Fatalf("secret хуулагдаагүй: %v", err)
	}
}

func TestSignRejectsBadPathsAndOptions(t *testing.T) {
	clk := &testClock{t: testNow}
	s := mustSigner(t, clk, SignKey{"k1", []byte(secretA)}, nil)
	for _, p := range []string{
		"", "/private/" + orgA + "/documents/2026/11/a.pdf",
		"uploaded/" + orgA + "/documents/2026/11/a.pdf",
		"private/" + orgA + "/documents/2026/11/../a.pdf",
		"private/../documents/2026/11/a.pdf",
		"private/" + orgA + "/documents/2026/11",
		"private/" + orgA + "/documents/2026/11/x/a.pdf",
		"private/" + orgA + "/Documents/2026/11/a.pdf",
		"private/" + orgA + "/documents/2026/11/.a.pdf",
		"private/" + orgA + "/documents/2026/11/a..pdf",
		"private/" + orgA + "/documents/2026/11/A.pdf",
		"private/" + orgA + "/documents/2026/11/a%2e.pdf",
		"private/" + orgA + "/documents/2026/11/a.pdf\x00",
		"private/" + orgA + "/documents/20266/11/a.pdf",
		"private/-bad/documents/2026/11/a.pdf",
		"private/" + strings.Repeat("a", 65) + "/documents/2026/11/a.pdf",
		"private\\" + orgA + "\\documents\\2026\\11\\a.pdf",
	} {
		if _, err := s.SignURL(p, time.Minute, SignOptions{}); err == nil {
			t.Errorf("зам зөвшөөрөгдсөн: %q", p)
		}
	}
	ok := "private/" + orgA + "/documents/2026/11/a.pdf"
	if _, err := s.SignURL(ok, time.Minute, SignOptions{Disposition: "evil"}); err == nil {
		t.Error("disposition")
	}
	if _, err := s.SignURL(ok, time.Minute, SignOptions{UserID: "a\nb"}); err == nil {
		t.Error("user id control char")
	}
	if _, err := s.SignURL(ok, time.Minute, SignOptions{UserID: strings.Repeat("u", 129)}); err == nil {
		t.Error("user id урт")
	}
}

// ---- Isolation ----

func TestPublicServeNeverReachesPrivate(t *testing.T) {
	e := newPrivEnv(t, nil)
	r := e.upload(t, orgA, pdfData)
	opaque := r.Path
	rel := strings.TrimPrefix(opaque, "private/")
	// Нийтийн хавтсанд ч мөн адил нэртэй түүхий файл тавьж (decoy), private-ийг ялгана.
	secret := "TOP-SECRET-PRIVATE"
	putPrivate(t, e, orgA, "documents", "s-0123456789abcdef.txt", []byte(secret))
	paths := []string{
		"/uploaded/private/" + rel,
		"/uploaded/../private/" + rel,
		"/uploaded/%2e%2e/private/" + rel,
		"/uploaded/%2E%2E%2Fprivate%2F" + rel,
		"/uploaded/documents/../../private/" + rel,
		"/uploaded/documents/%2e%2e/%2e%2e/private/" + rel,
		"/uploaded/documents/%252e%252e/%252e%252e/private/" + rel,
		"/uploaded/" + rel,
		"/uploaded/documents/2026/11/" + r.Name,
		"/uploaded/" + orgA + "/documents/2026/11/" + r.Name,
		"/uploaded/documents/2026/11/s-0123456789abcdef.txt",
		"/uploaded/documents//..//..//private/" + rel,
		"/uploaded/private",
		"/uploaded/private/",
		"/uploaded/",
		"/private/" + rel,
		"/uploaded/documents/..%5c..%5cprivate%5c" + rel,
		"/uploaded/documents/2026/11/..%00/" + r.Name,
	}
	for _, p := range paths {
		for _, m := range []string{"GET", "HEAD"} {
			req, err := http.NewRequest(m, "http://example.test"+p, nil)
			if err != nil {
				continue
			}
			resp, body := do(t, e.app, req)
			if resp.StatusCode == 200 || strings.Contains(body, secret) || strings.Contains(body, "%PDF") {
				t.Errorf("%s %s: status=%d — private файл public замаар гарлаа", m, p, resp.StatusCode)
			}
		}
	}
	// Хүлээн зөвшөөрөгдсөн public route-ийн хувьд private хавтас Root/uploaded-ийн гадна.
	if strings.HasPrefix(filepath.Join(e.u.cfg.Root, "private"), filepath.Join(e.u.cfg.Root, "uploaded")) {
		t.Fatal("private хавтас uploaded дотор")
	}
}

func TestPrivateServeTraversalAndMalformed(t *testing.T) {
	e := newPrivEnv(t, nil)
	opaque := putPrivate(t, e, orgA, "documents", "a-0123456789abcdef.pdf", pdfData)
	putPrivate(t, e, orgB, "documents", "a-0123456789abcdef.pdf", []byte("%PDF-B"))
	_, q := splitURL(t, e.sign(t, opaque, time.Minute, SignOptions{}))
	qs := "?" + q.Encode()
	pre := e.signer.Prefix() + "/"
	// Анхаар: fasthttp `/../` ба хвостын `/`-ийг роутерын өмнө цэвэрлэдэг тул ижил
	// файлд хүргэх хэлбэрүүд (гарын үсэг ЗӨВ замтай ижил) зөвхөн тэр файлыг өгнө.
	for _, p := range []string{
		pre + "private/" + orgA + "/documents/2026/11",
		pre + "private/" + orgA + "/documents/2026",
		pre + "private/" + orgA,
		pre + "private/",
		pre,
		e.signer.Prefix(),
		pre + "private/" + orgB + "/documents/2026/11/..%2f..%2f" + orgA + "/documents/2026/11/a-0123456789abcdef.pdf",
		pre + "private/" + orgA + "/documents/2026/11/a-0123456789abcdef.pdf%00",
		pre + opaque + "/..",
		pre + "../" + opaque,
		pre + "%2e%2e/" + opaque,
	} {
		for _, suffix := range []string{"", qs} {
			req, err := http.NewRequest("GET", "http://example.test"+p+suffix, nil)
			if err != nil {
				continue
			}
			resp, body := do(t, e.app, req)
			// 301/403/404 — ямар ч тохиолдолд агуулга өгөхгүй.
			if resp.StatusCode == 200 || strings.Contains(body, "%PDF") {
				t.Errorf("%s: status=%d", p+suffix, resp.StatusCode)
			}
		}
	}
	// Өөр method.
	resp, _ := e.req(t, "POST", e.signer.Prefix()+"/"+opaque+qs, "", "", nil)
	if resp.StatusCode != 405 || resp.Header.Get("Allow") != "GET, HEAD" {
		t.Fatalf("POST status=%d", resp.StatusCode)
	}
	// Хавтсанд илгээсэн зөв гарын үсэг хавтас жагсаахгүй (бүтэц буруу ⇒ 403).
	d, dq := splitURL(t, e.sign(t, opaque, time.Minute, SignOptions{}))
	dirURL := path403(d) + "?" + dq.Encode()
	if resp, _ := e.get(t, dirURL); resp.StatusCode != 403 {
		t.Fatalf("dir status=%d", resp.StatusCode)
	}
}

func path403(p string) string { return p[:strings.LastIndexByte(p, '/')] }

func TestSymlinkInPrivateRootDoesNotEscape(t *testing.T) {
	e := newPrivEnv(t, nil)
	outside := filepath.Join(t.TempDir(), "outside.pdf")
	if err := os.WriteFile(outside, []byte("%PDF-OUT"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(e.u.cfg.Root, "private", orgA, "documents", "2026", "11")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "link-0123456789abcdef.pdf")); err != nil {
		t.Skip("symlink дэмжигдэхгүй")
	}
	u := e.sign(t, "private/"+orgA+"/documents/2026/11/link-0123456789abcdef.pdf", time.Minute, SignOptions{})
	if resp, body := e.get(t, u); resp.StatusCode != 404 || strings.Contains(body, "OUT") {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

// ---- Headers / Range ----

func TestPrivateServeHeaders(t *testing.T) {
	e := newPrivEnv(t, nil)
	files := map[string]string{
		"svg":  putPrivate(t, e, orgA, "images", "a-0123456789abcdef.svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`)),
		"html": putPrivate(t, e, orgA, "documents", "p-0123456789abcdef.html", []byte("<script>alert(1)</script>")),
		"json": putPrivate(t, e, orgA, "documents", "d-0123456789abcdef.json", []byte("{}")),
		"pdf":  putPrivate(t, e, orgA, "documents", "r-0123456789abcdef.pdf", pdfData),
		"bin":  putPrivate(t, e, orgA, "documents", "w-0123456789abcdef.bin", []byte("x")),
		"png":  putPrivate(t, e, orgA, "images", "i-0123456789abcdef.png", pngBytes(t, 4, 4)),
	}
	const csp = "sandbox; default-src 'none'; img-src 'self' data:; style-src 'unsafe-inline'"
	tests := []struct {
		key, ct, csp, disp string
	}{
		{"svg", "image/svg+xml", csp, ""},
		{"html", "text/html", csp, "attachment"},
		{"json", "application/json", "", "attachment"},
		{"pdf", "application/pdf", "", ""},
		{"bin", "application/octet-stream", "", "attachment"},
		{"png", "image/png", "", ""},
	}
	ttl := 90 * time.Second
	for _, tc := range tests {
		u := e.sign(t, files[tc.key], ttl, SignOptions{})
		resp, _ := e.get(t, u)
		if resp.StatusCode != 200 {
			t.Errorf("%s: status=%d", tc.key, resp.StatusCode)
			continue
		}
		h := resp.Header
		if h.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: nosniff алга", tc.key)
		}
		if !strings.HasPrefix(h.Get("Content-Type"), tc.ct) {
			t.Errorf("%s: ct=%q", tc.key, h.Get("Content-Type"))
		}
		if h.Get("Content-Security-Policy") != tc.csp {
			t.Errorf("%s: csp=%q", tc.key, h.Get("Content-Security-Policy"))
		}
		if d := h.Get("Content-Disposition"); (tc.disp == "") != (d == "") || !strings.HasPrefix(d, tc.disp) {
			t.Errorf("%s: disposition=%q", tc.key, d)
		}
		if h.Get("Referrer-Policy") != "no-referrer" || !strings.Contains(h.Get("X-Robots-Tag"), "noindex") {
			t.Errorf("%s: referrer/robots", tc.key)
		}
		cc := h.Get("Cache-Control")
		if !strings.HasPrefix(cc, "private, max-age=") || strings.Contains(cc, "public") || strings.Contains(cc, "immutable") {
			t.Errorf("%s: cache=%q", tc.key, cc)
		}
	}
	// max-age ≤ үлдсэн ttl, цаг өнгөрөхөд буурна.
	maxAge := func(u string) int {
		resp, _ := e.get(t, u)
		var n int
		_, _ = fmtSscan(resp.Header.Get("Cache-Control"), &n)
		return n
	}
	u := e.sign(t, files["pdf"], ttl, SignOptions{})
	a := maxAge(u)
	if a <= 0 || a > int(ttl/time.Second)+1 {
		t.Fatalf("max-age=%d", a)
	}
	e.clk.Add(60 * time.Second)
	if b := maxAge(u); b >= a || b > 31 {
		t.Fatalf("max-age буураагүй: %d → %d", a, b)
	}
	// Skew дотор (exp өнгөрсөн) max-age=0.
	e.clk.Add(30 * time.Second)
	if c := maxAge(u); c != 0 {
		t.Fatalf("skew-д max-age=%d", c)
	}
}

func fmtSscan(cc string, n *int) (int, error) {
	i := strings.Index(cc, "max-age=")
	v := 0
	for _, r := range cc[i+len("max-age="):] {
		if r < '0' || r > '9' {
			break
		}
		v = v*10 + int(r-'0')
	}
	*n = v
	return 1, nil
}

func TestPrivateDispositionOverride(t *testing.T) {
	e := newPrivEnv(t, nil)
	pdf := putPrivate(t, e, orgA, "documents", "r-0123456789abcdef.pdf", pdfData)
	html := putPrivate(t, e, orgA, "documents", "p-0123456789abcdef.html", []byte("<b>x</b>"))
	disp := func(opaque, d string) string {
		resp, _ := e.get(t, e.sign(t, opaque, time.Minute, SignOptions{Disposition: d}))
		if resp.StatusCode != 200 {
			t.Fatalf("status=%d", resp.StatusCode)
		}
		return resp.Header.Get("Content-Disposition")
	}
	if d := disp(pdf, "attachment"); !strings.HasPrefix(d, "attachment") {
		t.Errorf("pdf attachment: %q", d)
	}
	if d := disp(pdf, "inline"); d != "" {
		t.Errorf("pdf inline: %q", d)
	}
	// inline хүссэн ч html хэзээ ч inline болохгүй.
	if d := disp(html, "inline"); !strings.HasPrefix(d, "attachment") {
		t.Errorf("html inline: %q", d)
	}
}

func TestPrivateRangeHeadConditional(t *testing.T) {
	e := newPrivEnv(t, nil)
	data := []byte("%PDF-0123456789ABCDEFGHIJ")
	opaque := putPrivate(t, e, orgA, "documents", "r-0123456789abcdef.pdf", data)
	u := e.sign(t, opaque, time.Minute, SignOptions{})
	resp, body := e.req(t, "GET", u, "", "", nil, "Range", "bytes=5-9")
	if resp.StatusCode != 206 || body != "01234" || resp.Header.Get("Content-Range") != "bytes 5-9/25" {
		t.Fatalf("status=%d body=%q cr=%q", resp.StatusCode, body, resp.Header.Get("Content-Range"))
	}
	if resp.Header.Get("Cache-Control") == "" || resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("range хариунд header алга")
	}
	resp, body = e.req(t, "GET", u, "", "", nil, "Range", "bytes=-4")
	if resp.StatusCode != 206 || body != "GHIJ" {
		t.Fatalf("suffix status=%d %q", resp.StatusCode, body)
	}
	resp, _ = e.req(t, "GET", u, "", "", nil, "Range", "bytes=100-")
	if resp.StatusCode != 416 {
		t.Fatalf("416 status=%d", resp.StatusCode)
	}
	// Range нь гарын үсгийг тойруулахгүй.
	bad := strings.Replace(u, "sig=", "sig=A", 1)
	if resp, _ = e.req(t, "GET", bad, "", "", nil, "Range", "bytes=0-3"); resp.StatusCode != 403 {
		t.Fatalf("bad sig + range status=%d", resp.StatusCode)
	}
	resp, body = e.req(t, "HEAD", u, "", "", nil)
	if resp.StatusCode != 200 || body != "" || resp.Header.Get("Content-Length") != "25" {
		t.Fatalf("HEAD status=%d len=%q", resp.StatusCode, resp.Header.Get("Content-Length"))
	}
	resp, _ = e.get(t, u)
	etag := resp.Header.Get("ETag")
	if resp, _ = e.req(t, "GET", u, "", "", nil, "If-None-Match", etag); resp.StatusCode != 304 {
		t.Fatalf("304 status=%d", resp.StatusCode)
	}
	// Гарын үсэггүй If-None-Match → 304 ХЭЗЭЭ Ч биш (403).
	p, _ := splitURL(t, u)
	if resp, _ = e.req(t, "GET", p, "", "", nil, "If-None-Match", etag); resp.StatusCode != 403 {
		t.Fatalf("unsigned conditional status=%d", resp.StatusCode)
	}
}

// ---- User binding ----

func TestUserBoundURL(t *testing.T) {
	e := newPrivEnv(t, nil)
	opaque := putPrivate(t, e, orgA, "documents", "a-0123456789abcdef.pdf", pdfData)
	u := e.sign(t, opaque, time.Minute, SignOptions{UserID: "u1"})
	if resp, _ := e.get(t, u); resp.StatusCode != 401 {
		t.Fatalf("principal-гүй status=%d", resp.StatusCode)
	}
	if resp, _ := e.req(t, "GET", u, "u2", orgA, nil); resp.StatusCode != 403 {
		t.Fatalf("өөр хэрэглэгч status=%d", resp.StatusCode)
	}
	if resp, _ := e.req(t, "GET", u, "u2", orgB, nil); resp.StatusCode != 403 {
		t.Fatalf("өөр хэрэглэгч+org status=%d", resp.StatusCode)
	}
	if resp, body := e.req(t, "GET", u, "u1", orgA, nil); resp.StatusCode != 200 || body != string(pdfData) {
		t.Fatalf("зөв хэрэглэгч status=%d", resp.StatusCode)
	}
	// uid-гүй холбоос principal шаардахгүй (capability).
	if resp, _ := e.get(t, e.sign(t, opaque, time.Minute, SignOptions{})); resp.StatusCode != 200 {
		t.Fatal("unbound холбоос")
	}
}

// ---- Cross-org / Auth path / SignHandler ----

func TestCrossOrgIsolation(t *testing.T) {
	e := newPrivEnv(t, nil)
	rA := e.upload(t, orgA, []byte("%PDF-A-secret"))
	rB := e.upload(t, orgB, []byte("%PDF-B-secret"))

	// A-ийн холбоосыг B-ийн замаар солих (sig нь A-ийн path-д): татгалзана.
	pA, qA := splitURL(t, rA.URL)
	forged := strings.Replace(pA, orgA, orgB, 1) + "?" + qA.Encode()
	if resp, body := e.get(t, forged); resp.StatusCode != 403 || strings.Contains(body, "secret") {
		t.Fatalf("forged status=%d", resp.StatusCode)
	}
	// Org B-ийн үнэн холбоос зөвхөн B-ийн файлыг өгнө.
	if resp, body := e.get(t, rB.URL); resp.StatusCode != 200 || body != "%PDF-B-secret" {
		t.Fatalf("B status=%d", resp.StatusCode)
	}
	// Signer бусад org-ийн замд A-гийн сегментээр зурж болохгүй: SignHandler.
	resp, _ := e.req(t, "GET", "/api/sign?path="+url.QueryEscape(rA.Path), "uB", orgB, nil)
	if resp.StatusCode != 404 {
		t.Fatalf("B → A sign status=%d", resp.StatusCode)
	}
	// Auth path: өөр org 404 (байхгүй файлтай ИЖИЛ), өөрийнх 200.
	authURL := "/api/files/" + rA.Path
	respB, bodyB := e.req(t, "GET", authURL, "uB", orgB, nil)
	ghost := "/api/files/private/" + orgA + "/documents/2026/11/ghost-0123456789abcdef.pdf"
	respG, bodyG := e.req(t, "GET", ghost, "uB", orgB, nil)
	if respB.StatusCode != 404 || respG.StatusCode != 404 || bodyB != bodyG || strings.Contains(bodyB, "secret") {
		t.Fatalf("cross-org auth: %d %q / %d %q", respB.StatusCode, bodyB, respG.StatusCode, bodyG)
	}
	if resp, body := e.req(t, "GET", authURL, "uA", orgA, nil); resp.StatusCode != 200 || body != "%PDF-A-secret" {
		t.Fatalf("A auth status=%d", resp.StatusCode)
	}
	// X-Org-ID header / org_id query-д итгэхгүй.
	resp, _ = e.req(t, "GET", authURL+"?org_id="+orgA, "uB", orgB, nil, "X-Org-ID", orgA)
	if resp.StatusCode != 404 {
		t.Fatalf("header spoof status=%d", resp.StatusCode)
	}
}

func TestServePrivateAuth(t *testing.T) {
	var calls []string
	authz := func(c fiber.Ctx, org, p string) bool {
		calls = append(calls, org+"|"+p)
		return c.Get("X-Allow") == "yes"
	}
	e := newPrivEnv(t, authz)
	r := e.upload(t, orgA, pdfData)
	target := "/api/files/" + r.Path
	if resp, _ := e.req(t, "GET", target, "", "", nil); resp.StatusCode != 401 {
		t.Fatalf("anon status=%d", resp.StatusCode)
	}
	if resp, _ := e.req(t, "GET", target, "u1", "", nil); resp.StatusCode != 403 {
		t.Fatalf("org-гүй status=%d", resp.StatusCode)
	}
	if resp, body := e.req(t, "GET", target, "u1", orgA, nil); resp.StatusCode != 403 || strings.Contains(body, "%PDF") {
		t.Fatalf("authz deny status=%d", resp.StatusCode)
	}
	resp, body := e.req(t, "GET", target, "u1", orgA, nil, "X-Allow", "yes")
	if resp.StatusCode != 200 || body != string(pdfData) {
		t.Fatalf("authz allow status=%d", resp.StatusCode)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "private, max-age=0, must-revalidate" {
		t.Fatalf("cache=%q", cc)
	}
	if resp.Header.Get("X-Content-Type-Options") != "nosniff" || resp.Header.Get("Content-Security-Policy") != "" {
		t.Fatal("header")
	}
	last := calls[len(calls)-1]
	if last != orgA+"|"+r.Path {
		t.Fatalf("authz args=%q", last)
	}
	// Дарааллаар: өөр org-д authz ДУУДАГДАХГҮЙ (org шалгалт өмнө).
	n := len(calls)
	e.req(t, "GET", target, "u2", orgB, nil, "X-Allow", "yes")
	if len(calls) != n {
		t.Fatal("өөр org-д authz дуудагдсан")
	}
	// disposition query.
	resp, _ = e.req(t, "GET", target+"?disposition=attachment", "u1", orgA, nil, "X-Allow", "yes")
	if !strings.HasPrefix(resp.Header.Get("Content-Disposition"), "attachment") {
		t.Fatal("disposition=attachment")
	}
	// Буруу бүтэц → 404.
	for _, p := range []string{"/api/files/private/" + orgA + "/documents/2026/11", "/api/files/private/" + orgA + "/../x", "/api/files/uploaded/x"} {
		if resp, _ := e.req(t, "GET", p, "u1", orgA, nil, "X-Allow", "yes"); resp.StatusCode == 200 {
			t.Fatalf("%s status=200", p)
		}
	}
	// nil authz: org гишүүн хангалттай.
	e2 := newPrivEnv(t, nil)
	r2 := e2.upload(t, orgA, pdfData)
	if resp, _ := e2.req(t, "GET", "/api/files/"+r2.Path, "u9", orgA, nil); resp.StatusCode != 200 {
		t.Fatalf("nil authz status=%d", resp.StatusCode)
	}
}

func TestSignHandler(t *testing.T) {
	e := newPrivEnv(t, func(c fiber.Ctx, org, p string) bool { return !strings.Contains(p, "denied-") })
	r := e.upload(t, orgA, pdfData)
	denied := putPrivate(t, e, orgA, "documents", "denied-0123456789abcdef.pdf", pdfData)

	type out struct {
		Path, URL string
		Expires   int64 `json:"expires_at"`
	}
	signGet := func(q string, user, org string) (*http.Response, out) {
		resp, body := e.req(t, "GET", "/api/sign?"+q, user, org, nil)
		var o out
		_ = json.Unmarshal([]byte(body), &o)
		return resp, o
	}
	resp, o := signGet("path="+url.QueryEscape(r.Path), "u1", orgA)
	if resp.StatusCode != 200 || o.Path != r.Path || o.URL == "" || resp.Header.Get("Cache-Control") != "private, no-store" {
		t.Fatalf("status=%d %+v", resp.StatusCode, o)
	}
	if resp, body := e.get(t, o.URL); resp.StatusCode != 200 || body != string(pdfData) {
		t.Fatalf("fetch status=%d", resp.StatusCode)
	}
	if o.Expires != testNow.Add(5*time.Minute).Unix() {
		t.Fatalf("default ttl expires=%d", o.Expires)
	}
	// ttl clamp (MaxTTL 1ц).
	_, o = signGet("path="+url.QueryEscape(r.Path)+"&ttl_seconds=999999", "u1", orgA)
	if o.Expires > testNow.Add(time.Hour).Unix() {
		t.Fatalf("ttl clamp: %d", o.Expires)
	}
	_, o = signGet("path="+url.QueryEscape(r.Path)+"&ttl_seconds=30", "u1", orgA)
	if o.Expires > testNow.Add(30*time.Second).Unix() {
		t.Fatalf("ttl=30: %d", o.Expires)
	}
	// bind_user ⇒ uid-тэй URL.
	_, o = signGet("path="+url.QueryEscape(r.Path)+"&bind_user=true", "u1", orgA)
	if !strings.Contains(o.URL, "uid=u1") {
		t.Fatalf("url=%q", o.URL)
	}
	if resp, _ := e.req(t, "GET", o.URL, "u2", orgA, nil); resp.StatusCode != 403 {
		t.Fatalf("bound өөр хэрэглэгч status=%d", resp.StatusCode)
	}
	// POST JSON.
	body, _ := json.Marshal(map[string]any{"path": r.Path, "disposition": "attachment", "bind_user": false})
	resp, rb := e.req(t, "POST", "/api/sign", "u1", orgA, bytes.NewReader(body), "Content-Type", "application/json")
	if resp.StatusCode != 200 || !strings.Contains(rb, "disp=attachment") {
		t.Fatalf("POST status=%d %s", resp.StatusCode, rb)
	}
	// Алдаанууд.
	cases := []struct {
		name, q, user, org string
		want               int
	}{
		{"нэвтрээгүй", "path=" + url.QueryEscape(r.Path), "", "", 401},
		{"org-гүй", "path=" + url.QueryEscape(r.Path), "u1", "", 403},
		{"өөр org", "path=" + url.QueryEscape(r.Path), "u1", orgB, 404},
		{"байхгүй файл", "path=" + url.QueryEscape("private/"+orgA+"/documents/2026/11/ghost-0123456789abcdef.pdf"), "u1", orgA, 404},
		{"traversal", "path=" + url.QueryEscape("private/"+orgA+"/documents/2026/11/../../../x/a.pdf"), "u1", orgA, 404},
		{"public зам", "path=" + url.QueryEscape("/uploaded/images/2026/11/a.png"), "u1", orgA, 404},
		{"disposition", "path=" + url.QueryEscape(r.Path) + "&disposition=evil", "u1", orgA, 400},
		{"authz deny", "path=" + url.QueryEscape(denied), "u1", orgA, 403},
		{"path хоосон", "", "u1", orgA, 404},
	}
	for _, tc := range cases {
		if resp, _ := signGet(tc.q, tc.user, tc.org); resp.StatusCode != tc.want {
			t.Errorf("%s: status=%d want %d", tc.name, resp.StatusCode, tc.want)
		}
	}
	if resp, _ := e.req(t, "POST", "/api/sign", "u1", orgA, strings.NewReader("{bad"), "Content-Type", "application/json"); resp.StatusCode != 400 {
		t.Errorf("bad json status=%d", resp.StatusCode)
	}
	// Signer-гүй ⇒ 501.
	e2 := newPrivEnv(t, nil, func(c *Config) { c.Signer = nil })
	if resp, _ := e2.req(t, "GET", "/api/sign?path=x", "u1", orgA, nil); resp.StatusCode != 501 {
		t.Errorf("no signer status=%d", resp.StatusCode)
	}
	// Signer-гүй upload: URL хоосон, path байна, ServePrivate 403.
	r2 := e2.upload(t, orgA, pdfData)
	if r2.URL != "" || r2.Path == "" {
		t.Fatalf("%+v", r2)
	}
	if resp, _ := e2.get(t, "/files/private/"+r2.Path); resp.StatusCode != 403 {
		t.Fatalf("signer-гүй serve status=%d", resp.StatusCode)
	}
	if resp, _ := e2.req(t, "GET", "/api/files/"+r2.Path, "u1", orgA, nil); resp.StatusCode != 200 {
		t.Fatalf("auth path status=%d", resp.StatusCode)
	}
}

// ---- Config / compat ----

func TestLambdaCompatUnaffected(t *testing.T) {
	e := newPrivEnv(t, nil, func(c *Config) { c.LambdaCompat = true })
	// Public: text/plain, `2026/November`, уншигдахуйц нэр.
	resp, body := do(t, e.app, privUploadReq(t, "/api/upload", nil, "Photo.jpg", "image/jpeg", jpegBytes(t, 20, 20), "u1", orgA))
	if resp.StatusCode != 200 || body != "/uploaded/images/2026/November/photo.jpg" || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/plain") {
		t.Fatalf("status=%d body=%q ct=%q", resp.StatusCode, body, resp.Header.Get("Content-Type"))
	}
	if resp, _ := e.get(t, body); resp.StatusCode != 200 {
		t.Fatalf("serve status=%d", resp.StatusCode)
	}
	// Private: LambdaCompat нэрлэлт үл хамаарна — JSON, санамсаргүй нэр, `11`.
	r := e.upload(t, orgA, pdfData)
	if !strings.Contains(r.Path, "/2026/11/geree-") || strings.Contains(r.Path, "November") {
		t.Fatalf("path=%q", r.Path)
	}
}

func TestPrivateConfigValidation(t *testing.T) {
	root := t.TempDir()
	clk := &testClock{t: testNow}
	sg := mustSigner(t, clk, SignKey{"k1", []byte(secretA)}, nil)
	sgOther := mustSigner(t, clk, SignKey{"k1", []byte(secretA)}, nil, WithSignPrefix("/x/private"))
	bad := map[string]Config{
		"default private allowlist-д алга": {Root: root, DefaultVisibility: VisibilityPrivate, AllowedVisibilities: []Visibility{VisibilityPublic}},
		"буруу default":                    {Root: root, DefaultVisibility: "secret"},
		"буруу allowlist":                  {Root: root, AllowedVisibilities: []Visibility{"secret"}},
		"prefix signer-тэй зөрсөн":         {Root: root, Signer: sg, PrivatePrefix: "/other/private"},
		"signer prefix зөрсөн":             {Root: root, Signer: sgOther, PrivatePrefix: "/files/private"},
		"private=public prefix":            {Root: root, Signer: sg, URLPrefix: "/files/private"},
		"private public дотор":             {Root: root, Signer: sg, URLPrefix: "/files"},
		"PrivateRoot = public хавтас":      {Root: root, PrivateRoot: filepath.Join(root, "uploaded")},
		"PrivateRoot public дотор":         {Root: root, PrivateRoot: filepath.Join(root, "uploaded", "p")},
		"URLPrefix=/private → root зөрчил": {Root: root, URLPrefix: "/private"},
	}
	for name, cfg := range bad {
		if u, err := New(cfg); err == nil {
			_ = u.Close()
			t.Errorf("%s: алдаа хүлээсэн", name)
		}
	}
	// Тохируулга зөв: PrivateRoot Root-оос гадна; Signer-ийн угтварыг дагана.
	pr := t.TempDir()
	u, err := New(Config{Root: root, PrivateRoot: pr, Signer: sg})
	if err != nil {
		t.Fatal(err)
	}
	defer u.Close()
	if u.PrivatePrefix() != "/files/private" {
		t.Fatalf("prefix=%q", u.PrivatePrefix())
	}
	if _, err := u.StoreFile(t.Context(), StoreRequest{Filename: "a.pdf", Visibility: VisibilityPrivate, OrgID: orgA},
		bytes.NewReader(pdfData), int64(len(pdfData))); err != nil {
		t.Fatal(err)
	}
	if ents, _ := os.ReadDir(filepath.Join(pr, orgA, "documents", "2026", "11")); len(ents) != 1 {
		t.Fatalf("PrivateRoot-д файл алга: %v", ents)
	}
	// Prefix зөвхөн URLPrefix="/files" үед автоматаар өөрчлөгдөнө (хуучин тохиргоо эвдрэхгүй).
	u2, err := New(Config{Root: t.TempDir(), URLPrefix: "/files"})
	if err != nil {
		t.Fatal(err)
	}
	defer u2.Close()
	if u2.PrivatePrefix() != "/private-files" {
		t.Fatalf("prefix=%q", u2.PrivatePrefix())
	}
}

func TestCustomStorageWithoutPrivateBackend(t *testing.T) {
	ls, err := NewLocalStorage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer ls.Close()
	u, err := New(Config{Storage: ls})
	if err != nil {
		t.Fatal(err)
	}
	_, err = u.StoreFile(t.Context(), StoreRequest{Filename: "a.pdf", Visibility: VisibilityPrivate, OrgID: orgA},
		bytes.NewReader(pdfData), int64(len(pdfData)))
	var ue *Error
	if !asErr(err, &ue) || ue.Status != 501 {
		t.Fatalf("err=%v", err)
	}
	// Public ердийнхөөрөө ажиллана.
	if _, err := u.Store(t.Context(), "a.png", "image/png", bytes.NewReader(pngBytes(t, 4, 4)), int64(len(pngBytes(t, 4, 4)))); err != nil {
		t.Fatal(err)
	}
	// Тусдаа PrivateStorage өгвөл ажиллана.
	ps, _ := NewPrivateLocalStorage(t.TempDir())
	defer ps.Close()
	u2, err := New(Config{Storage: ls, PrivateStorage: ps})
	if err != nil {
		t.Fatal(err)
	}
	res, err := u2.StoreFile(t.Context(), StoreRequest{Filename: "a.pdf", Visibility: VisibilityPrivate, OrgID: orgA},
		bytes.NewReader(pdfData), int64(len(pdfData)))
	if err != nil || res.Visibility != VisibilityPrivate {
		t.Fatalf("%v %+v", err, res)
	}
	if ok, _ := ps.Exists(t.Context(), strings.TrimPrefix(res.Path, "private/")); !ok {
		t.Fatal("PrivateStorage-д хадгалагдаагүй")
	}
	if ok, _ := ls.Exists(t.Context(), strings.TrimPrefix(res.Path, "private/")); ok {
		t.Fatal("public Storage-д хадгалагдсан")
	}
}

func TestStoreFileRejectsBadOrg(t *testing.T) {
	e := newPrivEnv(t, nil)
	for _, org := range []string{"", "../x", "a/b", "a b", tenant.ZeroUUID + "/", strings.Repeat("a", 65), ".hidden"} {
		_, err := e.u.StoreFile(t.Context(), StoreRequest{Filename: "a.pdf", Visibility: VisibilityPrivate, OrgID: org},
			bytes.NewReader(pdfData), int64(len(pdfData)))
		var ue *Error
		if !asErr(err, &ue) || ue.Status != 403 {
			t.Errorf("org=%q err=%v", org, err)
		}
	}
	// Агуулгын шалгалт private-д ч хэвээр (exe→pdf).
	_, err := e.u.StoreFile(t.Context(), StoreRequest{Filename: "a.pdf", Visibility: VisibilityPrivate, OrgID: orgA},
		strings.NewReader("MZ\x90\x00 not a pdf"), 14)
	var ue *Error
	if !asErr(err, &ue) || ue.Status != 400 {
		t.Errorf("err=%v", err)
	}
}

func asErr(err error, target **Error) bool {
	for err != nil {
		if e, ok := err.(*Error); ok {
			*target = e
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

func TestSignerConcurrent(t *testing.T) {
	clk := &testClock{t: testNow}
	s := mustSigner(t, clk, SignKey{"k2", []byte(secretB)}, []SignKey{{"k1", []byte(secretA)}})
	opaque := "private/" + orgA + "/documents/2026/11/a-0123456789abcdef.pdf"
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				u, err := s.SignURL(opaque, time.Minute, SignOptions{UserID: "u"})
				if err != nil {
					t.Error(err)
					return
				}
				p, q := splitURL(t, u)
				if _, err := s.Verify(strings.TrimPrefix(p, "/files/private/"), SignedParams{Exp: q.Get("exp"), KID: q.Get("kid"), Sig: q.Get("sig"), UserID: "u"}); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
}

func TestPrivateLocalStoragePermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "p")
	ls, err := NewPrivateLocalStorage(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer ls.Close()
	if err := ls.Save(t.Context(), "o/c/2026/11/a.txt", strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"", "o", "o/c/2026/11"} {
		st, err := os.Stat(filepath.Join(dir, filepath.FromSlash(p)))
		if err != nil || st.Mode().Perm() != 0o700 {
			t.Errorf("dir %q mode=%v err=%v", p, st, err)
		}
	}
	st, _ := os.Stat(filepath.Join(dir, "o/c/2026/11/a.txt"))
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("file mode=%v", st.Mode())
	}
}
