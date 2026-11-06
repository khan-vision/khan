package crud

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/khan-vision/khan/obs"
	"github.com/khan-vision/khan/validate"
)

func (f *fx) outbox(org, topic string) []map[string]any {
	f.t.Helper()
	var rows []map[string]any
	err := f.db.Raw(`SELECT topic, aggregate_type, aggregate_id, payload::text AS payload FROM khan.outbox_events WHERE org_id = ? AND topic = ? ORDER BY id`, org, topic).Scan(&rows).Error
	if err != nil {
		f.t.Fatal(err)
	}
	return rows
}

func TestHooksModifyAndOrder(t *testing.T) {
	f := newFx(t)
	res := f.roomResource()
	var calls []string
	res.Hooks.BeforeCreate = func(x *Ctx, v Values) error {
		calls = append(calls, "bc")
		if x.Tx == nil || x.Action != ActionCreate || x.OrgID != f.orgA || x.Resource != "test.room" || x.ID == "" || x.Old != nil {
			t.Errorf("BeforeCreate ctx: %+v", x)
		}
		v["code"] = "AUTO-1" // дугаар олгох
		v["org_id"] = f.orgB // hook дарж чадахгүй
		return nil
	}
	res.Hooks.AfterCreate = func(x *Ctx, v Values) error {
		calls = append(calls, "ac")
		var n int
		if err := x.Tx.Raw(`SELECT count(*) FROM `+f.tbl("room")+` WHERE id = ?`, x.ID).Scan(&n).Error; err != nil || n != 1 {
			t.Errorf("AfterCreate-д мөр харагдах ёстой: %d %v", n, err)
		}
		return nil
	}
	res.Hooks.BeforeUpdate = func(x *Ctx, v Values) error {
		calls = append(calls, "bu")
		if x.Old == nil || x.Old["name"] != "Анхны" || x.ID == "" {
			t.Errorf("BeforeUpdate Old: %+v", x.Old)
		}
		if x.UserID != "99999999-0000-0000-0000-000000000001" {
			t.Errorf("UserID: %q", x.UserID)
		}
		v["note"] = "hook"
		return nil
	}
	res.Hooks.AfterUpdate = func(x *Ctx, v Values) error { calls = append(calls, "au"); return nil }
	res.Hooks.BeforeDelete = func(x *Ctx) error {
		calls = append(calls, "bd")
		if x.Old["name"] != "Шинэ" {
			t.Errorf("BeforeDelete Old: %v", x.Old)
		}
		return nil
	}
	res.Hooks.AfterDelete = func(x *Ctx) error { calls = append(calls, "ad"); return nil }
	res.Bulk = nil // update hook-тэй resource-д bulk update хориотой (fail-closed)
	app := mountOn(t, "/rooms", res)

	c := do(t, app, "POST", "/rooms", f.orgA, 1, map[string]any{"name": "Анхны"})
	mustStatus(t, c, 201)
	if c.str("code") != "AUTO-1" || c.str("org_id") != f.orgA {
		t.Errorf("hook утга/org: %s", c.Raw)
	}
	u := do(t, app, "PUT", "/rooms/"+c.str("id"), f.orgA, 1, map[string]any{"name": "Шинэ", "version": 1})
	mustStatus(t, u, 200)
	if u.str("created_note") != "hook" {
		t.Errorf("BeforeUpdate нэмсэн утга: %s", u.Raw)
	}
	mustStatus(t, do(t, app, "DELETE", "/rooms/"+c.str("id"), f.orgA, 1, nil), 200)
	if strings.Join(calls, ",") != "bc,ac,bu,au,bd,ad" {
		t.Errorf("hook дараалал: %v", calls)
	}
}

