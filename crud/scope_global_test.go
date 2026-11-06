package crud

import (
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/khan-vision/khan/lookup"
)

func (f *sfx) mountGlobal(t *testing.T, writes bool, mut func(*Resource[map[string]any])) *fiber.App {
	t.Helper()
	r := f.countryResource(writes)
	if mut != nil {
		mut(r)
	}
	return auditApp(t, "/countries", r)
}

func TestGlobalReadsForEveryOrg(t *testing.T) {
	f := newSfx(t)
	app := f.mountGlobal(t, false, nil)
	mn := f.country("Монгол", "MN")
	f.country("Япон", "JP")
	f.exec(`INSERT INTO `+f.tbl("country_city")+` (id, country_id, name) VALUES (?,?,?)`, newUUID(), mn, "Улаанбаатар")

	for _, org := range []string{f.orgA, f.orgB, f.orgS} {
		l := do(t, app, "GET", "/countries", org, roleRead, nil)
		mustStatus(t, l, 200)
		if l.JSON["total"] != float64(2) {
			t.Fatalf("%s: %s", org, l.Raw)
		}
		d := do(t, app, "GET", "/countries/"+mn, org, roleRead, nil)
		mustStatus(t, d, 200)
		if len(d.list("cities")) != 1 || d.JSON["org_id"] != nil {
			t.Fatalf("detail: %s", d.Raw)
		}
	}
	if q := do(t, app, "POST", "/countries/query", f.orgB, roleRead, map[string]any{"search": "Япон"}); q.JSON["total"] != float64(1) {
		t.Fatalf("query: %s", q.Raw)
	}
	// auth заавал: JWT-гүй 401; Can(read)-гүй 403
	for _, p := range []string{"/countries", "/countries/" + mn, "/countries/meta"} {
		if r := do(t, app, "GET", p, "", 0, nil); r.Status != 401 {
			t.Errorf("%s: %d", p, r.Status)
		}
		if r := do(t, app, "GET", p, f.orgA, 9, nil); r.Status != 403 {
			t.Errorf("%s role 9: %d", p, r.Status)
		}
	}
	// GlobalWrites=false → бичих route-ууд mount хийгдээгүй, DB өөрчлөгдөхгүй
	for _, c := range [][2]string{{"POST", "/countries"}, {"PUT", "/countries/" + mn}, {"DELETE", "/countries/" + mn}, {"POST", "/countries/bulk"}, {"POST", "/countries/system"}} {
		r := do(t, app, c[0], c[1], f.orgS, roleSys, map[string]any{"name": "x", "version": 1, "action": "delete", "ids": []string{mn}})
		if r.Status != 404 && r.Status != 405 {
			t.Errorf("%v: бичих route нээлттэй: %d %s", c, r.Status, r.Raw)
		}
	}
	if f.count(`SELECT count(*) FROM `+f.tbl("country")+` WHERE deleted_at IS NULL`) != 2 {
		t.Fatal("унших-л Global өөрчлөгдсөн")
	}
	// meta/can: бичих боломжгүй гэж илэрхийлнэ (Can sysadmin-д true байсан ч)
	m := do(t, app, "GET", "/countries/meta", f.orgS, roleSys, nil)
	can := m.JSON["can"].(map[string]any)
	if m.JSON["scope"] != "global" || m.JSON["global_writes"] != nil || can["read"] != true || can["create"] != false || can["update"] != false || can["delete"] != false {
		t.Fatalf("meta: %s", m.Raw)
	}
	if lc := do(t, app, "GET", "/countries", f.orgS, roleSys, nil).JSON["can"].(map[string]any); lc["create"] != false {
		t.Fatalf("list can: %v", lc)
	}
}

