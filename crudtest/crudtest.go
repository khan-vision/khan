// Package crudtest нь crud.Resource-ийн tenant тусгаарлалтын (leak) тестийг автоматаар үүсгэнэ
// (docs/CRUD.md §7). Хоёр org (A, B) үүсгэж, A-ийн өгөгдөл B-д ямар ч замаар (жагсаалт, GET/PUT/DELETE,
// bulk, body org_id, ref, options, children) харагдахгүй, өөрчлөгдөхгүйг баталгаажуулна.
//
//	func TestRoomsIsolation(t *testing.T) {
//	    app := buildApp(t) // жинхэнэ auth middleware-тэй
//	    crudtest.Isolation(t, crudtest.Config{
//	        App: app, Meta: rooms.Meta(), Secret: testSecret,
//	        Sample: func(org string) map[string]any { return map[string]any{"name": "Өрөө " + uniq()} },
//	        RefSample: map[string]func(org string) string{"owner_id": func(org string) string { return seedPerson(t, org) }},
//	    })
//	}
//
// Auth-ын гурван арга (нэгийг нь заавал): Secret (HS256 JWT, `Authorization: Bearer`), Token (өөрийн
// token үйлдвэрлэгч) эсвэл Auth (хүсэлтэд өөрөө header тавих; ж: HeaderAuth + ClaimsMiddleware).
package crudtest

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/golang-jwt/jwt/v5"
	"github.com/khan-vision/khan/crud"
	"github.com/khan-vision/khan/tenant"
	"github.com/khan-vision/khan/testkit"
)

// T — Isolation-д хэрэгтэй хамгийн бага testing интерфейс. *testing.T-г `Isolation` өөрөө боож өгдөг;
// өөрийн recorder-оор (generator-ийн тест) `Check`-ийг шууд дуудаж болно.
type T interface {
	Helper()
	Errorf(format string, args ...any)
	Fatalf(format string, args ...any)
	Logf(format string, args ...any)
	Skipf(format string, args ...any)
	Cleanup(fn func())
	Run(name string, fn func(t T)) bool
}

type stdT struct{ *testing.T }

func (s stdT) Run(name string, fn func(t T)) bool {
	return s.T.Run(name, func(t *testing.T) { fn(stdT{t}) })
}

// Config — тестийн тохиргоо.
type Config struct {
	// App — resource mount хийгдсэн, жинхэнэ auth middleware-тэй app.
	App *fiber.App
	// Endpoint — resource-ийн зам (ж: "/api/rooms"). Хоосон бол Meta.Endpoint.
	Endpoint string
	// Meta — Resource.Meta().
	Meta crud.Meta

	// Auth: Secret, Token, Auth-ийн яг нэг нь заавал.
	Secret string                                    // HS256 JWT гарын үсэг (claims: id, org_id, role, exp)
	Token  func(org, user string) string             // өөрийн token (Authorization: Bearer)
	Auth   func(req *http.Request, org, user string) // хүсэлтэд өөрөө нэвтрэлт тавих (HeaderAuth)

	// Role / ExtraClaims — Secret-ээр үүсэх token-ий claims (Can-д хангалттай эрх; анхдагч Role 1).
	Role        int
	ExtraClaims map[string]any

	// OrgA, OrgB, UserA, UserB — анхдагч нь санамсаргүй UUID.
	OrgA, OrgB   string
	UserA, UserB string

	// Sample — org-ийн хүчинтэй create payload. Дуудлага бүрт unique талбарт ШИНЭ утга өгөх ёстой
	// (тест нэг org-д олон мөр үүсгэнэ). Org-д хамаарах ref-үүдийг тухайн org-ийнхээр бөглөнө.
	Sample func(org string) map[string]any
	// RefSample — ref талбарын нэр (child-д "<child>.<field>") → тухайн org-д хамаарах ref мөрийн id.
	// Байхгүй ref талбарын cross-org шалгалт алгасагдана.
	RefSample map[string]func(org string) string
	// ChildSample — child нэр → тухайн org-ийн хүчинтэй child мөр (id-гүй). Байхгүй бол children шалгалт алгасагдана.
	ChildSample map[string]func(org string) map[string]any

	// SharedID — Meta.Scope "shared": ЭХЭЭС БАЙГАА системийн (org_id NULL) мөрийн id; "global": байгаа
	// ямар ч мөрийн id. Shared үед tenant-ийн тусгаарлалтын шалгалтуудад нэмж «хоёр org хоёулаа системийн
	// мөрийг харна, хэн ч өөрчилж чадахгүй» шалгана. Хоосон бол энэ хэсэг алгасагдана (Global-д заавал,
	// эс бөгөөс зөвхөн token-гүй 401 шалгана).
	SharedID string
	// SystemWritable — Test principal нь SystemCan-тай (системийн мөрийг өөрчилж чадна): 403 шалгалтыг алгасна.
	SystemWritable bool
}

