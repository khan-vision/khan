package access

import (
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/golang-jwt/jwt/v5"
	"github.com/khan-vision/khan/crud"
	"github.com/khan-vision/khan/testkit"
)

func claims(user, org string) jwt.MapClaims {
	return jwt.MapClaims{"id": user, "org_id": org, "role": 6}
}

// appWith — claims-тай (nil → нэвтрээгүй) Fiber app.
func appWith(cl jwt.MapClaims, register func(app *fiber.App)) *fiber.App {
	app := fiber.New()
	app.Use(testkit.WithClaims(cl))
	register(app)
	return app
}

func do(t *testing.T, app *fiber.App, method, path string, hdr map[string]string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func httpPolicy(t *testing.T, opts ...Option) (*Policy, *MemorySource) {
	t.Helper()
	src := NewMemorySource()
	src.Set(orgA, "viewer", g(map[string]ModuleGrant{"finance": {Roles: []string{"viewer"}}}))
	src.Set(orgA, "acct", g(map[string]ModuleGrant{"finance": {Roles: []string{"accountant"}}}))
	src.Set(orgA, "boss", Grants{Owner: true})
	p, _ := newTestPolicy(t, src, opts...)
	return p, src
}

func TestRequire_401_403_200(t *testing.T) {
	p, _ := httpPolicy(t)
	ok := func(c fiber.Ctx) error { return c.SendString("ok") }
	reg := func(app *fiber.App) {
		app.Get("/inv", p.Require("finance", "invoices.view"), ok)
		app.Post("/post", p.Require("finance", "invoices.post"), ok)
		app.Get("/any", p.Require("finance", "invoices.post", "invoices.view"), ok)
	}
	cases := []struct {
		name   string
		cl     jwt.MapClaims
		method string
		path   string
		status int
	}{
		{"no token", nil, "GET", "/inv", 401},
		{"no org", jwt.MapClaims{"id": "viewer"}, "GET", "/inv", 401},
		{"zero org", jwt.MapClaims{"id": "viewer", "org_id": tenantZero}, "GET", "/inv", 401},
		{"no user id", jwt.MapClaims{"org_id": orgA}, "GET", "/inv", 401},
		{"viewer can view", claims("viewer", orgA), "GET", "/inv", 200},
		{"viewer cannot post", claims("viewer", orgA), "POST", "/post", 403},
		{"accountant can post", claims("acct", orgA), "POST", "/post", 200},
		{"any-of", claims("viewer", orgA), "GET", "/any", 200},
		{"owner", claims("boss", orgA), "POST", "/post", 200},
		{"unknown user", claims("stranger", orgA), "GET", "/inv", 403},
		{"same user other org", claims("viewer", orgB), "GET", "/inv", 403},
		{"numeric user id (legacy token)", jwt.MapClaims{"id": float64(7), "org_id": orgA}, "GET", "/inv", 403},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, body := do(t, appWith(c.cl, reg), c.method, c.path, nil)
			if status != c.status {
				t.Fatalf("status=%d body=%s want %d", status, body, c.status)
			}
			switch status {
			case 401:
				assertJSONError(t, body, MsgUnauthorized)
			case 403:
				var m map[string]any
				if err := json.Unmarshal([]byte(body), &m); err != nil || m["error"] != MsgForbidden || m["module"] != "finance" || m["capability"] == "" || m["capability"] == nil {
					t.Fatalf("403 body: %s", body)
				}
			}
		})
	}
}

const tenantZero = "00000000-0000-0000-0000-000000000000"

func assertJSONError(t *testing.T, body, want string) {
	t.Helper()
	var m map[string]string
	if err := json.Unmarshal([]byte(body), &m); err != nil || m["error"] != want || len(m) != 1 {
		t.Fatalf("body=%s want error=%q (өөр талбаргүй)", body, want)
	}
}

