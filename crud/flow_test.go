package crud

import (
	"fmt"
	"strings"
	"testing"
)

func TestRoomCRUDFlow(t *testing.T) {
	f := newFx(t)
	app := mountOn(t, "/rooms", f.roomResource())
	owner := f.person(f.orgA, "Эзэн", 1)
	bogusID := newUUID()

	r := do(t, app, "POST", "/rooms", f.orgA, 1, map[string]any{
		"name": "  Зал А ", "code": "A-1", "capacity": "30", "price": "1250.5", "opens": "2026-03-01",
		"meta": map[string]any{"floor": 2}, "owner_id": strings.ToUpper(owner), "created_note": "анх",
		// хаах ёстой түлхүүрүүд:
		"org_id": f.orgB, "id": bogusID, "version": 99, "created_at": "2000-01-01", "deleted_at": "2000-01-01",
		"created_by": "x", "bogus": 1,
	})
	mustStatus(t, r, 201)
	id := r.str("id")
	if id == "" || id == bogusID {
		t.Fatalf("id сервер үүсгэх ёстой: %s", r.Raw)
	}
	for k, want := range map[string]any{
		"name": "Зал А", "code": "A-1", "capacity": float64(30), "price": 1250.5, "active": true, "status": "draft",
		"opens": "2026-03-01", "owner_id": owner, "created_note": "анх", "version": float64(1), "org_id": f.orgA,
		"created_by": "99999999-0000-0000-0000-000000000001",
	} {
		if r.JSON[k] != want {
			t.Errorf("%s=%v (%T), хүлээсэн %v", k, r.JSON[k], r.JSON[k], want)
		}
	}
	if m, _ := r.JSON["meta"].(map[string]any); m["floor"] != float64(2) {
		t.Errorf("meta=%v", r.JSON["meta"])
	}
	if _, has := r.JSON["note"]; has {
		t.Error("Column нэр (note) биш Name (created_note) буцах ёстой")
	}
	if r.JSON["created_at"] == nil || r.JSON["updated_at"] == nil || r.JSON["deleted_at"] != nil {
		t.Errorf("timestamps: %s", r.Raw)
	}
	if n := f.count(`SELECT count(*) FROM `+f.tbl("room")+` WHERE org_id = ?`, f.orgB); n != 0 {
		t.Fatal("body org_id нөлөөлсөн!")
	}
	if got := f.count(`SELECT count(*) FROM ` + f.tbl("room") + ` WHERE price::text = '1250.50'`); got != 1 {
		t.Error("Money яг 1250.50 хадгалагдах ёстой")
	}

	// GET
	g := do(t, app, "GET", "/rooms/"+id, f.orgA, 2, nil)
	mustStatus(t, g, 200)
	if string(g.Raw) != string(r.Raw) {
		t.Errorf("GET ≠ create хариу:\n%s\n%s", g.Raw, r.Raw)
	}
	mustStatus(t, do(t, app, "GET", "/rooms/"+strings.ToUpper(id), f.orgA, 2, nil), 200) // том үсэг зөвшөөрөгдөнө

	// list
	l := do(t, app, "GET", "/rooms", f.orgA, 2, nil)
	mustStatus(t, l, 200)
	if l.JSON["total"] != float64(1) || len(l.list("data")) != 1 {
		t.Fatalf("list: %s", l.Raw)
	}
	can := l.JSON["can"].(map[string]any)
	if can["read"] != true || can["create"] != false || can["delete"] != false {
		t.Errorf("can (role 2): %v", can)
	}
	if _, ok := l.JSON["filterable"].(map[string]any)["status"]; !ok {
		t.Errorf("filterable: %s", l.Raw)
	}

	// partial update
	u := do(t, app, "PUT", "/rooms/"+id, f.orgA, 1, map[string]any{
		"name": "Зал Б", "version": 1, "capacity": nil, "created_note": "өөр", "org_id": f.orgB, "id": bogusID,
	})
	mustStatus(t, u, 200)
	if u.str("name") != "Зал Б" || u.JSON["capacity"] != nil || u.str("code") != "A-1" || u.JSON["price"] != 1250.5 ||
		u.JSON["version"] != float64(2) || u.str("created_note") != "анх" || u.str("id") != id || u.str("org_id") != f.orgA {
		t.Errorf("update: %s", u.Raw)
	}
	if u.str("updated_at") == r.str("updated_at") && u.str("updated_at") == "" {
		t.Error("updated_at")
	}

	// version
	mustStatus(t, do(t, app, "PUT", "/rooms/"+id, f.orgA, 1, map[string]any{"name": "Дахин", "version": 1}), 409)
	v := do(t, app, "PUT", "/rooms/"+id, f.orgA, 1, map[string]any{"name": "Дахин"})
	mustStatus(t, v, 422)
	if v.errs()["version"] == nil {
		t.Errorf("version заавал: %s", v.Raw)
	}
	mustStatus(t, do(t, app, "PUT", "/rooms/"+id, f.orgA, 1, map[string]any{"name": "Дахин", "version": "x"}), 422)
	ok := do(t, app, "PUT", "/rooms/"+id, f.orgA, 1, map[string]any{"code": "", "version": "2"}) // string version, nullable "" → NULL
	mustStatus(t, ok, 200)
	if ok.JSON["code"] != nil || ok.JSON["version"] != float64(3) || ok.str("name") != "Зал Б" {
		t.Errorf("%s", ok.Raw)
	}
	if body := do(t, app, "PUT", "/rooms/"+id, f.orgA, 1, map[string]any{"version": 3}); body.Status != 200 || body.JSON["version"] != float64(4) {
		t.Errorf("хоосон засвар version нэмэгдэх ёстой: %s", body.Raw)
	}

	// delete (soft)
	d := do(t, app, "DELETE", "/rooms/"+id, f.orgA, 1, nil)
	mustStatus(t, d, 200)
	if d.str("deleted") != id {
		t.Errorf("%s", d.Raw)
	}
	for _, m := range []string{"GET", "DELETE"} {
		x := do(t, app, m, "/rooms/"+id, f.orgA, 1, nil)
		mustStatus(t, x, 404)
		if x.str("error") != "Өрөө олдсонгүй" {
			t.Errorf("%s 404 мессеж: %s", m, x.Raw)
		}
	}
	mustStatus(t, do(t, app, "PUT", "/rooms/"+id, f.orgA, 1, map[string]any{"name": "x", "version": 4}), 404)
	l = do(t, app, "GET", "/rooms", f.orgA, 1, nil)
	if l.JSON["total"] != float64(0) {
		t.Errorf("устсан мөр жагсаалтад байж болохгүй: %s", l.Raw)
	}
	if n := f.count(`SELECT count(*) FROM `+f.tbl("room")+` WHERE id = ? AND deleted_at IS NOT NULL`, id); n != 1 {
		t.Error("soft delete: мөр DB-д үлдэх ёстой")
	}
	// soft-deleted-ийн кодыг дахин ашиглаж болно
	mustStatus(t, do(t, app, "POST", "/rooms", f.orgA, 1, map[string]any{"name": "Шинэ", "code": "A-1"}), 201)
}

