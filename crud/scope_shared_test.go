package crud

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/khan-vision/khan/config"
	"github.com/khan-vision/khan/db"
	"github.com/khan-vision/khan/tenant"
	"github.com/khan-vision/khan/testkit"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func (f *sfx) mountShared(t *testing.T, mut func(*Resource[map[string]any])) *fiber.App {
	t.Helper()
	r := f.kindResource()
	if mut != nil {
		mut(r)
	}
	return auditApp(t, "/kinds", r)
}

func ids(r resp) map[string]bool {
	out := map[string]bool{}
	for _, row := range r.list("data") {
		out[row["id"].(string)] = true
	}
	return out
}

func TestSharedReadVisibility(t *testing.T) {
	f := newSfx(t)
	app := f.mountShared(t, nil)
	sys := f.kindRow("", "Систем", "S")
	a := f.kindRow(f.orgA, "А-гийн", "A")
	b := f.kindRow(f.orgB, "Б-гийн", "B")
	f.kindItem("", sys, "системийн зүйл")
	f.kindItem(f.orgA, a, "а зүйл")

	la := do(t, app, "GET", "/kinds", f.orgA, roleRead, nil)
	mustStatus(t, la, 200)
	if got := ids(la); !got[sys] || !got[a] || got[b] || la.JSON["total"] != float64(2) {
		t.Fatalf("A: %s", la.Raw)
	}
	lb := do(t, app, "GET", "/kinds", f.orgB, roleRead, nil)
	if got := ids(lb); !got[sys] || !got[b] || got[a] || lb.JSON["total"] != float64(2) {
		t.Fatalf("B: %s", lb.Raw)
	}
	// POST /query, search, totals — системийн мөр хаа ч орно, бусдын мөр хаана ч орохгүй
	q := do(t, app, "POST", "/kinds/query", f.orgA, roleRead, map[string]any{"search": "гийн"})
	if q.JSON["total"] != float64(1) {
		t.Fatalf("search: %s", q.Raw)
	}
	if x := do(t, app, "GET", "/kinds?search=Б-гийн", f.orgA, roleRead, nil); x.JSON["total"] != float64(0) {
		t.Fatalf("B-ийн мөр A-д хайлтаар гарлаа: %s", x.Raw)
	}
	// detail: системийн мөр хоёуланд нь + children; org_id=null (FE read-only гэж таних)
	for _, org := range []string{f.orgA, f.orgB} {
		d := do(t, app, "GET", "/kinds/"+sys, org, roleRead, nil)
		mustStatus(t, d, 200)
		if d.JSON["org_id"] != nil || len(d.list("items")) != 1 {
			t.Fatalf("системийн detail: %s", d.Raw)
		}
	}
	// өөр org-ийн мөр = 404 (байхгүйтэй ялгагдахгүй)
	x := do(t, app, "GET", "/kinds/"+a, f.orgB, roleRead, nil)
	y := do(t, app, "GET", "/kinds/"+newUUID(), f.orgB, roleRead, nil)
	if x.Status != 404 || y.Status != 404 || string(x.Raw) != string(y.Raw) {
		t.Fatalf("өөр org: %s ; байхгүй: %s", x.Raw, y.Raw)
	}
	// A-ийн мөрийн children B-д харагдахгүй
	if d := do(t, app, "GET", "/kinds/"+a, f.orgA, roleRead, nil); len(d.list("items")) != 1 {
		t.Fatalf("A items: %s", d.Raw)
	}
}

func TestSharedOptionsIncludeSystemRows(t *testing.T) {
	f := newSfx(t)
	app := f.mountShared(t, nil)
	gs, ga, gb := f.grp("", "Системийн бүлэг"), f.grp(f.orgA, "А бүлэг"), f.grp(f.orgB, "Б бүлэг")
	r := do(t, app, "GET", "/kinds/options/group_id", f.orgA, roleRead, nil)
	mustStatus(t, r, 200)
	got := map[string]bool{}
	for _, o := range r.list("data") {
		got[o["value"].(string)] = true
	}
	if !got[gs] || !got[ga] || got[gb] || len(got) != 2 {
		t.Fatalf("options: %s", r.Raw)
	}
	// create: өөрийн + системийн бүлэг зөвшөөрөгдөнө, бусдынх 422
	ok := do(t, app, "POST", "/kinds", f.orgA, roleAdmin, map[string]any{"name": "x", "group_id": gs})
	mustStatus(t, ok, 201)
	ok = do(t, app, "POST", "/kinds", f.orgA, roleAdmin, map[string]any{"name": "y", "group_id": ga})
	mustStatus(t, ok, 201)
	bad := do(t, app, "POST", "/kinds", f.orgA, roleAdmin, map[string]any{"name": "z", "group_id": gb})
	mustStatus(t, bad, 422)
	// tenant-ийн лавлах (owner) өөр org-ийнх 422
	ob := f.owner(f.orgB, "Б эзэн")
	mustStatus(t, do(t, app, "POST", "/kinds", f.orgA, roleAdmin, map[string]any{"name": "w", "owner_id": ob}), 422)
}