// X-Org-ID header болон ?org_id нь эрхэд нөлөөлөх ёсгүй.
func TestRequire_IgnoresClientOrg(t *testing.T) {
	p, _ := httpPolicy(t)
	reg := func(app *fiber.App) {
		app.Get("/inv", p.Require("finance", "invoices.view"), func(c fiber.Ctx) error { return c.SendString("ok") })
	}
	// viewer нь org A-д эрхтэй. JWT org B → header-ээр A гэж хэлсэн ч 403.
	app := appWith(claims("viewer", orgB), reg)
	if st, _ := do(t, app, "GET", "/inv?org_id="+orgA, map[string]string{"X-Org-ID": orgA}); st != 403 {
		t.Fatalf("header/query-д итгэсэн: %d", st)
	}
	// эсрэгээр: JWT A, header B → эрх хэвээр.
	app = appWith(claims("viewer", orgA), reg)
	if st, _ := do(t, app, "GET", "/inv", map[string]string{"X-Org-ID": orgB}); st != 200 {
		t.Fatalf("header нөлөөлсөн: %d", st)
	}
}

func TestRequire_SourceError_503(t *testing.T) {
	p, src := httpPolicy(t)
	src.SetError(errors.New("db down"))
	app := appWith(claims("boss", orgA), func(app *fiber.App) {
		app.Get("/inv", p.Require("finance", "invoices.view"), func(c fiber.Ctx) error { return c.SendString("ok") })
	})
	st, body := do(t, app, "GET", "/inv", nil)
	if st != 503 {
		t.Fatalf("status=%d %s", st, body)
	}
	assertJSONError(t, body, MsgUnavailable)
}

func TestRequire_Misconfigured_FailsClosed(t *testing.T) {
	p, _ := httpPolicy(t)
	for name, h := range map[string]fiber.Handler{
		"typo cap":       p.Require("finance", "invoices.veiw"),
		"typo module":    p.Require("finanse", "invoices.view"),
		"no caps":        p.Require("finance"),
		"mixed typo cap": p.Require("finance", "invoices.view", "nope.nope"),
	} {
		t.Run(name, func(t *testing.T) {
			app := appWith(claims("boss", orgA), func(app *fiber.App) {
				app.Get("/x", h, func(c fiber.Ctx) error { return c.SendString("ok") })
			})
			if st, _ := do(t, app, "GET", "/x", nil); st != 500 {
				t.Fatalf("owner ч нэвтрэх ёсгүй: %d", st)
			}
		})
	}
}

func TestRequire_CopiesCaps(t *testing.T) {
	p, _ := httpPolicy(t)
	caps := []string{"invoices.view"}
	h := p.Require("finance", caps...)
	caps[0] = "invoices.post" // дараа нь мутаци
	app := appWith(claims("viewer", orgA), func(app *fiber.App) {
		app.Get("/x", h, func(c fiber.Ctx) error { return c.SendString("ok") })
	})
	if st, _ := do(t, app, "GET", "/x", nil); st != 200 {
		t.Fatalf("%d", st)
	}
}

func TestRequire_PortalOnly_NotTransitioned(t *testing.T) {
	p, _ := httpPolicy(t, WithTransition("finance", TransitionUser))
	reg := func(app *fiber.App) {
		app.Get("/inv", p.Require("finance", "invoices.view"), func(c fiber.Ctx) error { return c.SendString("ok") })
	}
	// эрхгүй ердийн хэрэглэгч → transition-ээр нэвтэрнэ
	if st, _ := do(t, appWith(claims("stranger", orgA), reg), "GET", "/inv", nil); st != 200 {
		t.Fatalf("transition: %d", st)
	}
	// portal-only token (сурагч/эцэг эх) → transition үйлчлэхгүй
	cl := claims("stranger", orgA)
	cl["portal_only"] = true
	if st, _ := do(t, appWith(cl, reg), "GET", "/inv", nil); st != 403 {
		t.Fatalf("portal-only transition-аар нэвтэрсэн: %d", st)
	}
}

func TestCan_And_CanAny(t *testing.T) {
	p, _ := httpPolicy(t)
	var got []bool
	app := appWith(claims("viewer", orgA), func(app *fiber.App) {
		app.Get("/", func(c fiber.Ctx) error {
			got = []bool{
				p.Can(c, "finance", "invoices.view"),
				p.Can(c, "finance", "invoices.post"),
				p.Can(c, "finance", "typo"),
				p.Can(c, "ghost", "invoices.view"),
				p.CanAny(c, "finance", "invoices.post", "invoices.view"),
				p.CanAny(c, "finance"),
			}
			return nil
		})
	})
	do(t, app, "GET", "/", nil)
	want := []bool{true, false, false, false, true, false}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
	// нэвтрээгүй
	app = appWith(nil, func(app *fiber.App) {
		app.Get("/", func(c fiber.Ctx) error { got = []bool{p.Can(c, "finance", "invoices.view")}; return nil })
	})
	do(t, app, "GET", "/", nil)
	if got[0] {
		t.Fatal("нэвтрээгүй Can=true")
	}
}