func TestHookErrorMapping(t *testing.T) {
	f := newFx(t)
	var fail error
	res := f.roomResource()
	res.Hooks.BeforeCreate = func(x *Ctx, v Values) error { return fail }
	app := mountOn(t, "/rooms", res)
	secret := "SECRET-DB-DSN-password=hunter2"
	for name, tc := range map[string]struct {
		err    error
		status int
		msg    string
	}{
		"conflict":    {Conflict("Дугаар давхцав"), 409, "Дугаар давхцав"},
		"forbidden":   {Forbidden("Танд эрх алга"), 403, "Танд эрх алга"},
		"bad request": {BadRequest("Буруу хүсэлт"), 400, "Буруу хүсэлт"},
		"not found":   {NotFound("Олдсонгүй"), 404, "Олдсонгүй"},
		"wrapped":     {fmt.Errorf("ctx: %w", Conflict("wrap")), 409, "wrap"},
		"validate":    {func() error { e := validate.NewErrors(); e.Add("name", "Хэт богино"); return e }(), 422, ""},
		"internal":    {errors.New(secret), 500, "Дотоод алдаа гарлаа"},
	} {
		fail = tc.err
		r := do(t, app, "POST", "/rooms", f.orgA, 1, map[string]any{"name": "x"})
		if r.Status != tc.status {
			t.Errorf("%s: %d %s", name, r.Status, r.Raw)
		}
		if tc.msg != "" && r.str("error") != tc.msg {
			t.Errorf("%s: %s", name, r.Raw)
		}
		if strings.Contains(string(r.Raw), "hunter2") {
			t.Errorf("%s: дотоод алдаа body-д алдагдсан: %s", name, r.Raw)
		}
		if name == "validate" && r.errs()["name"][0] != "Хэт богино" {
			t.Errorf("%s", r.Raw)
		}
	}
	if f.count(`SELECT count(*) FROM `+f.tbl("room")) != 0 {
		t.Error("hook алдаанд rollback болоогүй")
	}
}

func TestCrossFieldValidate(t *testing.T) {
	f := newFx(t)
	res := f.roomResource()
	res.Validate = func(x *Ctx, v Values) error {
		if x.Tx != nil {
			t.Error("Validate-д Tx nil байх ёстой")
		}
		e := validate.NewErrors()
		if v["status"] == "closed" && v["capacity"] == nil && x.Action == ActionCreate {
			e.Add("capacity", "Хаагдсан өрөөнд багтаамж заавал")
		}
		if x.Action == ActionUpdate {
			if x.Old == nil {
				t.Error("update Validate-д Old байх ёстой")
			}
			if cap, ok := v["capacity"]; ok && cap != nil && cap.(int64) < 5 && x.Old["capacity"] == nil {
				e.Add("capacity", "5-аас бага")
			}
		}
		return e.Err()
	}
	res.Bulk = nil // Validate-тэй resource-д bulk update хориотой (fail-closed)
	app := mountOn(t, "/rooms", res)
	r := do(t, app, "POST", "/rooms", f.orgA, 1, map[string]any{"name": "a", "status": "closed"})
	mustStatus(t, r, 422)
	if r.errs()["capacity"][0] != "Хаагдсан өрөөнд багтаамж заавал" {
		t.Errorf("%s", r.Raw)
	}
	// field алдаатай үед cross-field дуудагдахгүй (хэсэгчилсэн утга)
	called := false
	res2 := f.roomResource()
	res2.Validate = func(x *Ctx, v Values) error { called = true; return nil }
	res2.Bulk = nil
	app2 := mountOn(t, "/rooms", res2)
	mustStatus(t, do(t, app2, "POST", "/rooms", f.orgA, 1, map[string]any{"name": ""}), 422)
	if called {
		t.Error("field алдаатай үед Validate дуудагдсан")
	}
	c := do(t, app, "POST", "/rooms", f.orgA, 1, map[string]any{"name": "ok", "status": "open"})
	u := do(t, app, "PUT", "/rooms/"+c.str("id"), f.orgA, 1, map[string]any{"capacity": 3, "version": 1})
	mustStatus(t, u, 422)
	// field + cross-field + ref алдаа нэг хариунд нэгдэнэ
	res3 := f.roomResource()
	res3.Validate = func(x *Ctx, v Values) error { e := validate.NewErrors(); e.Add("name", "cross"); return e }
	res3.Bulk = nil
	app3 := mountOn(t, "/rooms", res3)
	r = do(t, app3, "POST", "/rooms", f.orgA, 1, map[string]any{"name": "a", "owner_id": newUUID()})
	mustStatus(t, r, 422)
	if r.errs()["name"] == nil || r.errs()["owner_id"] == nil {
		t.Errorf("%v", r.errs())
	}
}

