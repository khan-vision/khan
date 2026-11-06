package crud

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/khan-vision/khan/audit"
	"github.com/khan-vision/khan/auth"
	"github.com/khan-vision/khan/validate"
)

// doNoOrg — org_id claim-гүй token (X-T-NoOrg) бүхий хүсэлт; role = JWT role.
func doNoOrg(t *testing.T, app *fiber.App, method, path string, role int, body any) resp {
	t.Helper()
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rd = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, rd)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-T-NoOrg", "1")
	req.Header.Set("X-T-Role", fmt.Sprint(role))
	res, err := app.Test(req, fiber.TestConfig{Timeout: 0})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(res.Body)
	out := resp{Status: res.StatusCode, Raw: raw}
	out.JSON = map[string]any{}
	_ = json.Unmarshal(raw, &out.JSON)
	return out
}

// sysApp — kind (Shared) + country (Global) + owner (Tenant) нэг app-д, SystemPrincipal = role 4.
func (f *sfx) sysApp(t *testing.T) *fiber.App {
	t.Helper()
	kind := f.kindResource()
	kind.Name = fmt.Sprintf("test.nokind_%d", time.Now().UnixNano()%1000000000)
	kind.SystemPrincipal = auth.RoleIs(roleSys)
	kind.Audit = &AuditSpec{Enabled: true}
	country := f.countryResource(true)
	country.Name = kind.Name + "c"
	country.SystemPrincipal = auth.RoleIs(roleSys)
	owner := &Resource[map[string]any]{
		Name: kind.Name + "o", Table: f.tbl("owner"),
		Fields: []Field{{Name: "name", Label: "Нэр", Kind: validate.String, Rules: validate.Rules{Required: true}}},
		Can:    scopeCan, DB: kind.DB,
	}
	t.Cleanup(func() {
		_, _ = audit.Purge(context.Background(), f.db, time.Now(), audit.AllOrgs(), audit.ForResource(kind.Name))
		f.db.Exec("DELETE FROM khan.outbox_events WHERE topic LIKE ?", kind.Name+"%")
	})
	app := fiber.New()
	for p, mount := range map[string]func(fiber.Router) error{
		"/kinds":     func(g fiber.Router) error { return kind.Mount(g) },
		"/countries": func(g fiber.Router) error { return country.Mount(g) },
		"/owners":    func(g fiber.Router) error { return owner.Mount(g) },
	} {
		if err := mount(app.Group(p, claimsMW)); err != nil {
			t.Fatalf("Mount %s: %v", p, err)
		}
	}
	return app
}

func TestSystemPrincipalWithoutOrg(t *testing.T) {
	f := newSfx(t)
	app := f.sysApp(t)
	tenantRow := f.kindRow(f.orgA, "А-гийн", "A1")
	sysRowSeed := f.kindRow("", "Системийн", "S0")

	// Global: org-гүй sysadmin CRUD
	c := doNoOrg(t, app, "POST", "/countries", roleSys, map[string]any{"name": "Монгол"})
	mustStatus(t, c, 201)
	cid := c.str("id")
	mustStatus(t, doNoOrg(t, app, "GET", "/countries", roleSys, nil), 200)
	mustStatus(t, doNoOrg(t, app, "PUT", "/countries/"+cid, roleSys, map[string]any{"version": 1, "name": "Монгол-2"}), 200)
	mustStatus(t, doNoOrg(t, app, "DELETE", "/countries/"+cid, roleSys, nil), 200)

	// Shared: /system бичилт (org=NULL), унших нь зөвхөн системийн мөр
	s := doNoOrg(t, app, "POST", "/kinds/system", roleSys, map[string]any{"name": "Систем-2", "code": "S1"})
	mustStatus(t, s, 201)
	sid := s.str("id")
	l := doNoOrg(t, app, "GET", "/kinds", roleSys, nil)
	mustStatus(t, l, 200)
	if got := ids(l); !got[sid] || !got[sysRowSeed] || got[tenantRow] {
		t.Fatalf("org-гүй sysadmin зөвхөн системийн мөр харах ёстой: %s", l.Raw)
	}
	mustStatus(t, doNoOrg(t, app, "PUT", "/kinds/"+sid, roleSys, map[string]any{"version": 1, "name": "Систем-3"}), 200)
	if r := doNoOrg(t, app, "GET", "/kinds/"+tenantRow, roleSys, nil); r.Status != 404 {
		t.Fatalf("tenant мөр org-гүй принсипалд 404 байх ёстой: %d", r.Status)
	}
	if r := doNoOrg(t, app, "PUT", "/kinds/"+tenantRow, roleSys, map[string]any{"version": 1, "name": "x"}); r.Status != 404 {
		t.Fatalf("tenant мөр засах: %d", r.Status)
	}
	// /history: org-гүй actor-ийн түүх
	h := doNoOrg(t, app, "GET", "/kinds/"+sid+"/history", roleSys, nil)
	mustStatus(t, h, 200)
	if len(h.list("data")) < 2 {
		t.Fatalf("history: %s", h.Raw)
	}
	mustStatus(t, doNoOrg(t, app, "DELETE", "/kinds/"+sid, roleSys, nil), 200)
	// энгийн мөр үүсгэх = org заавал
	if r := doNoOrg(t, app, "POST", "/kinds", roleSys, map[string]any{"name": "x"}); r.Status != 401 {
		t.Fatalf("org-гүй энгийн мөр: %d %s", r.Status, r.Raw)
	}
	// Tenant resource: org-гүй sysadmin ч 401
	if r := doNoOrg(t, app, "GET", "/owners", roleSys, nil); r.Status != 401 {
		t.Fatalf("tenant resource: %d", r.Status)
	}
	// event org_id NULL, audit org_id NULL
	if f.count(`SELECT count(*) FROM khan.outbox_events WHERE topic LIKE 'test.nokind_%.created' AND org_id IS NULL`) < 2 {
		t.Fatal("org-гүй event NULL org-той байх ёстой")
	}
	if f.count(`SELECT count(*) FROM khan.audit_log WHERE resource LIKE 'test.nokind_%' AND org_id IS NULL`) < 3 {
		t.Fatal("org-гүй audit org_id NULL байх ёстой")
	}
}