// Нэг хүсэлтэд олон Can → Source нэг л удаа (Locals).
func TestCan_PerRequestCache(t *testing.T) {
	src := NewMemorySource()
	src.Set(orgA, "viewer", g(map[string]ModuleGrant{"finance": {Roles: []string{"viewer"}}}))
	p, _ := newTestPolicy(t, src, WithTTL(0)) // process cache унтраалттай
	app := appWith(claims("viewer", orgA), func(app *fiber.App) {
		app.Get("/", func(c fiber.Ctx) error {
			for i := 0; i < 5; i++ {
				p.Can(c, "finance", "invoices.view")
			}
			return nil
		})
	})
	do(t, app, "GET", "/", nil)
	if src.Loads() != 1 {
		t.Fatalf("loads=%d", src.Loads())
	}
	// Хоёр өөр Policy-ийн Locals холилдохгүй
	src2 := NewMemorySource()
	p2, _ := newTestPolicy(t, src2, WithTTL(0))
	var a, b bool
	app = appWith(claims("viewer", orgA), func(app *fiber.App) {
		app.Get("/", func(c fiber.Ctx) error {
			a = p.Can(c, "finance", "invoices.view")
			b = p2.Can(c, "finance", "invoices.view")
			return nil
		})
	})
	do(t, app, "GET", "/", nil)
	if !a || b {
		t.Fatalf("a=%v b=%v", a, b)
	}
}

func TestCrudCan(t *testing.T) {
	p, _ := httpPolicy(t)
	m := ActionCaps{
		Read:   []string{"invoices.view"},
		Create: []string{"invoices.create"},
		Update: []string{"invoices.edit"},
		Delete: []string{"invoices.delete"},
	}
	can := p.CrudCan("finance", m)
	m.Read[0] = "mutated" // хуулбар авсан эсэх

	eval := func(user string, a crud.Action) bool {
		var out bool
		app := appWith(claims(user, orgA), func(app *fiber.App) {
			app.Get("/", func(c fiber.Ctx) error { out = can(c, a); return nil })
		})
		do(t, app, "GET", "/", nil)
		return out
	}
	table := []struct {
		user string
		act  crud.Action
		want bool
	}{
		{"viewer", crud.ActionRead, true},
		{"viewer", crud.ActionCreate, false},
		{"acct", crud.ActionCreate, true},
		{"acct", crud.ActionUpdate, true},
		{"acct", crud.ActionDelete, false},
		{"boss", crud.ActionDelete, true},
		{"stranger", crud.ActionRead, false},
		{"boss", crud.Action("export"), false}, // үл мэдэгдэх үйлдэл
	}
	for _, c := range table {
		if got := eval(c.user, c.act); got != c.want {
			t.Errorf("%s %s: got %v want %v", c.user, c.act, got, c.want)
		}
	}
}

func TestCrudCan_MissingOrInvalidCaps_Deny(t *testing.T) {
	p, _ := httpPolicy(t)
	can := p.CrudCan("finance", ActionCaps{Read: []string{"invoices.view"}, Update: []string{"typo.cap"}})
	var rd, cr, up bool
	app := appWith(claims("boss", orgA), func(app *fiber.App) {
		app.Get("/", func(c fiber.Ctx) error {
			rd, cr, up = can(c, crud.ActionRead), can(c, crud.ActionCreate), can(c, crud.ActionUpdate)
			return nil
		})
	})
	do(t, app, "GET", "/", nil)
	if !rd || cr || up {
		t.Fatalf("read=%v create(хоосон)=%v update(буруу cap)=%v", rd, cr, up)
	}
	bad := p.CrudCan("ghost", ActionCaps{Read: []string{"a.b"}})
	app = appWith(claims("boss", orgA), func(app *fiber.App) {
		app.Get("/", func(c fiber.Ctx) error { rd = bad(c, crud.ActionRead); return nil })
	})
	do(t, app, "GET", "/", nil)
	if rd {
		t.Fatal("буруу модуль нэвтэрсэн")
	}
}