func TestGlobalWrites(t *testing.T) {
	f := newSfx(t)
	app := f.mountGlobal(t, true, nil)

	// Can(create) байхгүй (унших-л) → 403; JWT-гүй 401
	mustStatus(t, do(t, app, "POST", "/countries", f.orgA, roleRead, map[string]any{"name": "x"}), 403)
	mustStatus(t, do(t, app, "POST", "/countries", "", 0, map[string]any{"name": "x"}), 401)
	// sysadmin үүсгэнэ: org_id баганагүй хүснэгтэд INSERT амжилттай, children-тэй
	r := do(t, app, "POST", "/countries", f.orgS, roleSys, map[string]any{"name": "Монгол", "code": "MN", "pop": 3, "cities": []map[string]any{{"name": "УБ"}, {"name": "Дархан"}}})
	mustStatus(t, r, 201)
	id := r.str("id")
	if _, has := r.JSON["org_id"]; has && r.JSON["org_id"] != nil {
		t.Fatalf("org_id: %s", r.Raw)
	}
	if len(r.list("cities")) != 2 {
		t.Fatalf("cities: %s", r.Raw)
	}
	// бүх org шууд харна
	for _, org := range []string{f.orgA, f.orgB} {
		if d := do(t, app, "GET", "/countries/"+id, org, roleRead, nil); d.Status != 200 || len(d.list("cities")) != 2 {
			t.Fatalf("%s: %s", org, d.Raw)
		}
	}
	// unique нь глобал (аль ч org-оос)
	r2 := do(t, app, "POST", "/countries", f.orgS, roleSys, map[string]any{"name": "Монгол-2", "code": "MN"})
	mustStatus(t, r2, 422)
	// update (version, children): tenant админ ч Can-тай бол бичнэ — Can-ыг sysadmin-д л true болгох нь хэрэглэгчийн үүрэг
	cities := do(t, app, "GET", "/countries/"+id, f.orgS, roleSys, nil).list("cities")
	up := do(t, app, "PUT", "/countries/"+id, f.orgS, roleSys, map[string]any{"version": 1, "pop": 4,
		"cities": []map[string]any{{"id": cities[0]["id"], "name": "УБ-2"}, {"name": "Эрдэнэт"}}})
	mustStatus(t, up, 200)
	if up.JSON["pop"] != float64(4) || len(up.list("cities")) != 2 {
		t.Fatalf("update: %s", up.Raw)
	}
	mustStatus(t, do(t, app, "PUT", "/countries/"+id, f.orgS, roleSys, map[string]any{"version": 1, "pop": 5}), 409)
	// өөр org (Can-гүй) засч чадахгүй
	mustStatus(t, do(t, app, "PUT", "/countries/"+id, f.orgA, roleRead, map[string]any{"version": 2, "pop": 9}), 403)
	// bulk: org_id нөхцөлгүй (NoOrg)
	b := f.country("Япон", "JP")
	bk := do(t, app, "POST", "/countries/bulk", f.orgS, roleSys, map[string]any{"action": "update", "ids": []string{id, b}, "set": map[string]any{"pop": 7}})
	mustStatus(t, bk, 200)
	if bk.JSON["affected"] != float64(2) {
		t.Fatalf("bulk: %s", bk.Raw)
	}
	if f.count(`SELECT count(*) FROM `+f.tbl("country")+` WHERE pop = 7 AND version = 3`) != 1 { // id: v2→3; JP: v1→2
		t.Fatal("bulk version")
	}
	mustStatus(t, do(t, app, "POST", "/countries/bulk", f.orgA, roleRead, map[string]any{"action": "delete", "ids": []string{id}}), 403)
	// delete (soft)
	mustStatus(t, do(t, app, "DELETE", "/countries/"+id, f.orgS, roleSys, nil), 200)
	for _, org := range []string{f.orgA, f.orgS} {
		mustStatus(t, do(t, app, "GET", "/countries/"+id, org, roleRead, nil), 404)
	}
	bd := do(t, app, "POST", "/countries/bulk", f.orgS, roleSys, map[string]any{"action": "delete", "ids": []string{b}})
	if bd.JSON["affected"] != float64(1) {
		t.Fatalf("bulk delete: %s", bd.Raw)
	}
	m := do(t, app, "GET", "/countries/meta", f.orgS, roleSys, nil)
	if m.JSON["global_writes"] != true {
		t.Fatalf("meta: %s", m.Raw)
	}
}

