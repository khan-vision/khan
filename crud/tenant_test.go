package crud

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
)

func TestAuthAndTenantIsolation(t *testing.T) {
	f := newFx(t)
	app := mountOn(t, "/rooms", f.roomResource())
	a := do(t, app, "POST", "/rooms", f.orgA, 1, map[string]any{"name": "A-ийн", "code": "A"})
	mustStatus(t, a, 201)
	id := a.str("id")
	b := do(t, app, "POST", "/rooms", f.orgB, 1, map[string]any{"name": "B-ийн"})
	mustStatus(t, b, 201)

	// 401: claims байхгүй, бүх endpoint
	for _, c := range [][2]string{{"GET", "/rooms"}, {"POST", "/rooms/query"}, {"GET", "/rooms/" + id}, {"POST", "/rooms"}, {"PUT", "/rooms/" + id},
		{"DELETE", "/rooms/" + id}, {"GET", "/rooms/export.xlsx"}, {"POST", "/rooms/bulk"}, {"GET", "/rooms/meta"}, {"GET", "/rooms/options/owner_id"}} {
		r := do(t, app, c[0], c[1], "", 0, `{"name":"x","version":1}`)
		if r.Status != 401 || r.str("error") != "Зөвшөөрөлгүй" {
			t.Errorf("%v → %d %s", c, r.Status, r.Raw)
		}
	}
	// X-Org-ID / ?org_id-д итгэхгүй
	req := httptest.NewRequest("GET", "/rooms?org_id="+f.orgA, nil)
	req.Header.Set("X-Org-ID", f.orgA)
	res, _ := app.Test(req, fiber.TestConfig{Timeout: 0})
	if res.StatusCode != 401 {
		t.Errorf("X-Org-ID итгэгдсэн: %d", res.StatusCode)
	}

	// 404: өөр org (байхгүйтэй ялгагдахгүй)
	missing := newUUID()
	for _, m := range []string{"GET", "PUT", "DELETE"} {
		x := do(t, app, m, "/rooms/"+id, f.orgB, 1, `{"name":"hack","version":1}`)
		y := do(t, app, m, "/rooms/"+missing, f.orgB, 1, `{"name":"hack","version":1}`)
		if x.Status != 404 || y.Status != 404 || string(x.Raw) != string(y.Raw) {
			t.Errorf("%s: өөр org=%d %s ; байхгүй=%d %s", m, x.Status, x.Raw, y.Status, y.Raw)
		}
	}
	if f.count(`SELECT count(*) FROM `+f.tbl("room")+` WHERE id = ? AND name = 'A-ийн' AND deleted_at IS NULL`, id) != 1 {
		t.Fatal("A-ийн мөр өөрчлөгдсөн / устсан")
	}
	// list
	l := do(t, app, "GET", "/rooms", f.orgB, 1, nil)
	if l.JSON["total"] != float64(1) || l.list("data")[0]["name"] != "B-ийн" {
		t.Errorf("B list: %s", l.Raw)
	}
	// search / filter-ээр ч алдагдахгүй
	l = do(t, app, "GET", "/rooms?search=A-ийн", f.orgB, 1, nil)
	if l.JSON["total"] != float64(0) {
		t.Errorf("%s", l.Raw)
	}
	// bulk: өөр org-ийн id → affected 0
	bk := do(t, app, "POST", "/rooms/bulk", f.orgB, 1, map[string]any{"action": "update", "ids": []string{id}, "set": map[string]any{"capacity": 5}})
	mustStatus(t, bk, 200)
	if bk.JSON["affected"] != float64(0) {
		t.Errorf("bulk: %s", bk.Raw)
	}
	bk = do(t, app, "POST", "/rooms/bulk", f.orgB, 1, map[string]any{"action": "delete", "ids": []string{id}})
	if bk.JSON["affected"] != float64(0) {
		t.Errorf("bulk delete: %s", bk.Raw)
	}
	// parent-ийн id-г өөр org-ийн мөр дээр options-оор авахгүй (options_test-д)
}

