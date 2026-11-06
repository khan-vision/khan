package crud

import (
	"fmt"
	"gorm.io/gorm"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/khan-vision/khan/auth"
)

func TestOptionsEndpoint(t *testing.T) {
	f := newFx(t)
	app := mountOn(t, "/rooms", f.roomResource())
	bat := f.person(f.orgA, "Бат", 1)
	bold := f.person(f.orgA, "Болд", 2)
	gone := f.person(f.orgA, "Батаа устсан", 1)
	f.exec(`UPDATE `+f.tbl("person")+` SET deleted_at = now() WHERE id = ?`, gone)
	foreign := f.person(f.orgB, "Бат бусад", 1)

	r := do(t, app, "GET", "/rooms/options/owner_id", f.orgA, 2, nil)
	mustStatus(t, r, 200)
	d := r.list("data")
	if len(d) != 2 || d[0]["label"] != "Бат" || d[0]["value"] != bat || d[1]["value"] != bold || d[0]["parent_value"] != float64(1) {
		t.Fatalf("%s", r.Raw)
	}
	for _, x := range d {
		if x["value"] == foreign || x["value"] == gone {
			t.Errorf("өөр org / устсан гарсан: %s", r.Raw)
		}
	}
	r = do(t, app, "GET", "/rooms/options/owner_id?search="+url.QueryEscape("бо"), f.orgA, 2, nil)
	if len(r.list("data")) != 1 || r.list("data")[0]["value"] != bold {
		t.Errorf("search: %s", r.Raw)
	}
	r = do(t, app, "GET", "/rooms/options/owner_id?parent=2", f.orgA, 2, nil)
	if len(r.list("data")) != 1 {
		t.Errorf("parent: %s", r.Raw)
	}
	r = do(t, app, "GET", "/rooms/options/owner_id?ids="+strings.Join([]string{bat, foreign, gone, "junk"}, ","), f.orgA, 2, nil)
	if len(r.list("data")) != 1 || r.list("data")[0]["value"] != bat {
		t.Errorf("ids: %s", r.Raw)
	}
	r = do(t, app, "GET", "/rooms/options/owner_id?limit=1", f.orgA, 2, nil)
	if len(r.list("data")) != 1 {
		t.Errorf("limit: %s", r.Raw)
	}
	// B: зөвхөн өөрийнх
	r = do(t, app, "GET", "/rooms/options/owner_id", f.orgB, 2, nil)
	if len(r.list("data")) != 1 || r.list("data")[0]["value"] != foreign {
		t.Errorf("B: %s", r.Raw)
	}
	// ref биш талбар / үл мэдэгдэх → 404
	for _, fld := range []string{"name", "nope", "owner_id.x"} {
		if x := do(t, app, "GET", "/rooms/options/"+fld, f.orgA, 2, nil); x.Status != 404 {
			t.Errorf("%s → %d", fld, x.Status)
		}
	}
}

func TestOptionsChildRef(t *testing.T) {
	f := newFx(t)
	app := mountOn(t, "/invoices", f.invoiceResource())
	item := f.person(f.orgA, "Бараа", 1)
	f.person(f.orgB, "Бусдын", 1)
	r := do(t, app, "GET", "/invoices/options/lines.item_id", f.orgA, 1, nil)
	mustStatus(t, r, 200)
	if len(r.list("data")) != 1 || r.list("data")[0]["value"] != item {
		t.Errorf("%s", r.Raw)
	}
	m := (&Resource[map[string]any]{}).Meta() // буруу тохиргоо → хоосон Meta
	if len(m.Fields) != 0 {
		t.Error("буруу тохиргоонд Fields хоосон")
	}
	meta := f.invoiceResource().Meta()
	if meta.Children[0].Fields[0].Ref == nil || meta.Children[0].Fields[0].Ref.Options != "/invoices/options/lines.item_id" {
		t.Errorf("child ref meta: %+v", meta.Children[0].Fields[0].Ref)
	}
}

func TestConcurrentVersionedUpdates(t *testing.T) {
	f := newFx(t)
	app := mountOn(t, "/rooms", f.roomResource())
	c := do(t, app, "POST", "/rooms", f.orgA, 1, map[string]any{"name": "race", "capacity": 0})
	id := c.str("id")
	const n = 12
	var wg sync.WaitGroup
	statuses := make([]int, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := do(t, app, "PUT", "/rooms/"+id, f.orgA, 1, map[string]any{"capacity": i + 1, "version": 1})
			statuses[i] = r.Status
		}(i)
	}
	wg.Wait()
	ok, conflict := 0, 0
	for _, s := range statuses {
		switch s {
		case 200:
			ok++
		case 409:
			conflict++
		default:
			t.Errorf("гэнэтийн status: %v", statuses)
		}
	}
	if ok != 1 || conflict != n-1 {
		t.Fatalf("яг нэг амжилттай байх ёстой: %v", statuses)
	}
	if f.count(`SELECT version FROM `+f.tbl("room")+` WHERE id = ?`, id) != 2 {
		t.Error("version яг 1-ээр нэмэгдэх ёстой")
	}
}