func TestGlobalHardDeleteCascadesChildren(t *testing.T) {
	f := newSfx(t)
	app := f.mountGlobal(t, true, func(r *Resource[map[string]any]) {
		r.SoftDelete, r.Bulk, r.Children[0].SoftDelete = false, nil, false
	})
	f.exec(`ALTER TABLE ` + f.tbl("country") + ` DROP COLUMN deleted_at`)
	f.exec(`ALTER TABLE ` + f.tbl("country_city") + ` DROP COLUMN deleted_at`)
	r := do(t, app, "POST", "/countries", f.orgS, roleSys, map[string]any{"name": "Х", "cities": []map[string]any{{"name": "а"}, {"name": "б"}}})
	mustStatus(t, r, 201)
	other := f.country("Бусад", "OT")
	f.exec(`INSERT INTO `+f.tbl("country_city")+` (id, country_id, name) VALUES (?,?,?)`, newUUID(), other, "бусад хот")
	mustStatus(t, do(t, app, "DELETE", "/countries/"+r.str("id"), f.orgS, roleSys, nil), 200)
	if f.count(`SELECT count(*) FROM `+f.tbl("country")+` WHERE id = ?`, r.str("id")) != 0 ||
		f.count(`SELECT count(*) FROM `+f.tbl("country_city")+` WHERE country_id = ?`, r.str("id")) != 0 {
		t.Fatal("hard delete + cascade child")
	}
	if f.count(`SELECT count(*) FROM `+f.tbl("country_city")+` WHERE country_id = ?`, other) != 1 {
		t.Fatal("өөр эцгийн child устсан")
	}
}

func TestGlobalCacheSharedAcrossOrgs(t *testing.T) {
	f := newSfx(t)
	cc := newTestCache(t)
	app := f.mountGlobal(t, true, func(r *Resource[map[string]any]) { r.Cache = &CacheSpec{Cache: cc} })
	mn := f.country("Монгол", "MN")
	a := get(t, app, "/countries", f.orgA, roleRead)
	b := get(t, app, "/countries", f.orgB, roleRead)
	if a.cache() != "miss" || b.cache() != "hit" || a.etag() != b.etag() || a.etag() == "" {
		t.Fatalf("global cache org-оор хуваагдсан: %q %q %q %q", a.cache(), b.cache(), a.etag(), b.etag())
	}
	if r := doH(t, app, "GET", "/countries", f.orgB, roleRead, nil, map[string]string{"If-None-Match": a.etag()}); r.Status != 304 {
		t.Fatalf("304: %d", r.Status)
	}
	d1, d2 := get(t, app, "/countries/"+mn, f.orgA, roleRead), get(t, app, "/countries/"+mn, f.orgB, roleRead)
	if d1.cache() != "miss" || d2.cache() != "hit" {
		t.Fatalf("detail: %q %q", d1.cache(), d2.cache())
	}
	// `can` (эрх) cache-д орохгүй: org бүр өөрийн эрхийг авна
	if c := get(t, app, "/countries", f.orgB, roleSys); c.JSON["can"].(map[string]any)["create"] != true {
		t.Fatalf("can: %s", c.Raw)
	}
	// ямар ч org-ийн бичилт бүх org-ийн cache-ийг хүчингүй болгоно
	mustStatus(t, do(t, app, "POST", "/countries", f.orgS, roleSys, map[string]any{"name": "Япон", "code": "JP"}), 201)
	for _, org := range []string{f.orgA, f.orgB} {
		l := get(t, app, "/countries", org, roleRead)
		if !strings.Contains(string(l.Raw), "Япон") {
			t.Fatalf("%s: хуучирсан cache: %s", org, l.Raw)
		}
	}
	if get(t, app, "/countries", f.orgA, roleRead).cache() != "hit" {
		t.Fatal("хоёр дахь уншилт hit биш")
	}
	// options cache (Global лавлах руу Tenant: off) ажиллана
	up := do(t, app, "PUT", "/countries/"+mn, f.orgS, roleSys, map[string]any{"version": 1, "name": "Монгол Улс"})
	mustStatus(t, up, 200)
	if d := get(t, app, "/countries/"+mn, f.orgA, roleRead); d.cache() != "miss" || d.JSON["name"] != "Монгол Улс" {
		t.Fatalf("update-ийн дараа: %q %s", d.cache(), d.Raw)
	}
}