// Isolation — tenant leak тестүүдийг subtest-ээр ажиллуулна.
func Isolation(t *testing.T, cfg Config) {
	t.Helper()
	Check(stdT{t}, cfg)
}

// Header-ууд (HeaderAuth ба ClaimsMiddleware): ЗӨВХӨН тестэд. Production-д X-Org-ID-д итгэхгүй.
const (
	HeaderOrg  = "X-Test-Org"
	HeaderUser = "X-Test-User"
	HeaderRole = "X-Test-Role"
)

// HeaderAuth — Config.Auth: org/user/role-г HeaderOrg/HeaderUser/HeaderRole-д тавина. App талд
// ClaimsMiddleware эсвэл адил зүйл байх ёстой.
func HeaderAuth(role int) func(req *http.Request, org, user string) {
	return func(req *http.Request, org, user string) {
		req.Header.Set(HeaderOrg, org)
		req.Header.Set(HeaderUser, user)
		req.Header.Set(HeaderRole, fmt.Sprint(role))
	}
}

// ClaimsMiddleware — HeaderAuth-ийн толгойгоос JWT claims (id, org_id, role) тавина. Толгой байхгүй бол
// юу ч тавихгүй (→ tenant.Require 401). ЗӨВХӨН тестийн app-д холбоно.
func ClaimsMiddleware() fiber.Handler {
	return func(c fiber.Ctx) error {
		if org := c.Get(HeaderOrg); org != "" {
			role := 1
			_, _ = fmt.Sscan(c.Get(HeaderRole), &role)
			c.Locals(tenant.LocalsKey, &jwt.Token{Claims: jwt.MapClaims{
				"id": c.Get(HeaderUser), "org_id": org, "role": role}, Valid: true})
		}
		return c.Next()
	}
}

// ---- runner ---------------------------------------------------------------------

type runner struct {
	t        T
	cfg      Config
	endpoint string
	idCol    string
	bearer   bool // 401 тестэд буруу Bearer token нэмж шалгах эсэх
}

type response struct {
	Status int
	Raw    []byte
	JSON   map[string]any
}