func TestConcurrentCreatesAndReads(t *testing.T) {
	f := newFx(t)
	app := mountOn(t, "/rooms", f.roomResource())
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			org := f.orgA
			if i%2 == 1 {
				org = f.orgB
			}
			r := do(t, app, "POST", "/rooms", org, 1, map[string]any{"name": fmt.Sprint("n", i), "code": fmt.Sprint("C-", i)})
			if r.Status != 201 {
				t.Errorf("%d %s", r.Status, r.Raw)
				return
			}
			do(t, app, "GET", "/rooms", org, 1, nil)
		}(i)
	}
	wg.Wait()
	if f.count(`SELECT count(*) FROM `+f.tbl("room")+` WHERE org_id = ?`, f.orgA) != 10 || f.count(`SELECT count(*) FROM `+f.tbl("room")+` WHERE org_id = ?`, f.orgB) != 10 {
		t.Error("org тус бүр 10 мөртэй байх ёстой")
	}
}

func TestPasswordResource(t *testing.T) {
	f := newFx(t)
	app := mountOn(t, "/accounts", f.accountResource())
	c := do(t, app, "POST", "/accounts", f.orgA, 1, map[string]any{"login": "bat", "password": "secret-123", "display": "Бат"})
	mustStatus(t, c, 201)
	if _, has := c.JSON["password"]; has || strings.Contains(string(c.Raw), "secret-123") || strings.Contains(string(c.Raw), "$2") {
		t.Fatalf("password хариунд орсон: %s", c.Raw)
	}
	id := c.JSON["id"].(float64) // IDInt → тоо
	if id <= 0 {
		t.Fatal(c.Raw)
	}
	idStr := fmt.Sprint(int64(id))
	var hash string
	f.db.Raw(`SELECT password FROM `+f.tbl("account")+` WHERE id = ?`, idStr).Scan(&hash)
	if !auth.CheckPassword(hash, "secret-123") || hash == "secret-123" {
		t.Fatalf("bcrypt hash биш: %q", hash)
	}
	g := do(t, app, "GET", "/accounts/"+idStr, f.orgA, 1, nil)
	if _, has := g.JSON["password"]; has {
		t.Errorf("GET password: %s", g.Raw)
	}
	l := do(t, app, "GET", "/accounts", f.orgA, 1, nil)
	if strings.Contains(string(l.Raw), "password") || strings.Contains(string(l.Raw), "$2") {
		t.Errorf("list password: %s", l.Raw)
	}
	// update: password ирээгүй → hash хэвээр; хоосон → хэвээр; шинэ → солигдоно
	mustStatus(t, do(t, app, "PUT", "/accounts/"+idStr, f.orgA, 1, map[string]any{"display": "Бат2"}), 200)
	mustStatus(t, do(t, app, "PUT", "/accounts/"+idStr, f.orgA, 1, map[string]any{"password": ""}), 422) // Required: ирсэн бол хоосон байж болохгүй
	var h2 string
	f.db.Raw(`SELECT password FROM `+f.tbl("account")+` WHERE id = ?`, idStr).Scan(&h2)
	if h2 != hash {
		t.Error("password ирээгүй үед hash-ийг солисон")
	}
	mustStatus(t, do(t, app, "PUT", "/accounts/"+idStr, f.orgA, 1, map[string]any{"password": "new-secret-9"}), 200)
	f.db.Raw(`SELECT password FROM `+f.tbl("account")+` WHERE id = ?`, idStr).Scan(&h2)
	if !auth.CheckPassword(h2, "new-secret-9") || h2 == hash {
		t.Error("password солигдоогүй")
	}
	// богино нууц үг, заавал
	r := do(t, app, "POST", "/accounts", f.orgA, 1, map[string]any{"login": "x", "password": "short"})
	mustStatus(t, r, 422)
	r = do(t, app, "POST", "/accounts", f.orgA, 1, map[string]any{"login": "x"})
	if r.errs()["password"] == nil {
		t.Errorf("%s", r.Raw)
	}
	// unique login
	r = do(t, app, "POST", "/accounts", f.orgA, 1, map[string]any{"login": "bat", "password": "secret-123"})
	mustStatus(t, r, 422)
	// event payload-д hash орохгүй
	rows := f.outbox(f.orgA, "test.account.created")
	if len(rows) != 1 || strings.Contains(rows[0]["payload"].(string), "$2") || strings.Contains(rows[0]["payload"].(string), "password") {
		t.Errorf("event payload: %v", rows)
	}
	// IDInt: aggregate id
	if rows[0]["aggregate_id"] != idStr {
		t.Errorf("aggregate_id=%v", rows[0]["aggregate_id"])
	}
	// өөр org
	mustStatus(t, do(t, app, "GET", "/accounts/"+idStr, f.orgB, 1, nil), 404)
}