func TestSharedSystemRowsReadOnlyForTenants(t *testing.T) {
	f := newSfx(t)
	app := f.mountShared(t, nil)
	sys := f.kindRow("", "Систем", "S")
	itemID := f.kindItem("", sys, "зүйл")
	a := f.kindRow(f.orgA, "А", "A")

	forbid := func(label, method, path string, org string, role int, body any) {
		t.Helper()
		r := do(t, app, method, path, org, role, body)
		if r.Status != 403 || r.str("error") != "Системийн мөрийг өөрчлөх боломжгүй" {
			t.Errorf("%s: %d %s", label, r.Status, r.Raw)
		}
	}
	for _, org := range []string{f.orgA, f.orgB} {
		forbid("PUT", "PUT", "/kinds/"+sys, org, roleAdmin, map[string]any{"name": "hack", "version": 1})
		forbid("PUT items", "PUT", "/kinds/"+sys, org, roleAdmin, map[string]any{"version": 1, "items": []map[string]any{{"label": "x"}}})
		forbid("DELETE", "DELETE", "/kinds/"+sys, org, roleAdmin, nil)
		forbid("bulk update", "POST", "/kinds/bulk", org, roleAdmin, map[string]any{"action": "update", "ids": []string{sys}, "set": map[string]any{"note": "h"}})
		forbid("bulk delete", "POST", "/kinds/bulk", org, roleAdmin, map[string]any{"action": "delete", "ids": []string{sys}})
		forbid("POST /system", "POST", "/kinds/system", org, roleAdmin, map[string]any{"name": "x"})
	}
	// олон id-тай bulk: системийн мөр нэг байхад БҮТЭН хүсэлт 403, өөрийн мөр ч өөрчлөгдөхгүй (атомик)
	forbid("bulk mixed", "POST", "/kinds/bulk", f.orgA, roleAdmin, map[string]any{"action": "update", "ids": []string{a, sys}, "set": map[string]any{"note": "mixed"}})
	forbid("bulk mixed delete", "POST", "/kinds/bulk", f.orgA, roleAdmin, map[string]any{"action": "delete", "ids": []string{sys, a}})
	if f.count(`SELECT count(*) FROM `+f.tbl("kind")+` WHERE note IS NOT NULL OR deleted_at IS NOT NULL OR name NOT IN ('Систем','А')`) != 0 {
		t.Fatal("системийн/өөрийн мөр өөрчлөгдсөн")
	}
	if f.count(`SELECT count(*) FROM `+f.tbl("kind_item")+` WHERE id = ? AND deleted_at IS NULL AND label = 'зүйл' AND org_id IS NULL`, itemID) != 1 {
		t.Fatal("системийн child өөрчлөгдсөн")
	}
	// хоёр дахь түвшний хамгаалалт: 403 нь Can-аас өмнө биш — Can байхгүй бол ердийн 403
	if r := do(t, app, "PUT", "/kinds/"+sys, f.orgA, roleRead, map[string]any{"name": "x", "version": 1}); r.Status != 403 || r.str("error") != "Хандах эрхгүй байна" {
		t.Errorf("Can=false: %s", r.Raw)
	}
	// өөрийн мөр ердийнхөөрөө бичигдэнэ
	mustStatus(t, do(t, app, "PUT", "/kinds/"+a, f.orgA, roleAdmin, map[string]any{"name": "А2", "version": 1}), 200)
	bk := do(t, app, "POST", "/kinds/bulk", f.orgA, roleAdmin, map[string]any{"action": "update", "ids": []string{a}, "set": map[string]any{"note": "ok"}})
	mustStatus(t, bk, 200)
	if bk.JSON["affected"] != float64(1) {
		t.Fatalf("bulk: %s", bk.Raw)
	}
	// өөр org-ийн мөр: bulk-д чимээгүй 0, PUT/DELETE 404
	bb := f.kindRow(f.orgB, "Б", "B")
	bk = do(t, app, "POST", "/kinds/bulk", f.orgA, roleAdmin, map[string]any{"action": "delete", "ids": []string{bb}})
	mustStatus(t, bk, 200)
	if bk.JSON["affected"] != float64(0) {
		t.Fatalf("bulk өөр org: %s", bk.Raw)
	}
	mustStatus(t, do(t, app, "PUT", "/kinds/"+bb, f.orgA, roleAdmin, map[string]any{"name": "h", "version": 1}), 404)
	mustStatus(t, do(t, app, "DELETE", "/kinds/"+bb, f.orgA, roleAdmin, nil), 404)
}

func TestSharedCreateAlwaysOwnOrg(t *testing.T) {
	f := newSfx(t)
	app := f.mountShared(t, nil)
	// body org_id / system / үл мэдэгдэх түлхүүр org-ийг өөрчилж чадахгүй
	r := do(t, app, "POST", "/kinds", f.orgA, roleAdmin, map[string]any{"name": "x", "org_id": nil, "system": true, "_system": true})
	mustStatus(t, r, 201)
	if f.count(`SELECT count(*) FROM `+f.tbl("kind")+` WHERE id = ? AND org_id = ?`, r.str("id"), f.orgA) != 1 {
		t.Fatal("create org_id = JWT org биш")
	}
	if r.JSON["org_id"] != f.orgA {
		t.Fatalf("detail org_id: %s", r.Raw)
	}
	// sysadmin ч POST / үргэлжлүүлэн өөрийн org-д үүсгэнэ
	r = do(t, app, "POST", "/kinds", f.orgS, roleSys, map[string]any{"name": "sys-own"})
	mustStatus(t, r, 201)
	if f.count(`SELECT count(*) FROM `+f.tbl("kind")+` WHERE id = ? AND org_id = ?`, r.str("id"), f.orgS) != 1 {
		t.Fatal("sysadmin POST / системийн мөр үүсгэсэн")
	}
}

