package crud

import (
	"strings"
	"testing"
)

func (f *fx) lines(invoice string) []map[string]any {
	f.t.Helper()
	var rows []map[string]any
	if err := f.db.Raw(`SELECT id::text AS id, qty, position, deleted_at IS NOT NULL AS gone FROM `+f.tbl("invoice_line")+` WHERE invoice_id = ? ORDER BY position, id`, invoice).Scan(&rows).Error; err != nil {
		f.t.Fatal(err)
	}
	return rows
}

func line(item string, qty, pos int) map[string]any {
	return map[string]any{"item_id": item, "qty": qty, "price": "10.5", "position": pos}
}

func TestChildrenLifecycle(t *testing.T) {
	f := newFx(t)
	app := mountOn(t, "/invoices", f.invoiceResource())
	item := f.person(f.orgA, "Бараа", 1)
	foreignItem := f.person(f.orgB, "Бусдын бараа", 1)

	// create + children (id нь үл тооно)
	bogus := newUUID()
	l2 := line(item, 2, 2)
	l2["id"] = bogus
	l2["org_id"] = f.orgB
	l2["invoice_id"] = newUUID()
	c := do(t, app, "POST", "/invoices", f.orgA, 1, map[string]any{
		"number": "INV-1", "lines": []any{l2, line(item, 1, 1)}, "notes": []any{map[string]any{"body": "эхний"}},
	})
	mustStatus(t, c, 201)
	inv := c.str("id")
	lines := c.list("lines")
	if len(lines) != 2 || lines[0]["position"] != float64(1) || lines[1]["position"] != float64(2) {
		t.Fatalf("lines position-оор эрэмбэлэгдэх ёстой: %s", c.Raw)
	}
	if lines[1]["id"] == bogus || lines[1]["org_id"] != f.orgA || lines[1]["invoice_id"] != inv {
		t.Errorf("child id/org/fk сервер тавина: %v", lines[1])
	}
	if len(c.list("notes")) != 1 || c.list("notes")[0]["body"] != "эхний" {
		t.Errorf("%s", c.Raw)
	}
	if f.count(`SELECT count(*) FROM `+f.tbl("invoice_line")+` WHERE org_id = ?`, f.orgB) != 0 {
		t.Fatal("child body org_id нөлөөлсөн")
	}
	id1, id2 := lines[0]["id"].(string), lines[1]["id"].(string)

	// GET дээр children байна
	g := do(t, app, "GET", "/invoices/"+inv, f.orgA, 2, nil)
	if len(g.list("lines")) != 2 {
		t.Errorf("%s", g.Raw)
	}

	// replace: id1 засна, id2 жагсаалтад үгүй (устна), шинэ мөр нэмнэ; notes key алга → өөрчлөгдөхгүй
	u := do(t, app, "PUT", "/invoices/"+inv, f.orgA, 1, map[string]any{
		"version": 1,
		"lines":   []any{map[string]any{"id": id1, "qty": 5}, line(item, 9, 3)},
	})
	mustStatus(t, u, 200)
	got := u.list("lines")
	if len(got) != 2 || got[0]["id"] != id1 || got[0]["qty"] != float64(5) || got[0]["price"] != 10.5 || got[1]["qty"] != float64(9) {
		t.Fatalf("replace: %s", u.Raw)
	}
	if len(u.list("notes")) != 1 {
		t.Errorf("notes өөрчлөгдөх ёсгүй: %s", u.Raw)
	}
	if u.JSON["version"] != float64(2) {
		t.Errorf("version: %s", u.Raw)
	}
	rows := f.lines(inv)
	if len(rows) != 3 { // id2 soft-deleted үлдсэн
		t.Fatalf("%v", rows)
	}
	var softGone int
	for _, r := range rows {
		if r["id"] == id2 && r["gone"] == true {
			softGone++
		}
	}
	if softGone != 1 {
		t.Errorf("child soft delete: %v", rows)
	}

	// хоосон массив → бүх мөр устна; null → өөрчлөгдөхгүй
	u = do(t, app, "PUT", "/invoices/"+inv, f.orgA, 1, map[string]any{"version": 2, "lines": nil, "notes": []any{}})
	mustStatus(t, u, 200)
	if len(u.list("lines")) != 2 || len(u.list("notes")) != 0 {
		t.Errorf("null=өөрчлөхгүй, []=бүгдийг устгана: %s", u.Raw)
	}
	u = do(t, app, "PUT", "/invoices/"+inv, f.orgA, 1, map[string]any{"version": 3, "lines": []any{}})
	mustStatus(t, u, 200)
	if len(u.list("lines")) != 0 || u.JSON["lines"] == nil {
		t.Errorf("%s", u.Raw)
	}
	if f.count(`SELECT count(*) FROM `+f.tbl("invoice_note")+` WHERE invoice_id = ?`, inv) != 0 {
		t.Error("notes hard delete")
	}
	_ = foreignItem
}