func TestValidationErrors422(t *testing.T) {
	f := newFx(t)
	app := mountOn(t, "/rooms", f.roomResource())
	r := do(t, app, "POST", "/rooms", f.orgA, 1, map[string]any{
		"name": "", "code": "abc", "capacity": -1, "price": "1.234", "opens": "2026-02-30", "status": "x", "owner_id": "zzz",
		"meta": "not-json-object-but-valid-json-string",
	})
	mustStatus(t, r, 422)
	if r.str("error") != "Мэдээлэл буруу" {
		t.Errorf("error: %s", r.Raw)
	}
	e := r.errs()
	for _, k := range []string{"name", "code", "capacity", "price", "opens", "status", "owner_id"} {
		if len(e[k]) == 0 {
			t.Errorf("%s-д алдаа алга: %s", k, r.Raw)
		}
	}
	if e["code"][0] != "Код зөвхөн том латин үсэг, тоо" || e["name"][0] != "Нэр заавал" {
		t.Errorf("мессеж: %v", e)
	}
	if len(e["meta"]) != 0 {
		t.Errorf("meta дурын JSON: %v", e["meta"])
	}
	if f.count(`SELECT count(*) FROM `+f.tbl("room")) != 0 {
		t.Error("алдаатай хүсэлт DB-д бичигдсэн")
	}

	// Required: key огт байхгүй
	r = do(t, app, "POST", "/rooms", f.orgA, 1, map[string]any{})
	mustStatus(t, r, 422)
	if len(r.errs()) != 1 || r.errs()["name"][0] != "Нэр заавал" {
		t.Errorf("%v", r.errs())
	}
	// Update: Required талбар ирсэн бол хоосон байж болохгүй
	c := do(t, app, "POST", "/rooms", f.orgA, 1, map[string]any{"name": "ok"})
	mustStatus(t, c, 201)
	u := do(t, app, "PUT", "/rooms/"+c.str("id"), f.orgA, 1, map[string]any{"name": "  ", "version": 1})
	mustStatus(t, u, 422)
	if u.errs()["name"] == nil {
		t.Errorf("%s", u.Raw)
	}
}