func TestAfterHookErrorRollsBackEverything(t *testing.T) {
	f := newFx(t)
	res := f.invoiceResource()
	res.Hooks.AfterCreate = func(x *Ctx, v Values) error { return errors.New("after boom") }
	res.Hooks.AfterUpdate = func(x *Ctx, v Values) error { return errors.New("after boom") }
	res.Hooks.AfterDelete = func(x *Ctx) error { return errors.New("after boom") }
	app := mountOn(t, "/invoices", res)
	item := f.person(f.orgA, "Бараа", 1)

	r := do(t, app, "POST", "/invoices", f.orgA, 1, map[string]any{"number": "R1", "lines": []any{line(item, 1, 1)}, "notes": []any{map[string]any{"body": "n"}}})
	mustStatus(t, r, 500)
	if r.str("error") != "Дотоод алдаа гарлаа" || strings.Contains(string(r.Raw), "boom") {
		t.Errorf("%s", r.Raw)
	}
	for _, tbl := range []string{"invoice", "invoice_line", "invoice_note"} {
		if f.count(`SELECT count(*) FROM `+f.tbl(tbl)) != 0 {
			t.Errorf("%s rollback болоогүй", tbl)
		}
	}
	if len(f.outbox(f.orgA, "test.invoice.created")) != 0 {
		t.Error("event rollback болоогүй")
	}

	// update/delete-ийн after алдаа: өөрчлөлт буцна
	ok := f.invoiceResource()
	appOK := mountOn(t, "/invoices", ok)
	c := do(t, appOK, "POST", "/invoices", f.orgA, 1, map[string]any{"number": "R2", "lines": []any{line(item, 1, 1)}})
	mustStatus(t, c, 201)
	r = do(t, app, "PUT", "/invoices/"+c.str("id"), f.orgA, 1, map[string]any{"number": "R2b", "version": 1, "lines": []any{}})
	mustStatus(t, r, 500)
	if f.count(`SELECT count(*) FROM `+f.tbl("invoice")+` WHERE number = 'R2' AND version = 1`) != 1 || f.count(`SELECT count(*) FROM `+f.tbl("invoice_line")+` WHERE deleted_at IS NULL`) != 1 {
		t.Error("update rollback болоогүй (parent эсвэл children)")
	}
	if len(f.outbox(f.orgA, "test.invoice.updated")) != 0 {
		t.Error("updated event rollback")
	}
	mustStatus(t, do(t, app, "DELETE", "/invoices/"+c.str("id"), f.orgA, 1, nil), 500)
	if f.count(`SELECT count(*) FROM `+f.tbl("invoice")+` WHERE id = ?`, c.str("id")) != 1 || f.count(`SELECT count(*) FROM `+f.tbl("invoice_line")) != 1 {
		t.Error("delete rollback болоогүй (children)")
	}
}

func TestEvents(t *testing.T) {
	f := newFx(t)
	app := mountOn(t, "/rooms", f.roomResource())
	c := do(t, app, "POST", "/rooms", f.orgA, 1, map[string]any{"name": "ev", "capacity": 5})
	id := c.str("id")
	mustStatus(t, do(t, app, "PUT", "/rooms/"+id, f.orgA, 1, map[string]any{"capacity": 6, "version": 1}), 200)
	mustStatus(t, do(t, app, "DELETE", "/rooms/"+id, f.orgA, 1, nil), 200)

	for topic, check := range map[string]func(p map[string]any){
		"test.room.created": func(p map[string]any) {
			if p["values"].(map[string]any)["name"] != "ev" || p["values"].(map[string]any)["capacity"] != float64(5) {
				t.Errorf("created payload: %v", p)
			}
		},
		"test.room.updated": func(p map[string]any) {
			v := p["values"].(map[string]any)
			if v["capacity"] != float64(6) || v["name"] != nil {
				t.Errorf("updated payload зөвхөн өөрчлөгдсөн утга: %v", p)
			}
		},
		"test.room.deleted": func(p map[string]any) {
			if _, has := p["values"]; has {
				t.Errorf("deleted payload: %v", p)
			}
		},
	} {
		rows := f.outbox(f.orgA, topic)
		if len(rows) != 1 || rows[0]["aggregate_id"] != id || rows[0]["aggregate_type"] != "test.room" {
			t.Errorf("%s: %v", topic, rows)
			continue
		}
		var p map[string]any
		_ = json.Unmarshal([]byte(rows[0]["payload"].(string)), &p)
		if p["id"] != id || p["org_id"] != f.orgA {
			t.Errorf("%s payload: %v", topic, p)
		}
		check(p)
	}
	// Events=false → event бичигдэхгүй; алдаатай хүсэлт event бичихгүй
	before := f.count(`SELECT count(*) FROM khan.outbox_events WHERE org_id = ?`, f.orgA)
	mustStatus(t, do(t, app, "POST", "/rooms", f.orgA, 1, map[string]any{"name": ""}), 422)
	res := f.roomResource()
	res.Events = false
	app2 := mountOn(t, "/rooms", res)
	mustStatus(t, do(t, app2, "POST", "/rooms", f.orgA, 1, map[string]any{"name": "quiet"}), 201)
	if after := f.count(`SELECT count(*) FROM khan.outbox_events WHERE org_id = ?`, f.orgA); after != before {
		t.Errorf("event нэмэгдсэн: %d → %d", before, after)
	}
}