func TestChildrenValidation(t *testing.T) {
	f := newFx(t)
	app := mountOn(t, "/invoices", f.invoiceResource())
	item := f.person(f.orgA, "Бараа", 1)
	foreignItem := f.person(f.orgB, "Бусдын", 1)

	// child талбарын алдаа: lines.<i>.<field>
	r := do(t, app, "POST", "/invoices", f.orgA, 1, map[string]any{"number": "I", "lines": []any{
		line(item, 1, 1), map[string]any{"item_id": "zzz", "qty": 0},
	}})
	mustStatus(t, r, 422)
	e := r.errs()
	if e["lines.1.item_id"] == nil || e["lines.1.qty"] == nil || e["lines.1.price"] == nil || e["lines.0.qty"] != nil {
		t.Errorf("%v", e)
	}
	// ref өөр org (tx дотор шалгагдана)
	r = do(t, app, "POST", "/invoices", f.orgA, 1, map[string]any{"number": "I", "lines": []any{line(item, 1, 1), line(foreignItem, 1, 2)}})
	mustStatus(t, r, 422)
	if e := r.errs(); e["lines.1.item_id"][0] != "Бараа олдсонгүй" || e["lines.0.item_id"] != nil {
		t.Errorf("%v", e)
	}
	if f.count(`SELECT count(*) FROM `+f.tbl("invoice")) != 0 || f.count(`SELECT count(*) FROM `+f.tbl("invoice_line")) != 0 {
		t.Error("алдаатай хүсэлт бичигдсэн")
	}
	// MaxRows 3
	four := []any{line(item, 1, 1), line(item, 1, 2), line(item, 1, 3), line(item, 1, 4)}
	r = do(t, app, "POST", "/invoices", f.orgA, 1, map[string]any{"number": "I", "lines": four})
	mustStatus(t, r, 422)
	if r.errs()["lines"] == nil || !strings.Contains(r.errs()["lines"][0], "3") {
		t.Errorf("%v", r.errs())
	}
	// массив биш / мөр объект биш
	mustStatus(t, do(t, app, "POST", "/invoices", f.orgA, 1, map[string]any{"number": "I", "lines": "x"}), 422)
	r = do(t, app, "POST", "/invoices", f.orgA, 1, map[string]any{"number": "I", "lines": []any{5}})
	mustStatus(t, r, 422)
	if r.errs()["lines.0"] == nil {
		t.Errorf("%v", r.errs())
	}

	// foreign child id: өөр эцэг, өөр org
	mine := do(t, app, "POST", "/invoices", f.orgA, 1, map[string]any{"number": "M", "lines": []any{line(item, 1, 1)}})
	other := do(t, app, "POST", "/invoices", f.orgA, 1, map[string]any{"number": "O", "lines": []any{line(item, 7, 1)}})
	bInv := do(t, app, "POST", "/invoices", f.orgB, 1, map[string]any{"number": "B", "lines": []any{line(foreignItem, 3, 1)}})
	otherLine := other.list("lines")[0]["id"].(string)
	bLine := bInv.list("lines")[0]["id"].(string)
	for name, lid := range map[string]string{"өөр эцгийн": otherLine, "өөр org-ийн": bLine, "байхгүй": newUUID()} {
		r := do(t, app, "PUT", "/invoices/"+mine.str("id"), f.orgA, 1, map[string]any{"version": 1, "lines": []any{map[string]any{"id": lid, "qty": 99}}})
		mustStatus(t, r, 422)
		if r.errs()["lines.0.id"] == nil {
			t.Errorf("%s: %v", name, r.errs())
		}
	}
	if f.count(`SELECT count(*) FROM `+f.tbl("invoice_line")+` WHERE qty = 99`) != 0 {
		t.Fatal("foreign child мөр өөрчлөгдсөн")
	}
	if f.count(`SELECT count(*) FROM `+f.tbl("invoice_line")+` WHERE id = ? AND qty = 7 AND deleted_at IS NULL`, otherLine) != 1 {
		t.Fatal("өөр эцгийн мөр хөндөгдсөн")
	}
	// буруу id формат, давхардсан id
	r = do(t, app, "PUT", "/invoices/"+mine.str("id"), f.orgA, 1, map[string]any{"version": 1, "lines": []any{map[string]any{"id": "bad", "qty": 1}}})
	mustStatus(t, r, 422)
	own := mine.list("lines")[0]["id"].(string)
	r = do(t, app, "PUT", "/invoices/"+mine.str("id"), f.orgA, 1, map[string]any{"version": 1, "lines": []any{map[string]any{"id": own, "qty": 1}, map[string]any{"id": strings.ToUpper(own), "qty": 2}}})
	mustStatus(t, r, 422)
	if r.errs()["lines.1.id"] == nil {
		t.Errorf("%v", r.errs())
	}
	// B-ийн нэхэмжлэх A-д харагдахгүй, child орхигдсон
	mustStatus(t, do(t, app, "GET", "/invoices/"+bInv.str("id"), f.orgA, 1, nil), 404)
}