func TestNonSystemPrincipalWithoutOrg401(t *testing.T) {
	f := newSfx(t)
	app := f.sysApp(t)
	for _, role := range []int{roleAdmin, roleRead, 6} {
		for _, c := range [][2]string{
			{"GET", "/countries"}, {"POST", "/countries"}, {"GET", "/kinds"}, {"POST", "/kinds/system"},
			{"POST", "/kinds"}, {"GET", "/owners"}, {"GET", "/kinds/meta"},
		} {
			if r := doNoOrg(t, app, c[0], c[1], role, map[string]any{"name": "x"}); r.Status != 401 {
				t.Errorf("role %d %v: %d (401 хүлээсэн)", role, c, r.Status)
			}
		}
	}
}

func TestSystemPrincipalTenantMisconfig(t *testing.T) {
	f := newSfx(t)
	r := f.kindResource()
	r.Scope, r.SystemCan = ScopeTenant, nil
	r.SystemPrincipal = auth.RoleIs(roleSys)
	if err := r.Check(); err == nil {
		t.Fatal("ScopeTenant + SystemPrincipal Check алдаа өгөх ёстой")
	}
}

func TestSingleTenantTemplate(t *testing.T) {
	f := newSfx(t)
	r := f.countryResource(false)
	r.SingleTenant()
	app := auditApp(t, "/countries", r)
	// org_id-гүй ердийн хэрэглэгч (role 1) бүрэн CRUD
	c := doNoOrg(t, app, "POST", "/countries", roleAdmin, map[string]any{"name": "Монгол"})
	mustStatus(t, c, 201)
	mustStatus(t, doNoOrg(t, app, "GET", "/countries", roleRead, nil), 200)
	if r := doNoOrg(t, app, "GET", "/countries", 9, nil); r.Status != 403 {
		t.Fatalf("Can хэвээр хамаарна: %d", r.Status)
	}
	// JWT огт байхгүй → 401
	if r := do(t, app, "GET", "/countries", "", 0, nil); r.Status != 401 {
		t.Fatalf("token-гүй: %d", r.Status)
	}
	f.exec(`DELETE FROM ` + f.tbl("country"))
}