func (r response) String() string {
	return fmt.Sprintf("%d %s", r.Status, truncate(string(r.Raw), 300))
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// who — хүсэлт тавигч.
type who struct{ org, user string }

var anon = who{}

// Check — Isolation-ийн цөм (T интерфейстэй). Тохиргооны алдаа t.Fatalf.
func Check(t T, cfg Config) {
	t.Helper()
	r, err := newRunner(t, cfg)
	if err != nil {
		t.Fatalf("crudtest: %v", err)
		return
	}
	a, b := who{r.cfg.OrgA, r.cfg.UserA}, who{r.cfg.OrgB, r.cfg.UserB}
	if cfg.Meta.Scope == "global" {
		r.checkGlobal(t, a, b)
		return
	}
	rowA := r.createRow(a, nil, "A")
	rowB := r.createRow(b, nil, "B")

	t.Run("list_excludes_other_org", func(t T) { r.sub(t).listExcludes(a, b, rowA, rowB) })
	t.Run("get_other_org_404", func(t T) { r.sub(t).getOther(a, b, rowA) })
	t.Run("update_other_org_404", func(t T) { r.sub(t).updateOther(a, b, rowA) })
	t.Run("delete_other_org_404", func(t T) { r.sub(t).deleteOther(a, b, rowA) })
	if cfg.Meta.Bulk {
		t.Run("bulk_other_org_affects_nothing", func(t T) { r.sub(t).bulkOther(a, b, rowA) })
	}
	t.Run("create_ignores_body_org_id", func(t T) { r.sub(t).createBodyOrg(a, b, rowA) })
	if hasRefs(cfg.Meta) {
		t.Run("ref_cross_org_422", func(t T) { r.sub(t).refCrossOrg(a, b) })
		t.Run("options_never_leak", func(t T) { r.sub(t).optionsLeak(a, b) })
	}
	if len(cfg.Meta.Children) > 0 {
		t.Run("children_cross_org", func(t T) { r.sub(t).childrenCross(a, b, rowA) })
	}
	if cfg.Meta.Scope == "shared" && cfg.SharedID != "" {
		t.Run("shared_system_rows", func(t T) { r.sub(t).sharedSystem(a, b, row{id: cfg.SharedID}) })
	}
	t.Run("unauthenticated_401", func(t T) { r.sub(t).unauthenticated(rowA) })
}

// checkGlobal — ScopeGlobal: мөр org-оос үл хамаарна → «B A-ийнхыг харахгүй» биш, ХОЁР org ИЖИЛ
// өгөгдөл харна; token-гүй 401. GlobalWrites үед A үүсгэсэн мөрийг B шууд харна.
func (r *runner) checkGlobal(t T, a, b who) {
	id := r.cfg.SharedID
	if r.cfg.Meta.GlobalWrites {
		id = r.createRow(a, nil, "A").id
	}
	if id != "" {
		t.Run("global_visible_to_every_org", func(t T) {
			rr := r.sub(t)
			for _, w := range []who{a, b} {
				res := rr.do(w, "GET", "/"+url.PathEscape(id), nil)
				rr.wantStatus(res, 200, "GET /:id (global мөр, "+w.org+")")
				if l := rr.do(w, "GET", "?limit=200", nil); l.Status != 200 {
					t.Errorf("GET / (%s): %s", w.org, l)
				}
			}
		})
	}
	t.Run("unauthenticated_401", func(t T) { r.sub(t).unauthenticated(row{id: id}) })
}

// sharedSystem — ScopeShared: системийн мөрийг хоёр org харна (жагсаалт, detail); өөрчлөх оролдлого
// (PUT, DELETE, bulk) 403 бөгөөд мөр өөрчлөгдөхгүй (SystemWritable үед алгасна).
func (r *runner) sharedSystem(a, b who, sys row) {
	r.t.Helper()
	before := r.snapshot(a, sys.id)
	for _, w := range []who{a, b} {
		if res := r.do(w, "GET", "/"+url.PathEscape(sys.id), nil); res.Status != 200 {
			r.t.Errorf("%s: системийн мөр харагдсангүй: %s", w.org, res)
		}
		if l := r.do(w, "GET", "?limit=200", nil); l.Status != 200 || (len(r.listIDs(l)) > 0 && !r.listIDs(l)[sys.id]) {
			r.t.Errorf("%s: системийн мөр жагсаалтад алга: %s", w.org, l)
		}
	}
	if r.cfg.SystemWritable {
		return
	}
	for _, w := range []who{a, b} {
		put := before.JSON
		body := map[string]any{}
		if v := r.cfg.Meta.Version; v != "" {
			body[v] = put[v]
		}
		for k, v := range r.payload(w.org, nil) {
			body[k] = v
		}
		r.wantStatus(r.do(w, "PUT", "/"+url.PathEscape(sys.id), body), 403, w.org+": PUT системийн мөр")
		r.wantStatus(r.do(w, "DELETE", "/"+url.PathEscape(sys.id), nil), 403, w.org+": DELETE системийн мөр")
		if r.cfg.Meta.Bulk {
			act := "delete"
			if !r.cfg.Meta.BulkDelete {
				act = "update"
			}
			b := map[string]any{"action": act, "ids": []any{r.idJSON(sys.id)}}
			if act == "update" && len(r.cfg.Meta.BulkFields) > 0 {
				b["set"] = map[string]any{}
			}
			if res := r.do(w, "POST", "/bulk", b); res.Status == 200 {
				r.t.Errorf("%s: bulk системийн мөрөнд амжилттай (affected) — LEAK: %s", w.org, res)
			}
		}
	}
	r.unchanged(a, sys, before, "системийн мөрийн PUT/DELETE/bulk")
}

func newRunner(t T, cfg Config) (*runner, error) {
	if cfg.App == nil {
		return nil, errors.New("Config.App nil")
	}
	if cfg.Sample == nil {
		return nil, errors.New("Config.Sample nil")
	}
	n := 0
	if cfg.Secret != "" {
		n++
	}
	if cfg.Token != nil {
		n++
	}
	if cfg.Auth != nil {
		n++
	}
	if n != 1 {
		return nil, errors.New("Secret, Token, Auth-ийн яг нэгийг тохируул")
	}
	if cfg.Endpoint == "" {
		cfg.Endpoint = cfg.Meta.Endpoint
	}
	if cfg.Endpoint == "" || !strings.HasPrefix(cfg.Endpoint, "/") {
		return nil, fmt.Errorf("Endpoint буруу: %q", cfg.Endpoint)
	}
	cfg.Endpoint = strings.TrimRight(cfg.Endpoint, "/")
	def := func(p *string) {
		if *p == "" {
			*p = newUUID()
		}
	}
	def(&cfg.OrgA)
	def(&cfg.OrgB)
	def(&cfg.UserA)
	def(&cfg.UserB)
	if cfg.OrgA == cfg.OrgB {
		return nil, errors.New("OrgA == OrgB")
	}
	if cfg.Role == 0 {
		cfg.Role = 1
	}
	idCol := cfg.Meta.ID.Column
	if idCol == "" {
		idCol = "id"
	}
	return &runner{t: t, cfg: cfg, endpoint: cfg.Endpoint, idCol: idCol, bearer: cfg.Auth == nil}, nil
}

// sub — subtest-ийн T-тэй runner хуулбар.
func (r *runner) sub(t T) *runner { c := *r; c.t = t; return &c }

func (r *runner) token(w who) (string, error) {
	if r.cfg.Token != nil {
		return r.cfg.Token(w.org, w.user), nil
	}
	claims := testkit.Claims(w.user, w.org, r.cfg.Role)
	for k, v := range r.cfg.ExtraClaims {
		claims[k] = v
	}
	return testkit.SignedToken(r.cfg.Secret, claims)
}

// do — хүсэлт. w=anon бол нэвтрэлтгүй. body: nil, []byte/string (түүхий) эсвэл JSON болгох утга.
func (r *runner) do(w who, method, path string, body any) response {
	r.t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	case []byte:
		rd = bytes.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			r.t.Fatalf("crudtest: body кодлох: %v", err)
		}
		rd = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, r.endpoint+path, rd)
	req.Header.Set("Content-Type", "application/json")
	if w != anon {
		switch {
		case r.cfg.Auth != nil:
			r.cfg.Auth(req, w.org, w.user)
		default:
			tok, err := r.token(w)
			if err != nil {
				r.t.Fatalf("crudtest: token: %v", err)
			}
			req.Header.Set("Authorization", "Bearer "+tok)
		}
	}
	return r.send(req)
}