func TestPermissions403(t *testing.T) {
	f := newFx(t)
	app := mountOn(t, "/rooms", f.roomResource())
	c := do(t, app, "POST", "/rooms", f.orgA, 1, map[string]any{"name": "a"})
	id := c.str("id")

	forbid := func(role int, method, path string, body any) {
		t.Helper()
		r := do(t, app, method, path, f.orgA, role, body)
		if r.Status != 403 || r.str("error") != "Хандах эрхгүй байна" {
			t.Errorf("role %d %s %s → %d %s", role, method, path, r.Status, r.Raw)
		}
	}
	// role 2: зөвхөн унших
	forbid(2, "POST", "/rooms", `{"name":"x"}`)
	forbid(2, "PUT", "/rooms/"+id, `{"name":"x","version":1}`)
	forbid(2, "DELETE", "/rooms/"+id, nil)
	forbid(2, "POST", "/rooms/bulk", map[string]any{"action": "update", "ids": []string{id}, "set": map[string]any{"capacity": 1}})
	forbid(2, "POST", "/rooms/bulk", map[string]any{"action": "delete", "ids": []string{id}})
	// role 3: засах боломжтой, устгах эрхгүй → bulk update ok, bulk delete 403
	forbid(3, "DELETE", "/rooms/"+id, nil)
	forbid(3, "POST", "/rooms/bulk", map[string]any{"action": "delete", "ids": []string{id}})
	// үл мэдэгдэх үйлдэл (том үсэг) — ямар ч өөрчлөлтгүй 400
	mustStatus(t, do(t, app, "POST", "/rooms/bulk", f.orgA, 1, map[string]any{"action": "Delete", "ids": []string{id}}), 400)
	if f.count(`SELECT count(*) FROM `+f.tbl("room")+` WHERE id = ? AND deleted_at IS NULL`, id) != 1 {
		t.Fatal("үл мэдэгдэх bulk үйлдэл мөр устгасан")
	}
	ok := do(t, app, "POST", "/rooms/bulk", f.orgA, 3, map[string]any{"action": "update", "ids": []string{id}, "set": map[string]any{"capacity": 7}})
	mustStatus(t, ok, 200)
	if ok.JSON["affected"] != float64(1) {
		t.Errorf("%s", ok.Raw)
	}
	// эрх тодорхойгүй role 9: бүх зүйл 403 (унших ч)
	for _, c := range [][2]string{{"GET", "/rooms"}, {"GET", "/rooms/" + id}, {"GET", "/rooms/meta"}, {"GET", "/rooms/export.xlsx"}, {"GET", "/rooms/options/owner_id"}} {
		forbid(9, c[0], c[1], nil)
	}
	// эрхгүй хүсэлт DB-д хүрэхгүй (body-г задлахаас ӨМНӨ 403)
	forbid(2, "POST", "/rooms", `{not json`)
	if f.count(`SELECT count(*) FROM `+f.tbl("room")+` WHERE capacity = 1`) != 0 {
		t.Error("role 2 bulk өөрчилсөн")
	}
	// унших: role 2 GET ok
	mustStatus(t, do(t, app, "GET", "/rooms/"+id, f.orgA, 2, nil), 200)
}

func TestListFeatures(t *testing.T) {
	f := newFx(t)
	app := mountOn(t, "/rooms", f.roomResource())
	for i, n := range []string{"Alpha", "Beta", "Gamma"} {
		mustStatus(t, do(t, app, "POST", "/rooms", f.orgA, 1, map[string]any{"name": n, "capacity": (i + 1) * 10, "status": []string{"draft", "open", "open"}[i], "code": fmt.Sprintf("C-%d", i)}), 201)
	}
	l := do(t, app, "GET", "/rooms?sort=capacity&dir=desc&limit=2", f.orgA, 1, nil)
	d := l.list("data")
	if len(d) != 2 || d[0]["name"] != "Gamma" || l.JSON["total"] != float64(3) || l.JSON["limit"] != float64(2) {
		t.Fatalf("%s", l.Raw)
	}
	if l.JSON["totals"].(map[string]any)["capacity"] != float64(60) {
		t.Errorf("totals: %s", l.Raw)
	}
	l = do(t, app, "GET", "/rooms?search=bet", f.orgA, 1, nil)
	if l.JSON["total"] != float64(1) {
		t.Errorf("search: %s", l.Raw)
	}
	l = do(t, app, "GET", `/rooms?filters=`+`%7B%22status%22%3A%7B%22op%22%3A%22eq%22%2C%22v%22%3A%22open%22%7D%7D`, f.orgA, 1, nil)
	if l.JSON["total"] != float64(2) {
		t.Errorf("filter: %s", l.Raw)
	}
	// POST /query
	q := do(t, app, "POST", "/rooms/query", f.orgA, 1, map[string]any{"page": 1, "limit": 1, "sort": "name", "dir": "asc", "filters": map[string]any{"capacity": map[string]any{"op": "gte", "v": 20}}})
	mustStatus(t, q, 200)
	if q.JSON["total"] != float64(2) || q.list("data")[0]["name"] != "Beta" {
		t.Errorf("%s", q.Raw)
	}
	// буруу шүүлт → 400 (зөв мессеж), дотоод алдаа биш
	bad := do(t, app, "GET", `/rooms?filters=%7B%22code%22%3A%7B%22op%22%3A%22eq%22%2C%22v%22%3A%22x%22%7D%7D`, f.orgA, 1, nil)
	if bad.Status != 400 || bad.str("error") == "" {
		t.Errorf("%d %s", bad.Status, bad.Raw)
	}
	bad = do(t, app, "POST", "/rooms/query", f.orgA, 1, "{not json")
	mustStatus(t, bad, 400)
}