func TestSharedSystemWritesWithSystemCan(t *testing.T) {
	f := newSfx(t)
	app := f.mountShared(t, func(r *Resource[map[string]any]) {
		r.Hooks.BeforeCreate = func(x *Ctx, v Values) error {
			if x.System {
				v["note"] = "hook:system"
			}
			return nil
		}
		r.Hooks.AfterUpdate = func(x *Ctx, v Values) error {
			if x.System && x.OrgID == "" {
				return Forbidden("OrgID хоосон")
			}
			return nil
		}
		r.Bulk = nil // Update hook-тэй resource-д Bulk.Fields зөвшөөрөгдөхгүй
	})
	grpID := f.grp("", "Системийн бүлэг")
	country := f.country("Монгол", "MN")
	ownerA := f.owner(f.orgA, "А эзэн")

	// POST /system: sysadmin → org_id NULL, children org_id NULL
	r := do(t, app, "POST", "/kinds/system", f.orgS, roleSys, map[string]any{
		"name": "Системийн", "code": "SYS", "country_id": country, "items": []map[string]any{{"label": "i1"}, {"label": "i2"}}})
	mustStatus(t, r, 201)
	id := r.str("id")
	if r.JSON["org_id"] != nil || r.JSON["note"] != "hook:system" {
		t.Fatalf("system create: %s", r.Raw)
	}
	if f.count(`SELECT count(*) FROM `+f.tbl("kind")+` WHERE id = ? AND org_id IS NULL`, id) != 1 ||
		f.count(`SELECT count(*) FROM `+f.tbl("kind_item")+` WHERE kind_id = ? AND org_id IS NULL`, id) != 2 {
		t.Fatal("системийн мөр/child org_id NULL биш")
	}
	// sysadmin биш (Can байгаа ч) 403; Can байхгүй 403
	mustStatus(t, do(t, app, "POST", "/kinds/system", f.orgA, roleAdmin, map[string]any{"name": "x"}), 403)
	mustStatus(t, do(t, app, "POST", "/kinds/system", f.orgA, roleRead, map[string]any{"name": "x"}), 403)
	// системийн мөр org-ийн лавлахад (tenant/Shared) холбогдохгүй; Global лавлах (TenantOff) болно
	bad := do(t, app, "POST", "/kinds/system", f.orgS, roleSys, map[string]any{"name": "b", "owner_id": ownerA})
	mustStatus(t, bad, 422)
	bad = do(t, app, "POST", "/kinds/system", f.orgS, roleSys, map[string]any{"name": "b", "group_id": grpID})
	mustStatus(t, bad, 422)
	if !strings.Contains(string(bad.Raw), "системийн мөрөнд") {
		t.Fatalf("мессеж: %s", bad.Raw)
	}
	// Унших: бүх org-д харагдана
	for _, org := range []string{f.orgA, f.orgB} {
		if d := do(t, app, "GET", "/kinds/"+id, org, roleRead, nil); d.Status != 200 || len(d.list("items")) != 2 {
			t.Fatalf("%s: %s", org, d.Raw)
		}
	}
	// PUT: sysadmin засна (children бүрэн жагсаалт, org NULL хэвээр)
	d := do(t, app, "GET", "/kinds/"+id, f.orgS, roleSys, nil)
	items := d.list("items")
	up := do(t, app, "PUT", "/kinds/"+id, f.orgS, roleSys, map[string]any{
		"version": 1, "name": "Системийн-2",
		"items": []map[string]any{{"id": items[0]["id"], "label": "i1-new"}, {"label": "i3"}}})
	mustStatus(t, up, 200)
	if up.JSON["name"] != "Системийн-2" || len(up.list("items")) != 2 || up.JSON["org_id"] != nil {
		t.Fatalf("update: %s", up.Raw)
	}
	if f.count(`SELECT count(*) FROM `+f.tbl("kind_item")+` WHERE kind_id = ? AND org_id IS NULL AND deleted_at IS NULL`, id) != 2 {
		t.Fatal("child org_id NULL хэвээр биш")
	}
	// sysadmin өөр org-ийн мөрийг засч чадахгүй (404) — зөвхөн системийн + өөрийн org
	other := f.kindRow(f.orgA, "А", "A")
	mustStatus(t, do(t, app, "PUT", "/kinds/"+other, f.orgS, roleSys, map[string]any{"name": "h", "version": 1}), 404)
	mustStatus(t, do(t, app, "DELETE", "/kinds/"+other, f.orgS, roleSys, nil), 404)
	// version зөрвөл 409
	mustStatus(t, do(t, app, "PUT", "/kinds/"+id, f.orgS, roleSys, map[string]any{"version": 1, "name": "x"}), 409)
	// DELETE: soft delete, children хэвээр (soft), бүх org-д алга болно
	del := do(t, app, "DELETE", "/kinds/"+id, f.orgS, roleSys, nil)
	mustStatus(t, del, 200)
	for _, org := range []string{f.orgA, f.orgB, f.orgS} {
		mustStatus(t, do(t, app, "GET", "/kinds/"+id, org, roleRead, nil), 404)
	}
	if f.count(`SELECT count(*) FROM `+f.tbl("kind")+` WHERE id = ? AND deleted_at IS NOT NULL AND org_id IS NULL`, id) != 1 {
		t.Fatal("soft delete")
	}
	// sysadmin өөрийн org-ийн мөр: ердийн tenant мөр шиг (SystemCan шаардахгүй)
	mine := do(t, app, "POST", "/kinds", f.orgS, roleSys, map[string]any{"name": "миний"})
	mustStatus(t, do(t, app, "PUT", "/kinds/"+mine.str("id"), f.orgS, roleSys, map[string]any{"name": "миний2", "version": 1}), 200)
	if f.count(`SELECT count(*) FROM `+f.tbl("kind")+` WHERE id = ? AND org_id = ?`, mine.str("id"), f.orgS) != 1 {
		t.Fatal("өөрийн мөр org-ээ алдсан")
	}
}