func (r *runner) send(req *http.Request) response {
	r.t.Helper()
	res, err := r.cfg.App.Test(req, fiber.TestConfig{Timeout: 0})
	if err != nil {
		r.t.Fatalf("crudtest: %s %s: %v", req.Method, req.URL.Path, err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	out := response{Status: res.StatusCode, Raw: raw}
	_ = json.Unmarshal(raw, &out.JSON)
	return out
}

// ---- fixtures -------------------------------------------------------------------

type row struct {
	id   string
	who  who
	body response // create хариу
}

func idString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case float64:
		return fmt.Sprintf("%.0f", x)
	default:
		return fmt.Sprint(x)
	}
}

// payload — Sample(org) + (ChildSample байвал) children + нэмэлт override. Давхар хуулбар.
func (r *runner) payload(org string, extra map[string]any) map[string]any {
	p := map[string]any{}
	for k, v := range r.cfg.Sample(org) {
		p[k] = v
	}
	for _, c := range r.cfg.Meta.Children {
		if cs := r.cfg.ChildSample[c.Name]; cs != nil {
			p[c.Name] = []any{cs(org)}
		}
	}
	for k, v := range extra {
		p[k] = v
	}
	return p
}

// createRow — w-ээр мөр үүсгэнэ (201 заавал, үгүй бол Fatal — Sample буруу). Cleanup-д API-аар устгана (best effort).
func (r *runner) createRow(w who, extra map[string]any, name string) row {
	r.t.Helper()
	res := r.do(w, "POST", "", r.payload(w.org, extra))
	if res.Status != 201 {
		r.t.Fatalf("crudtest: %s org-ийн Sample-ээр үүсгэж чадсангүй: %s\n"+
			"(Sample хүчинтэй, unique талбар нь дуудлага бүрт өөр, Role/Can эрхтэй эсэхийг шалга)", name, res)
	}
	id := idString(res.JSON[r.idCol])
	if id == "" {
		r.t.Fatalf("crudtest: create хариунд %q алга: %s", r.idCol, res)
	}
	rw := row{id: id, who: w, body: res}
	r.t.Cleanup(func() { r.cleanup(w, id) })
	return rw
}