func TestMeHandler_ShapeMatchesFrontend(t *testing.T) {
	src := NewMemorySource()
	src.Set(orgA, "mgr", g(map[string]ModuleGrant{"finance": {Roles: []string{"viewer"}, Allow: []string{"extra.manage"}}}))
	src.Set(orgA, "boss", Grants{Owner: true})
	p, _ := newTestPolicy(t, src)
	reg := func(app *fiber.App) { app.Get("/me", p.MeHandler()); app.Get("/modules", p.ManifestHandler()) }

	// usePermission.ts: modules[key].role (string) + capabilities (string[]); is_owner.
	type modulePerm struct {
		Role         string   `json:"role"`
		Roles        []string `json:"roles"`
		Capabilities []string `json:"capabilities"`
	}
	var resp struct {
		IsOwner bool                  `json:"is_owner"`
		Modules map[string]modulePerm `json:"modules"`
	}
	st, body := do(t, appWith(claims("mgr", orgA), reg), "GET", "/me", nil)
	if st != 200 {
		t.Fatalf("%d %s", st, body)
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatal(err)
	}
	f := resp.Modules["finance"]
	if resp.IsOwner || f.Role != "viewer" || !contains(f.Capabilities, "invoices.view") || !contains(f.Capabilities, "extra.manage") {
		t.Fatalf("%s", body)
	}
	if _, has := resp.Modules["school"]; has {
		t.Fatal("эрхгүй модуль орсон")
	}

	st, body = do(t, appWith(claims("boss", orgA), reg), "GET", "/me", nil)
	_ = json.Unmarshal([]byte(body), &resp)
	if st != 200 || !resp.IsOwner || resp.Modules["school"].Role != "owner" || len(resp.Modules["school"].Capabilities) != 3 {
		t.Fatalf("%s", body)
	}

	// Эрхгүй: modules нь `{}` (null биш) — FE `Object.keys`-д аюулгүй.
	st, body = do(t, appWith(claims("nobody", orgA), reg), "GET", "/me", nil)
	if st != 200 || body != `{"is_owner":false,"modules":{}}` {
		t.Fatalf("%d %s", st, body)
	}

	if st, _ := do(t, appWith(nil, reg), "GET", "/me", nil); st != 401 {
		t.Fatalf("нэвтрээгүй /me: %d", st)
	}
	// Manifest: key/label/roles/capabilities[].default_roles (usePermission.ts ModuleManifest)
	st, body = do(t, appWith(claims("mgr", orgA), reg), "GET", "/modules", nil)
	var mans []Manifest
	if err := json.Unmarshal([]byte(body), &mans); err != nil || st != 200 || len(mans) != 2 || len(mans[0].Capabilities[0].DefaultRoles) == 0 {
		t.Fatalf("%d %s", st, body)
	}
	if st, _ := do(t, appWith(nil, reg), "GET", "/modules", nil); st != 401 {
		t.Fatalf("нэвтрээгүй /modules: %d", st)
	}
}

func TestMeHandler_NoStore_And_503(t *testing.T) {
	p, src := httpPolicy(t)
	app := appWith(claims("viewer", orgA), func(app *fiber.App) { app.Get("/me", p.MeHandler()) })
	req := httptest.NewRequest("GET", "/me", nil)
	resp, _ := app.Test(req)
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control=%q", resp.Header.Get("Cache-Control"))
	}
	src.SetError(errors.New("down"))
	p.Invalidate(req.Context(), orgA, "viewer")
	if st, _ := do(t, app, "GET", "/me", nil); st != 503 {
		t.Fatalf("%d", st)
	}
}

// Crud-тэй бодит интеграци: crud.Resource.Can-д CrudCan-г шууд өгч болох төрөл.
func TestCrudCan_AssignableToResource(t *testing.T) {
	p, _ := httpPolicy(t)
	r := crud.Resource[struct{}]{}
	r.Can = p.CrudCan("finance", ActionCaps{Read: []string{"invoices.view"}})
	if r.Can == nil {
		t.Fatal("assign")
	}
}