func TestSharedHardDeleteSystemRowWithChildren(t *testing.T) {
	f := newSfx(t)
	app := f.mountShared(t, func(r *Resource[map[string]any]) { r.SoftDelete = false; r.Bulk = nil })
	f.exec(`ALTER TABLE ` + f.tbl("kind") + ` DROP COLUMN deleted_at`)
	sys := f.kindRow("", "Систем", "S")
	f.kindItem("", sys, "зүйл")
	other := f.kindRow(f.orgA, "А", "A")
	f.kindItem(f.orgA, other, "а зүйл")
	mustStatus(t, do(t, app, "DELETE", "/kinds/"+sys, f.orgA, roleAdmin, nil), 403)
	mustStatus(t, do(t, app, "DELETE", "/kinds/"+sys, f.orgS, roleSys, nil), 200)
	if f.count(`SELECT count(*) FROM `+f.tbl("kind")+` WHERE id = ?`, sys) != 0 || f.count(`SELECT count(*) FROM `+f.tbl("kind_item")+` WHERE kind_id = ?`, sys) != 0 {
		t.Fatal("hard delete + cascade child")
	}
	if f.count(`SELECT count(*) FROM `+f.tbl("kind_item")+` WHERE kind_id = ?`, other) != 1 {
		t.Fatal("өөр org-ийн child устсан")
	}
}

func TestSharedUniqueSemantics(t *testing.T) {
	f := newSfx(t)
	app := f.mountShared(t, nil)
	f.kindRow("", "Систем", "SYS")
	f.kindRow(f.orgA, "А", "AAA")

	// tenant утга системийн утгатай мөргөлдөхгүй (харагдах мөр бүртэй давхцахгүй)
	r := do(t, app, "POST", "/kinds", f.orgB, roleAdmin, map[string]any{"name": "x", "code": "SYS"})
	mustStatus(t, r, 422)
	if got := r.errs()["code"]; len(got) != 1 {
		t.Fatalf("errs: %s", r.Raw)
	}
	// өөр org-ийн утгатай давхцаж болно, мөн ТОДРУУЛАХГҮЙ: хариу ердийн 201 (өөр org-ийн оршихуйг задруулахгүй)
	mustStatus(t, do(t, app, "POST", "/kinds", f.orgB, roleAdmin, map[string]any{"name": "x", "code": "AAA"}), 201)
	// өөрийн org дотор давхцал
	mustStatus(t, do(t, app, "POST", "/kinds", f.orgB, roleAdmin, map[string]any{"name": "y", "code": "AAA"}), 422)
	// update: өөрийн утгаа хадгалж болно (self хасагдана); системийн утга руу өөрчилбөл 422
	own := do(t, app, "POST", "/kinds", f.orgB, roleAdmin, map[string]any{"name": "z", "code": "ZZZ"})
	mustStatus(t, do(t, app, "PUT", "/kinds/"+own.str("id"), f.orgB, roleAdmin, map[string]any{"code": "ZZZ", "version": 1}), 200)
	mustStatus(t, do(t, app, "PUT", "/kinds/"+own.str("id"), f.orgB, roleAdmin, map[string]any{"code": "SYS", "version": 2}), 422)
	// системийн мөр: зөвхөн системийн мөртэй харьцуулагдана — org-ийн утгатай давхцаж болно (org мэдээлэл алдагдахгүй)
	mustStatus(t, do(t, app, "POST", "/kinds/system", f.orgS, roleSys, map[string]any{"name": "s2", "code": "AAA"}), 201)
	mustStatus(t, do(t, app, "POST", "/kinds/system", f.orgS, roleSys, map[string]any{"name": "s3", "code": "SYS"}), 422)
}