func (r *runner) cleanup(w who, id string) {
	req := httptest.NewRequest("DELETE", r.endpoint+"/"+url.PathEscape(id), nil)
	if r.cfg.Auth != nil {
		r.cfg.Auth(req, w.org, w.user)
	} else if tok, err := r.token(w); err == nil {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	if res, err := r.cfg.App.Test(req, fiber.TestConfig{Timeout: 0}); err == nil {
		res.Body.Close()
	}
}

// ---- assertions -----------------------------------------------------------------

func (r *runner) wantStatus(res response, want int, what string) {
	r.t.Helper()
	if res.Status != want {
		r.t.Errorf("%s: %d хүлээсэн, ирсэн %s", what, want, res)
	}
}

// listIDs — жагсаалтын хариунд id-ууд (data[].<idCol>), сүүлд түүхий хариу.
func (r *runner) listIDs(res response) (ids map[string]bool) {
	ids = map[string]bool{}
	if arr, ok := res.JSON["data"].([]any); ok {
		for _, x := range arr {
			if m, ok := x.(map[string]any); ok {
				if v, has := m[r.idCol]; has {
					ids[idString(v)] = true
				}
			}
		}
	}
	return ids
}

// mentions — A-ийн id хариунд ямар ч хэлбэрээр байгаа эсэх. UUID бол түүхий текстээр (хаана ч), бүхэл тоо бол data[] id-аар.
func (r *runner) mentions(res response, id string) bool {
	if strings.Contains(id, "-") {
		return strings.Contains(strings.ToLower(string(res.Raw)), strings.ToLower(id))
	}
	return r.listIDs(res)[id]
}

func (r *runner) listExcludes(a, b who, rowA, rowB row) {
	r.t.Helper()
	for _, c := range []struct{ name, method, path string }{
		{"GET /", "GET", "?limit=200"}, {"POST /query", "POST", "/query"},
	} {
		var body any
		if c.method == "POST" {
			body = map[string]any{"limit": 200}
		}
		res := r.do(b, c.method, c.path, body)
		if res.Status != 200 {
			r.t.Errorf("%s (B): 200 хүлээсэн, ирсэн %s", c.name, res)
			continue
		}
		if r.mentions(res, rowA.id) {
			r.t.Errorf("%s: B-ийн жагсаалтад A-ийн мөр (%s) гарлаа — LEAK", c.name, rowA.id)
		}
		// vacuous биш: B өөрийнхөө мөрийг харах ёстой (list route-ийн data нь id-г агуулдаг үед)
		if ids := r.listIDs(res); len(ids) > 0 && !ids[rowB.id] {
			r.t.Errorf("%s: B өөрийн мөрийг (%s) жагсаалтаас олсонгүй (limit 200)", c.name, rowB.id)
		}
	}
	// A өөрийнхөө мөрийг харна
	res := r.do(a, "GET", "?limit=200", nil)
	if ids := r.listIDs(res); len(ids) > 0 && !ids[rowA.id] {
		r.t.Errorf("A өөрийн мөрийг жагсаалтаас олсонгүй: %s", res)
	}
}

// snapshot — A-ийн мөрийн одоогийн байдал (түүхий).
func (r *runner) snapshot(a who, id string) response {
	r.t.Helper()
	res := r.do(a, "GET", "/"+url.PathEscape(id), nil)
	if res.Status != 200 {
		r.t.Errorf("A өөрийн мөрийг (%s) уншиж чадсангүй: %s", id, res)
	}
	return res
}

func (r *runner) unchanged(a who, rowA row, before response, what string) {
	r.t.Helper()
	after := r.snapshot(a, rowA.id)
	if after.Status == 200 && !bytes.Equal(before.Raw, after.Raw) {
		r.t.Errorf("%s: A-ийн мөр өөрчлөгдсөн — LEAK\nөмнө: %s\nдараа: %s", what, truncate(string(before.Raw), 300), truncate(string(after.Raw), 300))
	}
}

func (r *runner) getOther(a, b who, rowA row) {
	r.t.Helper()
	before := r.snapshot(a, rowA.id)
	res := r.do(b, "GET", "/"+url.PathEscape(rowA.id), nil)
	r.wantStatus(res, 404, "B: GET /:id (A-ийн мөр)")
	if res.Status == 200 {
		return
	}
	r.unchanged(a, rowA, before, "GET")
}

// updateBody — PUT body: CreateOnly-гүй Sample(org) + version (A-ийн мөрөөс, зөрчлөөр 409 болж 404-ийг далдлахгүйн тулд).
func (r *runner) updateBody(w who, target row) map[string]any {
	body := map[string]any{}
	create := map[string]bool{}
	for _, f := range r.cfg.Meta.Fields {
		create[f.Name] = f.CreateOnly
	}
	for k, v := range r.payload(w.org, nil) {
		if !create[k] {
			body[k] = v
		}
	}
	if v := r.cfg.Meta.Version; v != "" {
		body[v] = target.body.JSON[v]
	}
	return body
}

func (r *runner) updateOther(a, b who, rowA row) {
	r.t.Helper()
	before := r.snapshot(a, rowA.id)
	res := r.do(b, "PUT", "/"+url.PathEscape(rowA.id), r.updateBody(b, rowA))
	r.wantStatus(res, 404, "B: PUT /:id (A-ийн мөр)")
	r.unchanged(a, rowA, before, "PUT")
}

func (r *runner) deleteOther(a, b who, rowA row) {
	r.t.Helper()
	before := r.snapshot(a, rowA.id)
	res := r.do(b, "DELETE", "/"+url.PathEscape(rowA.id), nil)
	r.wantStatus(res, 404, "B: DELETE /:id (A-ийн мөр)")
	r.unchanged(a, rowA, before, "DELETE")
}

func (r *runner) idJSON(id string) any {
	if r.cfg.Meta.ID.Kind == crud.IDInt {
		var n json.Number = json.Number(id)
		return n
	}
	return id
}

func (r *runner) bulkOther(a, b who, rowA row) {
	r.t.Helper()
	before := r.snapshot(a, rowA.id)
	m := r.cfg.Meta
	ran := false
	if m.BulkDelete {
		ran = true
		res := r.do(b, "POST", "/bulk", map[string]any{"action": "delete", "ids": []any{r.idJSON(rowA.id)}})
		r.bulkZero(res, "bulk delete")
		r.unchanged(a, rowA, before, "bulk delete")
	}
	for _, field := range m.BulkFields {
		v, ok := r.cfg.Sample(b.org)[field]
		if !ok {
			continue
		}
		ran = true
		res := r.do(b, "POST", "/bulk", map[string]any{"action": "update", "ids": []any{r.idJSON(rowA.id)}, "set": map[string]any{field: v}})
		r.bulkZero(res, "bulk update "+field)
		r.unchanged(a, rowA, before, "bulk update "+field)
		break // нэг талбар хангалттай
	}
	if !ran {
		r.t.Logf("bulk: delete зөвшөөрөгдөөгүй, BulkFields-д Sample-ийн утга алга — шалгалт алгасав")
	}
}

func (r *runner) bulkZero(res response, what string) {
	r.t.Helper()
	if res.Status != 200 {
		r.t.Errorf("B: %s: 200 хүлээсэн, ирсэн %s (Role/Can эрх хангалттай эсэхийг шалга)", what, res)
		return
	}
	if n, ok := res.JSON["affected"].(float64); !ok || n != 0 {
		r.t.Errorf("B: %s өөр org-ийн мөрд нөлөөлсөн: %s — LEAK", what, res)
	}
}

func (r *runner) createBodyOrg(a, b who, rowA row) {
	r.t.Helper()
	extra := map[string]any{"org_id": a.org, r.idCol: rowA.id}
	res := r.do(b, "POST", "", r.payload(b.org, extra))
	if res.Status != 201 {
		r.t.Errorf("B: body org_id=A-тай create: 201 хүлээсэн (org_id үл тоогдоно), ирсэн %s", res)
		return
	}
	id := idString(res.JSON[r.idCol])
	r.t.Cleanup(func() { r.cleanup(b, id) })
	if id == rowA.id {
		r.t.Errorf("body-ийн id (A-ийн мөр) хүлээн авагдсан — LEAK/overwrite")
	}
	if o, ok := res.JSON["org_id"]; ok && idString(o) != b.org {
		r.t.Errorf("create хариуны org_id=%v, B (%s) байх ёстой — LEAK", o, b.org)
	}
	if g := r.do(a, "GET", "/"+url.PathEscape(id), nil); g.Status != 404 {
		r.t.Errorf("A шинэ мөрийг (B-ийнх) харж байна: %s — LEAK", g)
	}
	if g := r.do(b, "GET", "/"+url.PathEscape(id), nil); g.Status != 200 {
		r.t.Errorf("B өөрийн шинэ мөрийг харж чадсангүй: %s", g)
	}
	if l := r.do(a, "GET", "?limit=200", nil); l.Status == 200 && r.mentions(l, id) {
		r.t.Errorf("A-ийн жагсаалтад B-ийн шинэ мөр гарлаа — LEAK")
	}
	// A-ийн анхны мөр хэвээр
	if g := r.do(a, "GET", "/"+url.PathEscape(rowA.id), nil); g.Status != 200 {
		r.t.Errorf("A-ийн мөр алга болсон: %s", g)
	}
}

type refField struct {
	key   string // RefSample/options түлхүүр
	field string // payload-ын талбар
	child string // child нэр ("" = эцэг)
}

func refFields(m crud.Meta) []refField {
	var out []refField
	for _, f := range m.Fields {
		if f.Ref != nil {
			out = append(out, refField{key: f.Name, field: f.Name})
		}
	}
	for _, c := range m.Children {
		for _, f := range c.Fields {
			if f.Ref != nil {
				out = append(out, refField{key: c.Name + "." + f.Name, field: f.Name, child: c.Name})
			}
		}
	}
	return out
}

func hasRefs(m crud.Meta) bool { return len(refFields(m)) > 0 }

func (r *runner) refCrossOrg(a, b who) {
	r.t.Helper()
	tested := 0
	for _, rf := range refFields(r.cfg.Meta) {
		sample := r.cfg.RefSample[rf.key]
		if sample == nil {
			r.t.Logf("ref %s: RefSample алга — алгасав", rf.key)
			continue
		}
		foreign := sample(a.org)
		if foreign == "" {
			r.t.Errorf("ref %s: RefSample(A) хоосон id буцаав", rf.key)
			continue
		}
		extra := r.withRef(b, rf, foreign)
		if extra == nil {
			r.t.Logf("ref %s: ChildSample алга — алгасав", rf.key)
			continue
		}
		tested++
		res := r.do(b, "POST", "", extra)
		switch {
		case res.Status == 201:
			id := idString(res.JSON[r.idCol])
			r.t.Cleanup(func() { r.cleanup(b, id) })
			r.t.Errorf("ref %s: B нь A-ийн id-г заасан мөр үүсгэж чадлаа — LEAK: %s", rf.key, res)
		case res.Status != 422:
			r.t.Errorf("ref %s: create 422 хүлээсэн, ирсэн %s", rf.key, res)
		default:
			if !r.errorsMention(res, rf) {
				r.t.Errorf("ref %s: 422 боловч errors-д %q түлхүүр алга: %s", rf.key, rf.field, res)
			}
		}
		// update: B-ийн өөрийн мөр дээр
		own := r.createRow(b, nil, "B")
		body := r.updateBody(b, own)
		for k, v := range r.withRef(b, rf, foreign) {
			if k == rf.field && rf.child == "" || k == rf.child && rf.child != "" {
				body[k] = v
			}
		}
		up := r.do(b, "PUT", "/"+url.PathEscape(own.id), body)
		if up.Status != 422 {
			r.t.Errorf("ref %s: update 422 хүлээсэн, ирсэн %s", rf.key, up)
		}
	}
	if tested == 0 {
		r.t.Skipf("RefSample өгөөгүй тул ref шалгалт хийгдсэнгүй")
	}
}

// withRef — B-ийн хүчинтэй payload, rf талбарыг foreign-оор сольсон. Child-д ChildSample байхгүй бол nil.
func (r *runner) withRef(w who, rf refField, foreign string) map[string]any {
	p := r.payload(w.org, nil)
	if rf.child == "" {
		p[rf.field] = foreign
		return p
	}
	cs := r.cfg.ChildSample[rf.child]
	if cs == nil {
		return nil
	}
	item := map[string]any{}
	for k, v := range cs(w.org) {
		item[k] = v
	}
	item[rf.field] = foreign
	p[rf.child] = []any{item}
	return p
}

func (r *runner) errorsMention(res response, rf refField) bool {
	errs, _ := res.JSON["errors"].(map[string]any)
	for k := range errs {
		if rf.child == "" && k == rf.field {
			return true
		}
		if rf.child != "" && strings.HasPrefix(k, rf.child+".") && strings.HasSuffix(k, "."+rf.field) {
			return true
		}
	}
	return false
}

func (r *runner) optionsLeak(a, b who) {
	r.t.Helper()
	tested := 0
	for _, rf := range refFields(r.cfg.Meta) {
		sample := r.cfg.RefSample[rf.key]
		if sample == nil {
			continue
		}
		tested++
		foreign, own := sample(a.org), sample(b.org)
		base := "/options/" + url.PathEscape(rf.key)
		for _, q := range []string{"?limit=200", "?ids=" + url.QueryEscape(foreign), "?ids=" + url.QueryEscape(foreign+","+own) + "&limit=200"} {
			res := r.do(b, "GET", base+q, nil)
			if res.Status != 200 {
				r.t.Errorf("options %s%s (B): 200 хүлээсэн, ирсэн %s", rf.key, q, res)
				continue
			}
			if r.optionHas(res, foreign) {
				r.t.Errorf("options %s%s: B-д A-ийн утга (%s) гарлаа — LEAK", rf.key, q, foreign)
			}
		}
		// vacuous биш: B өөрийнхөө утгыг ids-ээр авч чадна
		res := r.do(b, "GET", base+"?ids="+url.QueryEscape(own), nil)
		if res.Status == 200 && !r.optionHas(res, own) {
			r.t.Errorf("options %s: B өөрийн утгыг (%s) ids-ээр авч чадсангүй: %s", rf.key, own, res)
		}
	}
	if tested == 0 {
		r.t.Skipf("RefSample өгөөгүй тул options шалгалт хийгдсэнгүй")
	}
}

func (r *runner) optionHas(res response, value string) bool {
	arr, _ := res.JSON["data"].([]any)
	for _, x := range arr {
		if m, ok := x.(map[string]any); ok && strings.EqualFold(idString(m["value"]), value) {
			return true
		}
	}
	return false
}

func (r *runner) childrenCross(a, b who, rowA row) {
	r.t.Helper()
	tested := 0
	for _, c := range r.cfg.Meta.Children {
		cs := r.cfg.ChildSample[c.Name]
		if cs == nil {
			r.t.Logf("children %s: ChildSample алга — алгасав", c.Name)
			continue
		}
		tested++
		// A-ийн child id-г A-ийн detail-аас авна.
		detailA := r.snapshot(a, rowA.id)
		var aChildID string
		if arr, ok := detailA.JSON[c.Name].([]any); ok && len(arr) > 0 {
			if m, ok := arr[0].(map[string]any); ok {
				aChildID = idString(m["id"])
			}
		}
		if aChildID == "" {
			r.t.Errorf("children %s: A-ийн detail-д child мөр алга (%s)", c.Name, detailA)
			continue
		}
		own := r.createRow(b, nil, "B")
		// 1) B өөрийн эцэг дээр A-ийн child id-г засахыг оролдоно.
		item := map[string]any{"id": aChildID}
		for k, v := range cs(b.org) {
			item[k] = v
		}
		body := r.updateBody(b, own)
		body[c.Name] = []any{item}
		up := r.do(b, "PUT", "/"+url.PathEscape(own.id), body)
		if up.Status == 200 {
			r.t.Errorf("children %s: B нь A-ийн child id-г өөрийн мөрөнд холбож чадлаа — LEAK: %s", c.Name, up)
		} else if up.Status != 422 {
			r.t.Errorf("children %s: A-ийн child id-д 422 хүлээсэн, ирсэн %s", c.Name, up)
		}
		// 2) create-д ирсэн child id үл тоогдоно (шинэ мөр үүснэ, A-ийнх хөндөгдөхгүй).
		p := r.payload(b.org, nil)
		item2 := map[string]any{"id": aChildID}
		for k, v := range cs(b.org) {
			item2[k] = v
		}
		p[c.Name] = []any{item2}
		cr := r.do(b, "POST", "", p)
		if cr.Status == 201 {
			r.t.Cleanup(func() { r.cleanup(b, idString(cr.JSON[r.idCol])) })
		}
		// A-ийн мөр (child-тэй) өөрчлөгдөөгүй байх ёстой
		after := r.snapshot(a, rowA.id)
		if after.Status == 200 && !bytes.Equal(detailA.Raw, after.Raw) {
			r.t.Errorf("children %s: A-ийн мөр/child өөрчлөгдсөн — LEAK\nөмнө: %s\nдараа: %s", c.Name,
				truncate(string(detailA.Raw), 300), truncate(string(after.Raw), 300))
		}
	}
	if tested == 0 {
		r.t.Skipf("ChildSample өгөөгүй тул children шалгалт хийгдсэнгүй")
	}
}

func (r *runner) unauthenticated(rowA row) {
	r.t.Helper()
	id := "/" + url.PathEscape(rowA.id)
	cases := []struct{ method, path string }{
		{"GET", ""}, {"POST", "/query"}, {"GET", id}, {"POST", ""}, {"PUT", id}, {"DELETE", id},
	}
	m := r.cfg.Meta
	if m.Bulk {
		cases = append(cases, struct{ method, path string }{"POST", "/bulk"})
	}
	if m.Export {
		cases = append(cases, struct{ method, path string }{"GET", "/export.xlsx"})
	}
	if hasRefs(m) {
		cases = append(cases, struct{ method, path string }{"GET", "/options/" + refFields(m)[0].key})
	}
	for _, c := range cases {
		var body any
		if c.method == "POST" || c.method == "PUT" {
			body = map[string]any{}
		}
		res := r.do(anon, c.method, c.path, body)
		// унших-л Global-д бичих route mount хийгдээгүй (405/404) — өгөгдөл өөрчлөгдөхгүй тул зөвшөөрнө
		if readOnlyGlobal := m.Scope == "global" && !m.GlobalWrites; readOnlyGlobal && c.method != "GET" && c.path != "/query" &&
			(res.Status == 405 || res.Status == 404) {
			continue
		}
		if res.Status != 401 {
			r.t.Errorf("token-гүй %s %s: 401 хүлээсэн, ирсэн %s", c.method, c.path, res)
		}
	}
	if r.bearer {
		req := httptest.NewRequest("GET", r.endpoint+id, nil)
		req.Header.Set("Authorization", "Bearer not.a.token")
		if res := r.send(req); res.Status != 401 {
			r.t.Errorf("буруу token: 401 хүлээсэн, ирсэн %s", res)
		}
	}
}

// newUUID — crypto/rand-аас UUIDv4.
func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err) // crypto/rand унах нь хурааж болохгүй орчны алдаа
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