func TestStrictUnknown(t *testing.T) {
	f := newFx(t)
	res := f.roomResource()
	res.StrictUnknown = true
	app := mountOn(t, "/rooms", res)
	r := do(t, app, "POST", "/rooms", f.orgA, 1, map[string]any{"name": "a", "bogus": 1, "org_id": f.orgB})
	mustStatus(t, r, 422)
	e := r.errs()
	if e["bogus"] == nil || e["org_id"] == nil || len(e) != 2 {
		t.Errorf("%v", e)
	}
	// version (зарлагдсан түлхүүр) strict-д зөвшөөрөгдөнө
	ok := do(t, app, "POST", "/rooms", f.orgA, 1, map[string]any{"name": "a", "version": 1})
	mustStatus(t, ok, 201)
	mustStatus(t, do(t, app, "PUT", "/rooms/"+ok.str("id"), f.orgA, 1, map[string]any{"name": "b", "version": 1}), 200)
	r = do(t, app, "PUT", "/rooms/"+ok.str("id"), f.orgA, 1, map[string]any{"name": "b", "version": 2, "zzz": true})
	mustStatus(t, r, 422)
}

func TestCreateOnlyAndDefaults(t *testing.T) {
	f := newFx(t)
	app := mountOn(t, "/rooms", f.roomResource())
	c := do(t, app, "POST", "/rooms", f.orgA, 1, map[string]any{"name": "a", "active": nil, "status": "", "created_note": "n1"})
	mustStatus(t, c, 201)
	// null/"" non-nullable → "илгээгээгүй" → Default
	if c.JSON["active"] != true || c.JSON["status"] != "draft" {
		t.Errorf("default: %s", c.Raw)
	}
	u := do(t, app, "PUT", "/rooms/"+c.str("id"), f.orgA, 1, map[string]any{"created_note": "n2", "active": false, "version": 1})
	mustStatus(t, u, 200)
	if u.str("created_note") != "n1" || u.JSON["active"] != false {
		t.Errorf("CreateOnly update-д үл тооно: %s", u.Raw)
	}
	// 0/1 bool
	u = do(t, app, "PUT", "/rooms/"+c.str("id"), f.orgA, 1, map[string]any{"active": 1, "version": 2})
	if u.JSON["active"] != true {
		t.Errorf("%s", u.Raw)
	}
}