func TestSharedNoSystemRouteWithoutSystemCan(t *testing.T) {
	f := newSfx(t)
	app := f.mountShared(t, func(r *Resource[map[string]any]) { r.SystemCan = nil })
	sys := f.kindRow("", "Систем", "S")
	// route байхгүй: /system нь POST / биш, өөр route-д таарахгүй
	if r := do(t, app, "POST", "/kinds/system", f.orgS, roleSys, map[string]any{"name": "x"}); r.Status != 404 && r.Status != 405 {
		t.Fatalf("/system route нээлттэй: %d %s", r.Status, r.Raw)
	}
	// sysadmin ч системийн мөрийг засаж/устгаж чадахгүй (анхдагч: боломжгүй)
	mustStatus(t, do(t, app, "PUT", "/kinds/"+sys, f.orgS, roleSys, map[string]any{"name": "h", "version": 1}), 403)
	mustStatus(t, do(t, app, "DELETE", "/kinds/"+sys, f.orgS, roleSys, nil), 403)
	m := do(t, app, "GET", "/kinds/meta", f.orgS, roleSys, nil)
	if m.JSON["scope"] != "shared" || m.JSON["system_writes"] != nil {
		t.Fatalf("meta: %s", m.Raw)
	}
}

func TestSharedMeta(t *testing.T) {
	f := newSfx(t)
	app := f.mountShared(t, nil)
	m := do(t, app, "GET", "/kinds/meta", f.orgA, roleRead, nil)
	mustStatus(t, m, 200)
	if m.JSON["scope"] != "shared" || m.JSON["system_writes"] != true {
		t.Fatalf("meta: %s", m.Raw)
	}
	if got := f.kindResource().Meta(); got.Scope != "shared" || !got.SystemWrites || got.GlobalWrites {
		t.Fatalf("Meta(): %+v", got)
	}
	bad := f.kindResource()
	bad.Name = "Буруу нэр"
	if got := bad.Meta(); got.Scope != "shared" {
		t.Fatalf("алдаатай Meta() scope: %q", got.Scope)
	}
	// tenant анхдагч
	tr := f.kindResource()
	tr.Scope, tr.SystemCan = ScopeTenant, nil
	if got := tr.Meta(); got.Scope != "tenant" || got.SystemWrites {
		t.Fatalf("tenant Meta(): %+v", got)
	}
}

func TestSharedEventsAndAudit(t *testing.T) {
	f := newSfx(t)
	app := f.mountShared(t, func(r *Resource[map[string]any]) {
		r.Audit = &AuditSpec{Enabled: true, Snapshot: true}
	})
	sys := do(t, app, "POST", "/kinds/system", f.orgS, roleSys, map[string]any{"name": "Систем"})
	mustStatus(t, sys, 201)
	id := sys.str("id")
	// event: OrgID = actor org, payload.system=true, org_id=null
	if f.count(`SELECT count(*) FROM khan.outbox_events WHERE topic = 'test.kind.created' AND org_id = ? AND aggregate_id = ?
		AND payload->>'system' = 'true' AND payload->'org_id' = 'null'::jsonb`, f.orgS, id) != 1 {
		t.Fatal("system created event")
	}
	own := do(t, app, "POST", "/kinds", f.orgA, roleAdmin, map[string]any{"name": "А"})
	if f.count(`SELECT count(*) FROM khan.outbox_events WHERE topic = 'test.kind.created' AND org_id = ? AND aggregate_id = ?
		AND payload->>'org_id' = ? AND payload->'system' IS NULL`, f.orgA, own.str("id"), f.orgA) != 1 {
		t.Fatal("tenant created event")
	}
	mustStatus(t, do(t, app, "PUT", "/kinds/"+id, f.orgS, roleSys, map[string]any{"name": "Систем2", "version": 1}), 200)
	mustStatus(t, do(t, app, "DELETE", "/kinds/"+id, f.orgS, roleSys, nil), 200)

	// audit: системийн мөрийн өөрчлөлт нь sysadmin-ий org-д бичигдэнэ (audit.org_id NOT NULL)
	if n := f.count(`SELECT count(*) FROM khan.audit_log WHERE resource = 'test.kind' AND record_id = ? AND org_id = ?`, id, f.orgS); n != 3 {
		t.Fatalf("audit мөр %d, want 3 (create, update, delete)", n)
	}
	if f.count(`SELECT count(*) FROM khan.audit_log WHERE record_id = ? AND org_id <> ?`, id, f.orgS) != 0 {
		t.Fatal("системийн мөрийн audit өөр org-д бичигдсэн")
	}
	// history: өөр org системийн мөрийн sysadmin-ий түүхийг ХАРАХГҮЙ (org-оор тусгаарласан)
	sys2 := do(t, app, "POST", "/kinds/system", f.orgS, roleSys, map[string]any{"name": "Систем3"})
	id2 := sys2.str("id")
	h := do(t, app, "GET", "/kinds/"+id2+"/history", f.orgA, roleRead, nil)
	mustStatus(t, h, 200)
	if len(h.list("data")) != 0 {
		t.Fatalf("tenant системийн мөрийн sysadmin түүхийг харлаа: %s", h.Raw)
	}
	if h := do(t, app, "GET", "/kinds/"+id2+"/history", f.orgS, roleRead, nil); len(h.list("data")) != 1 {
		t.Fatalf("sysadmin өөрийн түүх: %s", h.Raw)
	}
	// өөр org-ийн мөрийн түүх = 404
	mustStatus(t, do(t, app, "GET", "/kinds/"+own.str("id")+"/history", f.orgB, roleRead, nil), 404)
}