// Tenant resource нь Shared/Global dependency-ийн СИСТЕМ/ГЛОБАЛ бичилтэд хүчингүй болно.
func TestCacheDependsOnSystemAndGlobalWrites(t *testing.T) {
	f := newSfx(t)
	c := newTestCache(t)
	kind := f.kindResource()
	kind.Cache = &CacheSpec{Cache: c, BumpOnly: true}
	country := f.countryResource(true)
	country.Cache = &CacheSpec{Cache: c, BumpOnly: true}
	owner := &Resource[map[string]any]{
		Name: "test.owner", Table: f.tbl("owner"), Endpoint: "/owners",
		Fields: []Field{{Name: "name", Label: "Нэр", Kind: validate.String, Rules: validate.Rules{Required: true}}},
		Cache:  &CacheSpec{Cache: c, DependsOn: []string{"test.kind", "test.country"}},
		Can:    scopeCan, DB: kind.DB,
	}
	app := fiber.New()
	for p, m := range map[string]func(fiber.Router) error{
		"/kinds": func(g fiber.Router) error { return kind.Mount(g) }, "/countries": func(g fiber.Router) error { return country.Mount(g) },
		"/owners": func(g fiber.Router) error { return owner.Mount(g) },
	} {
		if err := m(app.Group(p, claimsMW)); err != nil {
			t.Fatal(err)
		}
	}
	f.owner(f.orgA, "O1")
	get := func() string {
		req := httptest.NewRequest("GET", "/owners", nil)
		req.Header.Set("X-T-Org", f.orgA)
		req.Header.Set("X-T-Role", fmt.Sprint(roleRead))
		res, err := app.Test(req, fiber.TestConfig{Timeout: 0})
		if err != nil || res.StatusCode != 200 {
			t.Fatalf("GET /owners: %v %d", err, res.StatusCode)
		}
		return res.Header.Get(hdrCache)
	}
	expect := func(step, want string) {
		t.Helper()
		if got := get(); got != want {
			t.Fatalf("%s: cache=%q, хүлээсэн %q", step, got, want)
		}
	}
	expect("1-р уншилт", "miss")
	expect("2-р уншилт", "hit")
	// системийн мөрийн бичилт (өөр org-оос): tenant dependent хүчингүй болно
	mustStatus(t, do(t, app, "POST", "/kinds/system", f.orgS, roleSys, map[string]any{"name": "Систем"}), 201)
	expect("system write дараа", "miss")
	expect("system write дараа 2", "hit")
	// глобал мөрийн бичилт
	mustStatus(t, do(t, app, "POST", "/countries", f.orgS, roleSys, map[string]any{"name": "Монгол"}), 201)
	expect("global write дараа", "miss")
	expect("global write дараа 2", "hit")
	// өөр org-ийн энгийн Shared бичилт A-гийн dependent-ийг хүчингүй болгохгүй (өөрчлөлтгүй зан үйл)
	mustStatus(t, do(t, app, "POST", "/kinds", f.orgB, roleAdmin, map[string]any{"name": "Б"}), 201)
	expect("бусад org-ийн бичилт", "hit")
	f.exec(`DELETE FROM ` + f.tbl("country"))
}

// JWT-ийн org том үсэгтэй байсан ч өөрийн Shared мөрөө засна/устгана (uuid харьцуулалт том/жижиг үсэг үл ялгана).
func TestSharedOwnershipCaseInsensitive(t *testing.T) {
	f := newSfx(t)
	app := f.mountShared(t, nil)
	up := strings.ToUpper(f.orgA)
	r := do(t, app, "POST", "/kinds", up, roleAdmin, map[string]any{"name": "А"})
	mustStatus(t, r, 201)
	id := r.str("id")
	mustStatus(t, do(t, app, "PUT", "/kinds/"+id, up, roleAdmin, map[string]any{"version": 1, "name": "А2"}), 200)
	mustStatus(t, do(t, app, "POST", "/kinds/bulk", up, roleAdmin, map[string]any{"action": "update", "ids": []string{id}, "set": map[string]any{"note": "x"}}), 200)
	mustStatus(t, do(t, app, "DELETE", "/kinds/"+id, up, roleAdmin, nil), 200)
	// rowOwner шууд: том/жижиг үсэг ялгахгүй
	e, err := f.kindResource().build(false)
	if err != nil {
		t.Fatal(err)
	}
	if o, err := e.rowOwner(f.orgA, map[string]any{"org_id": up}); err != nil || o.system {
		t.Fatalf("rowOwner: %v %+v", err, o)
	}
}

// Tenant-ийн утга системийн мөртэй давхцвал мессеж «системд нөөцлөгдсөн» (өөр org-ийн мөрийг ил гаргахгүй).
func TestSharedUniqueReservedBySystem(t *testing.T) {
	f := newSfx(t)
	app := f.mountShared(t, nil)
	f.kindRow("", "Систем", "RES")
	f.kindRow(f.orgB, "Б", "BBB")
	r := do(t, app, "POST", "/kinds", f.orgA, roleAdmin, map[string]any{"name": "А", "code": "RES"})
	mustStatus(t, r, 422)
	if msg := strings.Join(r.errs()["code"], " "); !strings.Contains(msg, "системд нөөцлөгдсөн") {
		t.Fatalf("мессеж: %v", r.errs())
	}
	// өөр org-ийн утга: ердийн «давхардсан» биш, 201 (A-д харагдахгүй)
	mustStatus(t, do(t, app, "POST", "/kinds", f.orgA, roleAdmin, map[string]any{"name": "А", "code": "BBB"}), 201)
	// өөрийн мөртэй давхцвал «давхардсан»
	d := do(t, app, "POST", "/kinds", f.orgA, roleAdmin, map[string]any{"name": "А2", "code": "BBB"})
	mustStatus(t, d, 422)
	if msg := strings.Join(d.errs()["code"], " "); !strings.Contains(msg, "давхардсан") {
		t.Fatalf("мессеж: %v", d.errs())
	}
}