func TestGlobalEventsAndAudit(t *testing.T) {
	f := newSfx(t)
	app := f.mountGlobal(t, true, func(r *Resource[map[string]any]) { r.Audit = &AuditSpec{Enabled: true} })
	r := do(t, app, "POST", "/countries", f.orgS, roleSys, map[string]any{"name": "Монгол"})
	mustStatus(t, r, 201)
	id := r.str("id")
	mustStatus(t, do(t, app, "PUT", "/countries/"+id, f.orgS, roleSys, map[string]any{"version": 1, "name": "Монгол-2"}), 200)
	// event: OrgID = actor org, payload-д org_id алга, scope=global
	if f.count(`SELECT count(*) FROM khan.outbox_events WHERE topic = 'test.country.created' AND org_id = ? AND aggregate_id = ?
		AND payload->>'scope' = 'global' AND NOT payload ? 'org_id'`, f.orgS, id) != 1 {
		t.Fatal("global event")
	}
	// audit: actor org-д бичигдэнэ
	if f.count(`SELECT count(*) FROM khan.audit_log WHERE resource = 'test.country' AND record_id = ? AND org_id = ?`, id, f.orgS) != 2 {
		t.Fatal("global audit")
	}
	// history: өөр org түүхийг харахгүй (200, хоосон); sysadmin-ий org өөрийнхөө
	h := do(t, app, "GET", "/countries/"+id+"/history", f.orgA, roleRead, nil)
	mustStatus(t, h, 200)
	if len(h.list("data")) != 0 {
		t.Fatalf("өөр org түүх харлаа: %s", h.Raw)
	}
	if h := do(t, app, "GET", "/countries/"+id+"/history", f.orgS, roleSys, nil); len(h.list("data")) != 2 {
		t.Fatalf("өөрийн түүх: %s", h.Raw)
	}
}

func TestGlobalMountValidation(t *testing.T) {
	f := newSfx(t)
	cc := newTestCache(t)
	cases := map[string]func(r *Resource[map[string]any]){
		"RLS":                   func(r *Resource[map[string]any]) { r.RLS = true },
		"SystemCan":             func(r *Resource[map[string]any]) { r.SystemCan = sysCan },
		"DependsOn":             func(r *Resource[map[string]any]) { r.Cache = &CacheSpec{Cache: cc, DependsOn: []string{"x.y"}} },
		"Bulk GlobalWrites-гүй": func(r *Resource[map[string]any]) { r.GlobalWrites = false },
		"tenant Ref": func(r *Resource[map[string]any]) {
			r.Fields = append(r.Fields, Field{Name: "owner_id", Kind: "uuid", Ref: &lookup.Spec{Table: f.tbl("owner"), Label: []string{"name"}}})
		},
		"shared Ref": func(r *Resource[map[string]any]) {
			r.Fields = append(r.Fields, Field{Name: "grp_id", Kind: "uuid", Ref: &lookup.Spec{Table: f.tbl("grp"), Label: []string{"name"}, Shared: true}})
		},
		"child tenant Ref": func(r *Resource[map[string]any]) {
			r.Children[0].Fields = append(r.Children[0].Fields, Field{Name: "owner_id", Kind: "uuid", Ref: &lookup.Spec{Table: f.tbl("owner"), Label: []string{"name"}}})
		},
	}
	for name, mut := range cases {
		r := f.countryResource(true)
		mut(r)
		if err := r.Check(); err == nil {
			t.Errorf("%s: Check алдаагүй", name)
		}
		if err := r.Mount(fiber.New().Group("/x")); err == nil {
			t.Errorf("%s: Mount алдаагүй", name)
		}
	}
	// Global лавлах (TenantOff) ref зөвшөөрөгдөнө; GlobalWrites=false + Bulk-гүй зөвшөөрөгдөнө
	ok := f.countryResource(false)
	ok.Fields = append(ok.Fields, Field{Name: "grp_id", Kind: "uuid", Rules: ok.Fields[0].Rules, Ref: &lookup.Spec{Table: f.tbl("country"), Label: []string{"name"}, Tenant: lookup.TenantOff()}})
	ok.Fields[len(ok.Fields)-1].Rules.Required = false
	ok.Fields[len(ok.Fields)-1].Rules.Nullable = true
	if err := ok.Check(); err != nil {
		t.Errorf("Global + TenantOff ref: %v", err)
	}
	if err := ok.Mount(fiber.New().Group("/x")); err != nil {
		t.Errorf("Mount: %v", err)
	}
	// GlobalWrites tenant-д алдаа
	tr := f.kindResource()
	tr.Scope, tr.SystemCan, tr.GlobalWrites = ScopeTenant, nil, true
	if err := tr.Check(); err == nil {
		t.Error("tenant + GlobalWrites алдаагүй")
	}
}