func TestUniqueField(t *testing.T) {
	f := newFx(t)
	app := mountOn(t, "/rooms", f.roomResource())
	a := do(t, app, "POST", "/rooms", f.orgA, 1, map[string]any{"name": "a", "code": "X-1"})
	mustStatus(t, a, 201)
	dup := do(t, app, "POST", "/rooms", f.orgA, 1, map[string]any{"name": "b", "code": "X-1"})
	mustStatus(t, dup, 422)
	if dup.errs()["code"][0] != "Код давхардсан байна" {
		t.Errorf("%s", dup.Raw)
	}
	// өөр org-д ижил код зөвшөөрөгдөнө
	mustStatus(t, do(t, app, "POST", "/rooms", f.orgB, 1, map[string]any{"name": "b", "code": "X-1"}), 201)
	// өөрийн кодоо дахин илгээх нь давхардал биш
	mustStatus(t, do(t, app, "PUT", "/rooms/"+a.str("id"), f.orgA, 1, map[string]any{"code": "X-1", "version": 1}), 200)
	// өөр мөрийн код руу засах
	b := do(t, app, "POST", "/rooms", f.orgA, 1, map[string]any{"name": "c", "code": "X-2"})
	r := do(t, app, "PUT", "/rooms/"+b.str("id"), f.orgA, 1, map[string]any{"code": "X-1", "version": 1})
	mustStatus(t, r, 422)
	if r.errs()["code"] == nil {
		t.Errorf("%s", r.Raw)
	}
	// NULL код давхардал биш
	mustStatus(t, do(t, app, "POST", "/rooms", f.orgA, 1, map[string]any{"name": "n1"}), 201)
	mustStatus(t, do(t, app, "POST", "/rooms", f.orgA, 1, map[string]any{"name": "n2"}), 201)
}

func TestUniqueRaceMapsTo409(t *testing.T) {
	f := newFx(t)
	res := f.roomResource()
	// Уралдааныг дууриах: апп-ийн шалгалтыг давсны дараа өөр транзакц ижил кодыг оруулсан.
	res.Hooks.BeforeCreate = func(x *Ctx, v Values) error {
		return x.Tx.Exec(`INSERT INTO `+f.tbl("room")+` (id, org_id, name, code) VALUES (?, ?, 'race', ?)`, newUUID(), x.OrgID, v["code"]).Error
	}
	app := mountOn(t, "/rooms", res)
	r := do(t, app, "POST", "/rooms", f.orgA, 1, map[string]any{"name": "a", "code": "RACE-1"})
	mustStatus(t, r, 409)
	if r.str("error") != "Ийм утгатай мөр аль хэдийн байна" || strings.Contains(string(r.Raw), "room_code_uq") || strings.Contains(string(r.Raw), "duplicate") {
		t.Errorf("constraint-ийн дэлгэрэнгүй алдагдсан: %s", r.Raw)
	}
	if f.count(`SELECT count(*) FROM `+f.tbl("room")+` WHERE code = 'RACE-1'`) != 0 {
		t.Error("rollback болоогүй")
	}
}

func TestRefValidation(t *testing.T) {
	f := newFx(t)
	app := mountOn(t, "/rooms", f.roomResource())
	mine := f.person(f.orgA, "Миний", 1)
	foreign := f.person(f.orgB, "Бусдын", 1)
	gone := f.person(f.orgA, "Устсан", 1)
	f.exec(`UPDATE `+f.tbl("person")+` SET deleted_at = now() WHERE id = ?`, gone)

	mustStatus(t, do(t, app, "POST", "/rooms", f.orgA, 1, map[string]any{"name": "a", "owner_id": mine}), 201)
	for name, id := range map[string]string{"өөр org": foreign, "устсан": gone, "байхгүй": newUUID()} {
		r := do(t, app, "POST", "/rooms", f.orgA, 1, map[string]any{"name": "a", "owner_id": id})
		mustStatus(t, r, 422)
		if r.errs()["owner_id"][0] != "Эзэмшигч олдсонгүй" {
			t.Errorf("%s: %s", name, r.Raw)
		}
	}
	// update: ref өөр org-ийнх
	ok := do(t, app, "POST", "/rooms", f.orgA, 1, map[string]any{"name": "a"})
	r := do(t, app, "PUT", "/rooms/"+ok.str("id"), f.orgA, 1, map[string]any{"owner_id": foreign, "version": 1})
	mustStatus(t, r, 422)
	// ref null зөвшөөрөгдөнө
	mustStatus(t, do(t, app, "PUT", "/rooms/"+ok.str("id"), f.orgA, 1, map[string]any{"owner_id": nil, "version": 1}), 200)
	// ref-ийг шалгахгүй байсан бол тухайн мөрд FK ямар ч байсан хадгалагдах байсан: DB-д орсонгүй
	if f.count(`SELECT count(*) FROM `+f.tbl("room")+` WHERE owner_id = ?`, foreign) != 0 {
		t.Error("өөр org-ийн ref хадгалагдсан")
	}
}