func TestSharedCache(t *testing.T) {
	f := newSfx(t)
	cc := newTestCache(t)
	app := f.mountShared(t, func(r *Resource[map[string]any]) { r.Cache = &CacheSpec{Cache: cc} })
	sys := f.kindRow("", "Систем", "S")
	f.kindRow(f.orgA, "А", "A")
	f.kindRow(f.orgB, "Б", "B")

	warm := func(org string) hresp {
		return get(t, app, "/kinds", org, roleRead)
	}
	a1, b1 := warm(f.orgA), warm(f.orgB)
	if a1.cache() != "miss" || b1.cache() != "miss" || a1.etag() == b1.etag() {
		t.Fatalf("эхний уншилт: %q %q", a1.cache(), b1.cache())
	}
	if strings.Contains(string(a1.Raw), "Б") && !strings.Contains(string(a1.Raw), "Систем") {
		t.Fatalf("A-ийн cache: %s", a1.Raw)
	}
	d1 := get(t, app, "/kinds/"+sys, f.orgA, roleRead)
	d1b := get(t, app, "/kinds/"+sys, f.orgB, roleRead)
	if d1.cache() != "miss" || d1b.cache() != "miss" {
		t.Fatalf("detail: %q %q", d1.cache(), d1b.cache())
	}
	if a2, b2 := warm(f.orgA), warm(f.orgB); a2.cache() != "hit" || b2.cache() != "hit" {
		t.Fatalf("дахин: %q %q", a2.cache(), b2.cache())
	}
	// A-ийн өөрийн бичилт B-ийн cache-д нөлөөлөхгүй
	mustStatus(t, do(t, app, "POST", "/kinds", f.orgA, roleAdmin, map[string]any{"name": "А2"}), 201)
	if warm(f.orgA).cache() != "miss" || warm(f.orgB).cache() != "hit" {
		t.Fatal("A-ийн бичилт B-ийн cache-ийг хүчингүй болгосон / A-ийнхыг болгоогүй")
	}
	// системийн мөрийн бичилт БҮХ org-ийн cache-ийг хүчингүй болгоно (list, detail, ETag, 304)
	etagA, etagB := warm(f.orgA).etag(), warm(f.orgB).etag()
	mustStatus(t, do(t, app, "PUT", "/kinds/"+sys, f.orgS, roleSys, map[string]any{"name": "Систем-шинэ", "version": 1}), 200)
	for org, old := range map[string]string{f.orgA: etagA, f.orgB: etagB} {
		l := warm(org)
		if l.cache() != "miss" || !strings.Contains(string(l.Raw), "Систем-шинэ") || l.etag() == old {
			t.Fatalf("%s: системийн бичилтийн дараа %q etag-солигдсон=%v %s", org, l.cache(), l.etag() != old, l.Raw)
		}
		if r := doH(t, app, "GET", "/kinds", org, roleRead, nil, map[string]string{"If-None-Match": old}); r.Status != 200 {
			t.Fatalf("хуучин ETag 304 өглөө: %d", r.Status)
		}
		d := get(t, app, "/kinds/"+sys, org, roleRead)
		if d.cache() != "miss" || d.JSON["name"] != "Систем-шинэ" {
			t.Fatalf("detail %q %s", d.cache(), d.Raw)
		}
	}
	// системийн create / delete ч мөн
	warm(f.orgA)
	nid := do(t, app, "POST", "/kinds/system", f.orgS, roleSys, map[string]any{"name": "Систем-2"})
	if l := warm(f.orgA); l.cache() != "miss" || !strings.Contains(string(l.Raw), "Систем-2") {
		t.Fatalf("system create: %q", l.cache())
	}
	warm(f.orgB)
	mustStatus(t, do(t, app, "DELETE", "/kinds/"+nid.str("id"), f.orgS, roleSys, nil), 200)
	if l := warm(f.orgB); l.cache() != "miss" || strings.Contains(string(l.Raw), "Систем-2") {
		t.Fatalf("system delete: %q", l.cache())
	}
	// 403-аар татгалзсан системийн бичилт cache-ийг хүчингүй болгохгүй
	warm(f.orgA)
	mustStatus(t, do(t, app, "PUT", "/kinds/"+sys, f.orgA, roleAdmin, map[string]any{"name": "h", "version": 2}), 403)
	if warm(f.orgA).cache() != "hit" {
		t.Fatal("татгалзсан бичилт cache-ийг хүчингүй болгосон")
	}
	// options cache: Shared лавлахын системийн мөр
	gs := f.grp("", "Системийн бүлэг")
	o1 := get(t, app, "/kinds/options/group_id", f.orgA, roleRead)
	o2 := get(t, app, "/kinds/options/group_id", f.orgA, roleRead)
	if o1.cache() != "miss" || o2.cache() != "hit" || !strings.Contains(string(o2.Raw), gs) {
		t.Fatalf("options cache: %q %q %s", o1.cache(), o2.cache(), o2.Raw)
	}
}