func TestRLSPathSetsOrgInTx(t *testing.T) {
	f := newFx(t)
	res := f.roomResource()
	res.RLS = true
	var seen []string
	var mu sync.Mutex
	res.Hooks.BeforeCreate = func(x *Ctx, v Values) error {
		var org string
		if err := x.Tx.Raw(`SELECT current_setting('app.org_id', true)`).Scan(&org).Error; err != nil {
			return err
		}
		mu.Lock()
		seen = append(seen, org)
		mu.Unlock()
		return nil
	}
	app := mountOn(t, "/rooms", res)
	c := do(t, app, "POST", "/rooms", f.orgA, 1, map[string]any{"name": "rls"})
	mustStatus(t, c, 201)
	if len(seen) != 1 || seen[0] != f.orgA {
		t.Errorf("tx-д app.org_id тавигдаагүй: %v", seen)
	}
	id := c.str("id")
	mustStatus(t, do(t, app, "GET", "/rooms/"+id, f.orgA, 1, nil), 200)
	mustStatus(t, do(t, app, "PUT", "/rooms/"+id, f.orgA, 1, map[string]any{"name": "rls2", "version": 1}), 200)
	mustStatus(t, do(t, app, "GET", "/rooms/"+id, f.orgB, 1, nil), 404)
	bk := do(t, app, "POST", "/rooms/bulk", f.orgA, 1, map[string]any{"action": "update", "ids": []string{id}, "set": map[string]any{"capacity": 1}})
	if bk.Status != 200 || bk.JSON["affected"] != float64(1) {
		t.Errorf("bulk RLS: %s", bk.Raw)
	}
	mustStatus(t, do(t, app, "DELETE", "/rooms/"+id, f.orgA, 1, nil), 200)
	// tx-ийн setting дараагийн хүсэлт рүү алдагдахгүй (SET LOCAL)
	var leaked string
	f.db.Raw(`SELECT coalesce(current_setting('app.org_id', true), '')`).Scan(&leaked)
	if leaked != "" {
		t.Errorf("app.org_id connection-д үлдсэн: %q", leaked)
	}
}

func TestInternalErrorLoggedNotLeaked(t *testing.T) {
	f := newFx(t)
	res := f.roomResource()
	res.ListQuery = func(db *gorm.DB, orgID string) *gorm.DB {
		return db.Table(f.tbl("no_such_table")+" t").Where("t.org_id = ?", orgID)
	}
	var buf bytes.Buffer
	var mu sync.Mutex
	logger := slog.New(slog.NewTextHandler(&lockedWriter{&buf, &mu}, nil))
	app := fiber.New()
	app.Use(obs.Middleware(logger))
	g := app.Group("/rooms", claimsMW)
	if err := res.Mount(g); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/rooms", "/rooms/query"} {
		m := "GET"
		if strings.HasSuffix(p, "query") {
			m = "POST"
		}
		r := do(t, app, m, p, f.orgA, 1, nil)
		if r.Status != 500 || r.str("error") != "Дотоод алдаа гарлаа" {
			t.Errorf("%s: %d %s", p, r.Status, r.Raw)
		}
		if strings.Contains(string(r.Raw), "no_such_table") || strings.Contains(strings.ToLower(string(r.Raw)), "relation") {
			t.Errorf("DB алдаа body-д алдагдсан: %s", r.Raw)
		}
	}
	mu.Lock()
	logged := buf.String()
	mu.Unlock()
	if !strings.Contains(logged, "no_such_table") || !strings.Contains(logged, "resource=test.room") {
		t.Errorf("жинхэнэ алдаа лог руу орох ёстой: %s", logged)
	}
}

type lockedWriter struct {
	b  *bytes.Buffer
	mu *sync.Mutex
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}