func TestChildrenStrictAndHardDeleteParent(t *testing.T) {
	f := newFx(t)
	res := f.invoiceResource()
	res.StrictUnknown = true
	app := mountOn(t, "/invoices", res)
	item := f.person(f.orgA, "Бараа", 1)
	bad := line(item, 1, 1)
	bad["zzz"] = 1
	r := do(t, app, "POST", "/invoices", f.orgA, 1, map[string]any{"number": "I", "lines": []any{bad}})
	mustStatus(t, r, 422)
	if r.errs()["lines.0.zzz"] == nil {
		t.Errorf("%v", r.errs())
	}
	c := do(t, app, "POST", "/invoices", f.orgA, 1, map[string]any{"number": "I", "lines": []any{line(item, 1, 1)}, "notes": []any{map[string]any{"body": "x"}}})
	mustStatus(t, c, 201)
	// эцгийг hard delete → child-ууд (soft/hard аль ч) устна, FK алдаа гарахгүй
	mustStatus(t, do(t, app, "DELETE", "/invoices/"+c.str("id"), f.orgA, 1, nil), 200)
	if f.count(`SELECT count(*) FROM `+f.tbl("invoice_line")) != 0 || f.count(`SELECT count(*) FROM `+f.tbl("invoice_note")) != 0 || f.count(`SELECT count(*) FROM `+f.tbl("invoice")) != 0 {
		t.Error("hard delete: parent+children устах ёстой")
	}
}

func TestChildDecimalExact(t *testing.T) {
	f := newFx(t)
	app := mountOn(t, "/invoices", f.invoiceResource())
	item := f.person(f.orgA, "Бараа", 1)
	l := line(item, 1, 1)
	l["price"] = "9999999999999999.99"
	c := do(t, app, "POST", "/invoices", f.orgA, 1, map[string]any{"number": "D", "lines": []any{l}})
	mustStatus(t, c, 201)
	if !strings.Contains(string(c.Raw), `"price":9999999999999999.99`) {
		t.Errorf("child Money хариунд яг байх ёстой: %s", c.Raw)
	}
}