func TestSharedCacheDependsOnSystemGeneration(t *testing.T) {
	f := newSfx(t)
	cc := newTestCache(t)
	app := f.mountShared(t, func(r *Resource[map[string]any]) { r.Cache = &CacheSpec{Cache: cc, DependsOn: []string{"test.dep"}} })
	sys := f.kindRow("", "Систем", "S")
	get(t, app, "/kinds", f.orgA, roleRead)
	if get(t, app, "/kinds", f.orgA, roleRead).cache() != "hit" {
		t.Fatal("hit")
	}
	// DependsOn-ийн системийн generation өөрчлөгдвөл бүх org-ийн түлхүүр солигдоно
	if err := cc.Bump(t.Context(), systemCacheOrg, "test.dep"); err != nil {
		t.Fatal(err)
	}
	if get(t, app, "/kinds", f.orgA, roleRead).cache() != "miss" {
		t.Fatal("DependsOn-ийн _system generation түлхүүрт ороогүй")
	}
	_ = sys
}

func TestSharedBulkAndHooksDelete(t *testing.T) {
	f := newSfx(t)
	var seen []string
	app := f.mountShared(t, func(r *Resource[map[string]any]) {
		r.Hooks.BeforeDelete = func(x *Ctx) error { seen = append(seen, x.ID); return nil }
	})
	sys := f.kindRow("", "Систем", "S")
	a1, a2 := f.kindRow(f.orgA, "А1", "A1"), f.kindRow(f.orgA, "А2", "A2")
	// hook-тэй bulk delete (deleteOne замаар) — системийн мөр байвал 403 бөгөөд юу ч устахгүй
	mustStatus(t, do(t, app, "POST", "/kinds/bulk", f.orgA, roleAdmin, map[string]any{"action": "delete", "ids": []string{a1, sys}}), 403)
	if len(seen) != 0 || f.count(`SELECT count(*) FROM `+f.tbl("kind")+` WHERE deleted_at IS NOT NULL`) != 0 {
		t.Fatalf("hook-тэй bulk хэсэгчлэн ажилласан: %v", seen)
	}
	// bulk нь sysadmin-д ч системийн мөрөнд ажиллахгүй
	mustStatus(t, do(t, app, "POST", "/kinds/bulk", f.orgS, roleSys, map[string]any{"action": "update", "ids": []string{sys}, "set": map[string]any{"note": "b"}}), 403)
	bk := do(t, app, "POST", "/kinds/bulk", f.orgA, roleAdmin, map[string]any{"action": "delete", "ids": []string{a1, a2}})
	mustStatus(t, bk, 200)
	if bk.JSON["affected"] != float64(2) || len(seen) != 2 {
		t.Fatalf("bulk delete: %s %v", bk.Raw, seen)
	}
	// bulk update өөрийн мөр: version нэмэгдэнэ, event
	a3 := f.kindRow(f.orgA, "А3", "A3")
	mustStatus(t, do(t, app, "POST", "/kinds/bulk", f.orgA, roleAdmin, map[string]any{"action": "update", "ids": []string{a3}, "set": map[string]any{"note": "n"}}), 200)
	if f.count(`SELECT count(*) FROM `+f.tbl("kind")+` WHERE id = ? AND version = 2 AND note = 'n'`, a3) != 1 {
		t.Fatal("bulk update version")
	}
	if f.count(`SELECT count(*) FROM `+f.tbl("kind")+` WHERE id = ? AND note IS NULL AND version = 1`, sys) != 1 {
		t.Fatal("системийн мөр өөрчлөгдсөн")
	}
}

func TestShared_MountValidation(t *testing.T) {
	f := newSfx(t)
	cases := map[string]func(r *Resource[map[string]any]){
		"scope буруу":           func(r *Resource[map[string]any]) { r.Scope = Scope(9) },
		"SystemCan + RLS":       func(r *Resource[map[string]any]) { r.RLS = true },
		"SystemCan tenant-д":    func(r *Resource[map[string]any]) { r.Scope = ScopeTenant },
		"GlobalWrites shared-д": func(r *Resource[map[string]any]) { r.GlobalWrites = true },
	}
	for name, mut := range cases {
		r := f.kindResource()
		mut(r)
		if err := r.Check(); err == nil {
			// Check нь runtime-гүй: RLS+SystemCan бүх горимд тохиргооны алдаа
			t.Errorf("%s: Check алдаагүй", name)
		}
		if err := r.Mount(fiber.New().Group("/x")); err == nil {
			t.Errorf("%s: Mount алдаагүй", name)
		}
	}
	// Shared + RLS (SystemCan-гүй) зөвшөөрөгдөнө — системийн мөрийг унших (PolicyOptions.SystemRows)
	r := f.kindResource()
	r.SystemCan, r.RLS = nil, true
	if err := r.Check(); err != nil {
		t.Errorf("Shared+RLS: %v", err)
	}
	if got := Scope(9).String(); got != "scope(9)" {
		t.Errorf("String: %q", got)
	}
	for s, want := range map[Scope]string{ScopeTenant: "tenant", ScopeShared: "shared", ScopeGlobal: "global"} {
		if s.String() != want {
			t.Errorf("%d → %q", s, s.String())
		}
	}
}