func TestIDValidationAndBodyErrors(t *testing.T) {
	f := newFx(t)
	app := mountOn(t, "/rooms", f.roomResource())
	for _, bad := range []string{"abc", "123", "12345678-1234-1234-1234-12345678901g", "12345678123412341234123456789012", "%20"} {
		for _, m := range []string{"GET", "PUT", "DELETE"} {
			body := any(nil)
			if m == "PUT" {
				body = `{"version":1}`
			}
			r := do(t, app, m, "/rooms/"+bad, f.orgA, 1, body)
			if r.Status != 400 || r.str("error") != "id буруу" {
				t.Errorf("%s %s → %d %s", m, bad, r.Status, r.Raw)
			}
		}
	}
	for name, body := range map[string]any{
		"хоосон": "", "invalid json": "{", "array": "[1]", "string": `"x"`, "null": "null", "number": "5",
		"trailing": `{"name":"a"} {"x":1}`, "trailing junk": `{"name":"a"}xx`,
	} {
		r := do(t, app, "POST", "/rooms", f.orgA, 1, body)
		if r.Status != 400 || r.str("error") != "Хүсэлтийн JSON буруу" {
			t.Errorf("%s → %d %s", name, r.Status, r.Raw)
		}
	}
	// хэт том body
	res := f.roomResource()
	res.MaxBody = 64
	small := mountOn(t, "/rooms", res)
	r := do(t, small, "POST", "/rooms", f.orgA, 1, fmt.Sprintf(`{"name":"%s"}`, strings.Repeat("a", 100)))
	if r.Status != 400 || r.str("error") != "Хүсэлтийн хэмжээ хэт том" {
		t.Errorf("%d %s", r.Status, r.Raw)
	}
	mustStatus(t, do(t, small, "POST", "/rooms", f.orgA, 1, `{"name":"ok"}`), 201)
	// нэг түлхүүрийн давхардал: сүүлийнх нь
	mustStatus(t, do(t, app, "POST", "/rooms", f.orgA, 1, `{"name":"a","name":"b"}`), 201)
}

func TestTypesRoundTrip(t *testing.T) {
	f := newFx(t)
	app := mountOn(t, "/rooms", f.roomResource())
	c := do(t, app, "POST", "/rooms", f.orgA, 1, map[string]any{
		"name": "t", "price": 0, "opens": "2028-02-29", "meta": []any{1, "a", map[string]any{"b": nil}},
		"status": "closed", "capacity": 100000,
	})
	mustStatus(t, c, 201)
	if c.JSON["price"] != float64(0) || c.JSON["opens"] != "2028-02-29" || c.JSON["status"] != "closed" || c.JSON["capacity"] != float64(100000) {
		t.Errorf("%s", c.Raw)
	}
	if arr, ok := c.JSON["meta"].([]any); !ok || len(arr) != 3 {
		t.Errorf("meta массив хэвээр: %s", c.Raw)
	}
	for _, bad := range []map[string]any{{"capacity": 100001}, {"price": -0.01}, {"price": "1e16"}} {
		bad["name"] = "t"
		mustStatus(t, do(t, app, "POST", "/rooms", f.orgA, 1, bad), 422)
	}
}