func TestExportAndBulk(t *testing.T) {
	f := newFx(t)
	app := mountOn(t, "/rooms", f.roomResource())
	var ids []string
	for i := 0; i < 3; i++ {
		c := do(t, app, "POST", "/rooms", f.orgA, 1, map[string]any{"name": fmt.Sprint("r", i), "capacity": i})
		ids = append(ids, c.str("id"))
	}
	req := httptest.NewRequest("GET", "/rooms/export.xlsx", nil)
	req.Header.Set("X-T-Org", f.orgA)
	req.Header.Set("X-T-Role", "2")
	res, err := app.Test(req, fiber.TestConfig{Timeout: 0})
	if err != nil || res.StatusCode != 200 || !strings.Contains(res.Header.Get("Content-Type"), "spreadsheetml") || res.Header.Get("X-Kv-Export-Rows") != "3" {
		t.Fatalf("export: %v %v", err, res)
	}
	// bulk update
	u := do(t, app, "POST", "/rooms/bulk", f.orgA, 1, map[string]any{"action": "update", "ids": ids[:2], "set": map[string]any{"capacity": 99}})
	mustStatus(t, u, 200)
	if u.JSON["affected"] != float64(2) || f.count(`SELECT count(*) FROM `+f.tbl("room")+` WHERE capacity = 99`) != 2 {
		t.Errorf("%s", u.Raw)
	}
	// bulk-д зарлаагүй талбар / буруу утга → 400
	mustStatus(t, do(t, app, "POST", "/rooms/bulk", f.orgA, 1, map[string]any{"action": "update", "ids": ids, "set": map[string]any{"name": "x"}}), 400)
	mustStatus(t, do(t, app, "POST", "/rooms/bulk", f.orgA, 1, map[string]any{"action": "update", "ids": ids, "set": map[string]any{"capacity": -5}}), 400)
	mustStatus(t, do(t, app, "POST", "/rooms/bulk", f.orgA, 1, "{bad"), 400)
	// bulk delete = soft delete
	dl := do(t, app, "POST", "/rooms/bulk", f.orgA, 1, map[string]any{"action": "delete", "ids": ids})
	mustStatus(t, dl, 200)
	if dl.JSON["affected"] != float64(3) || f.count(`SELECT count(*) FROM `+f.tbl("room")+` WHERE deleted_at IS NOT NULL`) != 3 {
		t.Errorf("%s", dl.Raw)
	}
	// дахин устгахад affected 0 (soft-deleted)
	dl = do(t, app, "POST", "/rooms/bulk", f.orgA, 1, map[string]any{"action": "delete", "ids": ids})
	if dl.JSON["affected"] != float64(0) {
		t.Errorf("%s", dl.Raw)
	}
}

func TestMetaEndpointAndStruct(t *testing.T) {
	f := newFx(t)
	res := f.roomResource()
	app := mountOn(t, "/rooms", res)
	r := do(t, app, "GET", "/rooms/meta", f.orgA, 2, nil)
	mustStatus(t, r, 200)
	if r.str("name") != "test.room" || r.str("endpoint") != "/rooms" || r.JSON["export"] != true || r.JSON["bulk"] != true {
		t.Errorf("%s", r.Raw)
	}
	can := r.JSON["can"].(map[string]any)
	if can["read"] != true || can["update"] != false {
		t.Errorf("can: %v", can)
	}
	// ExposeMeta=false → /meta route байхгүй (id гэж ойлгож 400)
	res2 := f.roomResource()
	res2.ExposeMeta = false
	app2 := mountOn(t, "/rooms", res2)
	if x := do(t, app2, "GET", "/rooms/meta", f.orgA, 1, nil); x.Status != 400 {
		t.Errorf("ExposeMeta=false: %d %s", x.Status, x.Raw)
	}
	// Export/Bulk nil → route байхгүй
	res3 := f.roomResource()
	res3.Export, res3.Bulk = nil, nil
	app3 := mountOn(t, "/rooms", res3)
	if x := do(t, app3, "POST", "/rooms/bulk", f.orgA, 1, `{}`); x.Status == 200 {
		t.Errorf("bulk route байх ёсгүй: %d", x.Status)
	}
	if x := do(t, app3, "GET", "/rooms/export.xlsx", f.orgA, 1, nil); x.Status != 400 {
		t.Errorf("export route байх ёсгүй: %d %s", x.Status, x.Raw)
	}
}