// RLS (strict) + SystemRows policy: tenant системийн мөрийг ҮЗНЭ, бичиж чадахгүй.
func TestSharedWithRLSSystemRows(t *testing.T) {
	f := newSfx(t)
	sys := f.kindRow("", "Систем", "S")
	f.kindItem("", sys, "системийн зүйл")
	a := f.kindRow(f.orgA, "А", "A")
	b := f.kindRow(f.orgB, "Б", "B")
	gs, ga := f.grp("", "Систем бүлэг"), f.grp(f.orgA, "А бүлэг")
	f.grp(f.orgB, "Б бүлэг")
	f.owner(f.orgA, "А эзэн")

	cfg, ok, err := testkit.DBConfig()
	if err != nil || !ok {
		t.Skip("test DB тохиргоогүй")
	}
	role := fmt.Sprintf("khan_rls_%d", time.Now().UnixNano()%1000000000)
	f.exec(`CREATE ROLE ` + role + ` NOLOGIN NOSUPERUSER NOBYPASSRLS`)
	t.Cleanup(func() {
		f.db.Exec(`DROP OWNED BY ` + role)
		f.db.Exec(`DROP ROLE IF EXISTS ` + role)
	})
	f.exec(`GRANT USAGE ON SCHEMA ` + f.schema + ` TO ` + role)
	f.exec(`GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA ` + f.schema + ` TO ` + role)
	for _, tbl := range []string{"kind", "kind_item", "grp"} {
		sql, err := tenant.PolicySQL(f.tbl(tbl), tenant.PolicyOptions{Mode: tenant.PolicyStrict, SystemRows: true})
		if err != nil {
			t.Fatal(err)
		}
		f.exec(sql)
	}
	sql, _ := tenant.PolicySQL(f.tbl("owner"), tenant.PolicyOptions{Mode: tenant.PolicyStrict})
	f.exec(sql)
	cfg.Pool = config.Pool{MaxOpen: 1, MaxIdle: 1}
	rdb, err := db.Open(cfg, db.WithLogger(logger.Discard))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close(rdb) })
	if err := rdb.Exec(`SET ROLE ` + role).Error; err != nil {
		t.Fatal(err)
	}

	r := f.kindResource()
	r.RLS, r.SystemCan, r.Events, r.DB = true, nil, false, func() *gorm.DB { return rdb }
	app := mountOn(t, "/kinds", r)

	l := do(t, app, "GET", "/kinds", f.orgA, roleAdmin, nil)
	mustStatus(t, l, 200)
	if got := ids(l); !got[sys] || !got[a] || got[b] || len(got) != 2 {
		t.Fatalf("RLS жагсаалт: %s", l.Raw)
	}
	d := do(t, app, "GET", "/kinds/"+sys, f.orgB, roleAdmin, nil)
	if d.Status != 200 || len(d.list("items")) != 1 {
		t.Fatalf("RLS системийн detail+children: %s", d.Raw)
	}
	mustStatus(t, do(t, app, "GET", "/kinds/"+b, f.orgA, roleAdmin, nil), 404)
	o := do(t, app, "GET", "/kinds/options/group_id", f.orgA, roleAdmin, nil)
	if vals := o.list("data"); len(vals) != 2 {
		t.Fatalf("RLS options: %s", o.Raw)
	}
	// өөрийн бичилт ажиллана (WITH CHECK org_id = app.org_id)
	c := do(t, app, "POST", "/kinds", f.orgA, roleAdmin, map[string]any{"name": "шинэ", "group_id": gs, "items": []map[string]any{{"label": "x"}}})
	mustStatus(t, c, 201)
	mustStatus(t, do(t, app, "PUT", "/kinds/"+c.str("id"), f.orgA, roleAdmin, map[string]any{"name": "шинэ2", "group_id": ga, "version": 1}), 200)
	// системийн мөр: RLS-ийн `FOR UPDATE` нь бичих policy-д харагдахгүй тул түгжих үед 404 (403 биш) —
	// аль ч тохиолдолд өөрчлөгдөхгүй (fail-closed). bulk нь системийн мөрийг түгжиж үл чадна → 0 нөлөө.
	mustStatus(t, do(t, app, "PUT", "/kinds/"+sys, f.orgA, roleAdmin, map[string]any{"name": "h", "version": 1}), 404)
	mustStatus(t, do(t, app, "DELETE", "/kinds/"+sys, f.orgA, roleAdmin, nil), 404)
	if bk := do(t, app, "POST", "/kinds/bulk", f.orgA, roleAdmin, map[string]any{"action": "delete", "ids": []string{sys}}); bk.Status != 200 || bk.JSON["affected"] != float64(0) {
		t.Fatalf("RLS bulk: %s", bk.Raw)
	}
	if f.count(`SELECT count(*) FROM `+f.tbl("kind")+` WHERE id = ? AND name = 'Систем' AND org_id IS NULL`, sys) != 1 {
		t.Fatal("системийн мөр өөрчлөгдсөн")
	}
}