func TestIntIDValidation(t *testing.T) {
	f := newFx(t)
	app := mountOn(t, "/accounts", f.accountResource())
	for _, bad := range []string{"abc", "0", "-1", "1.5", "+1", "99999999999999999999", "1e3", " 1", "0x1"} {
		r := do(t, app, "GET", "/accounts/"+url.PathEscape(bad), f.orgA, 1, nil)
		if r.Status != 400 {
			t.Errorf("%q → %d %s", bad, r.Status, r.Raw)
		}
	}
	mustStatus(t, do(t, app, "GET", "/accounts/2147483647", f.orgA, 1, nil), 404)
	c := do(t, app, "POST", "/accounts", f.orgA, 1, map[string]any{"login": "u", "password": "password-1"})
	idStr := fmt.Sprint(int64(c.JSON["id"].(float64)))
	d := do(t, app, "DELETE", "/accounts/"+idStr, f.orgA, 1, nil)
	mustStatus(t, d, 200)
	if d.str("deleted") != idStr {
		t.Errorf("%s", d.Raw)
	}
	mustStatus(t, do(t, app, "GET", "/accounts/"+idStr, f.orgA, 1, nil), 404)
}

func TestNoTimestampsHardDelete(t *testing.T) {
	f := newFx(t)
	app := mountOn(t, "/tags", f.tagResource())
	c := do(t, app, "POST", "/tags", f.orgA, 1, map[string]any{"label": "x"})
	mustStatus(t, c, 201)
	if _, has := c.JSON["created_at"]; has {
		t.Errorf("%s", c.Raw)
	}
	u := do(t, app, "PUT", "/tags/"+c.str("id"), f.orgA, 1, map[string]any{"label": "y"})
	mustStatus(t, u, 200)
	if u.str("label") != "y" {
		t.Errorf("%s", u.Raw)
	}
	mustStatus(t, do(t, app, "DELETE", "/tags/"+c.str("id"), f.orgA, 1, nil), 200)
	if f.count(`SELECT count(*) FROM `+f.tbl("tag")) != 0 {
		t.Error("hard delete")
	}
	// version тохируулаагүй → body-д version шаардахгүй
	mustStatus(t, do(t, app, "PUT", "/tags/"+newUUID(), f.orgA, 1, map[string]any{"label": "z"}), 404)
}

func TestCustomGenerateAndUppercaseID(t *testing.T) {
	f := newFx(t)
	res := f.tagResource()
	fixed := "abcdefab-0000-4000-8000-000000000001"
	res.ID.Generate = func() string { return fixed }
	app := mountOn(t, "/tags", res)
	c := do(t, app, "POST", "/tags", f.orgA, 1, map[string]any{"label": "x"})
	mustStatus(t, c, 201)
	if c.str("id") != fixed {
		t.Errorf("%s", c.Raw)
	}
	mustStatus(t, do(t, app, "GET", "/tags/ABCDEFAB-0000-4000-8000-000000000001", f.orgA, 1, nil), 200)
}

func TestListQueryScopedEvenIfCustomForgetsOrg(t *testing.T) {
	f := newFx(t)
	res := f.tagResource()
	res.ListQuery = func(db *gorm.DB, orgID string) *gorm.DB { return db.Table(f.tbl("tag") + " t") } // org нөхцөл мартсан
	app := mountOn(t, "/tags", res)
	mustStatus(t, do(t, app, "POST", "/tags", f.orgA, 1, map[string]any{"label": "a"}), 201)
	b := do(t, app, "POST", "/tags", f.orgB, 1, map[string]any{"label": "b"})
	mustStatus(t, b, 201)
	l := do(t, app, "GET", "/tags", f.orgA, 1, nil)
	if l.JSON["total"] != float64(1) {
		t.Errorf("crud org нөхцөлийг өөрөө нэмэх ёстой: %s", l.Raw)
	}
	mustStatus(t, do(t, app, "GET", "/tags/"+b.str("id"), f.orgA, 1, nil), 404)
}
