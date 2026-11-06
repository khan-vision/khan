package crud

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/khan-vision/khan/audit"
	"github.com/khan-vision/khan/obs"
	"github.com/khan-vision/khan/query"
	"gorm.io/gorm"
)

// afx — audit-тэй fixture: fx + khan.audit_log (SchemaSQL) + org цэвэрлэгээ.
type afx struct{ *fx }

func newAfx(t *testing.T) *afx {
	t.Helper()
	f := newFx(t)
	f.exec(audit.SchemaSQL)
	t.Cleanup(func() { purgeAuditOrgs(f.db, f.orgA, f.orgB) })
	return &afx{f}
}

// purgeAuditOrgs — audit_log append-only (DELETE trigger) тул цэвэрлэгээг албан ёсны Purge-ээр.
func purgeAuditOrgs(db *gorm.DB, orgs ...string) {
	for _, o := range orgs {
		_, _ = audit.Purge(context.Background(), db, time.Now(), audit.ForOrg(o))
	}
}

func withAudit(r *Resource[map[string]any], spec *AuditSpec) *Resource[map[string]any] {
	if spec == nil {
		spec = &AuditSpec{Enabled: true}
	}
	r.Audit = spec
	return r
}

// auditApp — obs.RequestID + claims бүхий app (request id, IP, User-Agent бүртгэгдэнэ).
func auditApp[T any](t *testing.T, prefix string, r *Resource[T]) *fiber.App {
	t.Helper()
	app := fiber.New()
	app.Use(obs.RequestID())
	if err := r.Mount(app.Group(prefix, claimsMW)); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	return app
}

type areq struct {
	org, user, reqID, ua string
	role                 int
}

func (f *afx) send(t *testing.T, app *fiber.App, method, path string, a areq, body any) resp {
	t.Helper()
	var rd io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, rd)
	req.Header.Set("Content-Type", "application/json")
	if a.org != "" {
		req.Header.Set("X-T-Org", a.org)
		if a.role == 0 {
			a.role = 1
		}
		req.Header.Set("X-T-Role", fmt.Sprint(a.role))
	}
	if a.user != "" {
		req.Header.Set("X-T-User", a.user)
	}
	if a.reqID != "" {
		req.Header.Set("X-Request-ID", a.reqID)
	}
	if a.ua != "" {
		req.Header.Set("User-Agent", a.ua)
	}
	res, err := app.Test(req, fiber.TestConfig{Timeout: 0})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(res.Body)
	out := resp{Status: res.StatusCode, Raw: raw}
	_ = json.Unmarshal(raw, &out.JSON)
	return out
}

type arow struct {
	ID        int64
	RecordID  string
	Action    string
	ActorID   *string
	ActorRole *string
	IP        *string
	UserAgent *string
	RequestID *string
	Changes   string
	Snapshot  *string
}

func (f *afx) entries(org, resource string) []arow {
	f.t.Helper()
	var rows []arow
	err := f.db.Raw(`SELECT id, record_id, action, actor_id, actor_role, ip, user_agent, request_id, changes::text AS changes, snapshot::text AS snapshot
		FROM khan.audit_log WHERE org_id = ? AND resource = ? ORDER BY id`, org, resource).Scan(&rows).Error
	if err != nil {
		f.t.Fatal(err)
	}
	return rows
}

func (r arow) changes(t *testing.T) audit.Changes {
	t.Helper()
	var cs audit.Changes
	if err := json.Unmarshal([]byte(r.Changes), &cs); err != nil {
		t.Fatalf("%v: %s", err, r.Changes)
	}
	return cs
}

func (r arow) snap(t *testing.T) map[string]any {
	t.Helper()
	if r.Snapshot == nil {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(*r.Snapshot), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func (f *afx) must2(err error, t *testing.T) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func jn(s string) json.Number { return json.Number(s) }

const uA = "aaaaaaaa-0000-0000-0000-00000000000a"

func TestAuditCreateUpdateDeleteRoom(t *testing.T) {
	f := newAfx(t)
	app := auditApp(t, "/rooms", withAudit(f.roomResource(), &AuditSpec{Enabled: true, Snapshot: true}))
	a := areq{org: f.orgA, user: uA, reqID: "req-create-1", ua: "KhanTest/1.0", role: 1}

	c := f.send(t, app, "POST", "/rooms", a, map[string]any{"name": "Өрөө 1", "code": "R-1", "capacity": 10, "price": "12.50", "meta": map[string]any{"b": 1, "a": []any{1, 2}}})
	mustStatus(t, c, 201)
	id := c.str("id")

	es := f.entries(f.orgA, "test.room")
	if len(es) != 1 {
		t.Fatalf("create entry=%d", len(es))
	}
	e := es[0]
	if e.Action != "create" || e.RecordID != id || *e.ActorID != uA || *e.ActorRole != "1" || *e.RequestID != "req-create-1" || *e.UserAgent != "KhanTest/1.0" || e.IP == nil || *e.IP == "" {
		t.Errorf("meta: %+v", e)
	}
	cs := e.changes(t)
	if cs["name"].Old != nil || cs["name"].New != "Өрөө 1" || cs["price"].New != jn("12.50") || cs["capacity"].New != jn("10") {
		t.Errorf("create changes: %v", cs)
	}
	// DB default-ууд (status='draft', active=true) бодит төлвөөс орно; хамгаалагдсан баганууд орохгүй
	if cs["status"].New != "draft" || cs["active"].New != true {
		t.Errorf("default-ууд: %v", cs)
	}
	for _, k := range []string{"id", "org_id", "version", "created_at", "updated_at", "created_by"} {
		if _, ok := cs[k]; ok {
			t.Errorf("%s түүхэнд орох ёсгүй", k)
		}
	}
	if s := e.snap(t); s["name"] != "Өрөө 1" || s["price"] != json.Number("12.50") && s["price"] != 12.5 {
		t.Errorf("create snapshot: %v", s)
	}

	// UPDATE: зөвхөн өөрчлөгдсөн талбар; үнэ 12.5 (тоо) = 12.50 → өөрчлөлт БИШ
	u := f.send(t, app, "PUT", "/rooms/"+id, areq{org: f.orgA, user: uA, reqID: "req-up-1"}, map[string]any{"version": 1, "name": "Өрөө 1", "price": 12.5, "capacity": 25, "meta": map[string]any{"a": []any{1, 2}, "b": 1}})
	mustStatus(t, u, 200)
	es = f.entries(f.orgA, "test.room")
	if len(es) != 2 {
		t.Fatalf("update entry=%d", len(es))
	}
	cs = es[1].changes(t)
	if len(cs) != 1 || cs["capacity"].Old != jn("10") || cs["capacity"].New != jn("25") {
		t.Errorf("update changes: %v", cs)
	}
	if es[1].Action != "update" || *es[1].RequestID != "req-up-1" || es[1].snap(t)["capacity"] != json.Number("25") && es[1].snap(t)["capacity"] != 25.0 {
		t.Errorf("update entry: %+v snap=%v", es[1], es[1].Snapshot)
	}

	// Өөрчлөлтгүй PUT → entry нэмэгдэхгүй (хэдий version өсөх ч)
	mustStatus(t, f.send(t, app, "PUT", "/rooms/"+id, a, map[string]any{"version": 2, "name": "Өрөө 1", "capacity": 25}), 200)
	if n := len(f.entries(f.orgA, "test.room")); n != 2 {
		t.Errorf("өөрчлөлтгүй update entry бичсэн: %d", n)
	}

	// Төрөл, null болгох: price → null, opens тавих
	mustStatus(t, f.send(t, app, "PUT", "/rooms/"+id, a, map[string]any{"version": 3, "price": nil, "opens": "2026-05-06"}), 200)
	cs = f.entries(f.orgA, "test.room")[2].changes(t)
	if cs["price"].Old != jn("12.50") || cs["price"].New != nil || cs["opens"].Old != nil || cs["opens"].New != "2026-05-06" || len(cs) != 2 {
		t.Errorf("null/date: %v", cs)
	}

	// DELETE (soft): changes [old,nil], snapshot = сүүлийн төлөв
	mustStatus(t, f.send(t, app, "DELETE", "/rooms/"+id, a, nil), 200)
	es = f.entries(f.orgA, "test.room")
	d := es[3]
	cs = d.changes(t)
	if d.Action != "delete" || cs["name"].Old != "Өрөө 1" || cs["name"].New != nil || cs["capacity"].Old != jn("25") {
		t.Errorf("delete: %+v %v", d, cs)
	}
	if s := d.snap(t); s["name"] != "Өрөө 1" || s["opens"] != "2026-05-06" {
		t.Errorf("delete snapshot: %v", s)
	}

	// Бусад org-д огт entry байхгүй
	if n := len(f.entries(f.orgB, "test.room")); n != 0 {
		t.Errorf("orgB entry=%d", n)
	}
}

func TestAuditNilSpecNoEffect(t *testing.T) {
	f := newAfx(t)
	for name, spec := range map[string]*AuditSpec{"nil": nil, "disabled": {Enabled: false, Snapshot: true}} {
		r := f.roomResource()
		r.Audit = spec
		app := auditApp(t, "/rooms", r)
		c := f.send(t, app, "POST", "/rooms", areq{org: f.orgA}, map[string]any{"name": "N-" + name})
		mustStatus(t, c, 201)
		mustStatus(t, f.send(t, app, "PUT", "/rooms/"+c.str("id"), areq{org: f.orgA}, map[string]any{"version": 1, "name": "M"}), 200)
		mustStatus(t, f.send(t, app, "DELETE", "/rooms/"+c.str("id"), areq{org: f.orgA}, nil), 200)
		// history route-ууд бүртгэгдээгүй
		if h := f.send(t, app, "GET", "/rooms/"+c.str("id")+"/history", areq{org: f.orgA}, nil); h.Status != 404 {
			t.Errorf("%s: /:id/history route нээлттэй: %d", name, h.Status)
		}
		if h := f.send(t, app, "GET", "/rooms/history", areq{org: f.orgA}, nil); h.Status != 400 { // "history" нь /:id болж id буруу
			t.Errorf("%s: /history route нээлттэй: %d", name, h.Status)
		}
	}
	if n := len(f.entries(f.orgA, "test.room")); n != 0 {
		t.Errorf("Audit унтраалттай үед entry=%d", n)
	}
}

func TestAuditAtomicityHooksAndAuditFailure(t *testing.T) {
	f := newAfx(t)
	boom := errors.New("boom")
	r := withAudit(f.roomResource(), nil)
	r.Bulk = &query.BulkSpec{AllowDelete: true} // AfterUpdate hook-тэй resource-д bulk update хориотой
	var failAfterCreate, failAfterUpdate, failAfterDelete bool
	r.Hooks.AfterCreate = func(x *Ctx, v Values) error {
		if failAfterCreate {
			return boom
		}
		return nil
	}
	r.Hooks.AfterUpdate = func(x *Ctx, v Values) error {
		if failAfterUpdate {
			return boom
		}
		return nil
	}
	r.Hooks.AfterDelete = func(x *Ctx) error {
		if failAfterDelete {
			return boom
		}
		return nil
	}
	app := auditApp(t, "/rooms", r)
	a := areq{org: f.orgA}

	failAfterCreate = true
	mustStatus(t, f.send(t, app, "POST", "/rooms", a, map[string]any{"name": "X"}), 500)
	if f.count(`SELECT count(*) FROM `+f.tbl("room")) != 0 || len(f.entries(f.orgA, "test.room")) != 0 {
		t.Error("AfterCreate алдаа: мөр эсвэл audit үлдсэн")
	}
	failAfterCreate = false
	c := f.send(t, app, "POST", "/rooms", a, map[string]any{"name": "X", "capacity": 1})
	mustStatus(t, c, 201)
	id := c.str("id")

	failAfterUpdate = true
	mustStatus(t, f.send(t, app, "PUT", "/rooms/"+id, a, map[string]any{"version": 1, "capacity": 99}), 500)
	if n := len(f.entries(f.orgA, "test.room")); n != 1 {
		t.Errorf("AfterUpdate алдаа: audit=%d (1 хүлээсэн)", n)
	}
	if f.count(`SELECT capacity FROM `+f.tbl("room")+` WHERE id = ?`, id) != 1 {
		t.Error("update буцаагдаагүй")
	}

	failAfterDelete = true
	mustStatus(t, f.send(t, app, "DELETE", "/rooms/"+id, a, nil), 500)
	if n := len(f.entries(f.orgA, "test.room")); n != 1 {
		t.Errorf("AfterDelete алдаа: audit=%d", n)
	}
	// bulk delete (hook-той → deleteOne) мөн буцна
	mustStatus(t, f.send(t, app, "POST", "/rooms/bulk", a, map[string]any{"action": "delete", "ids": []string{id}}), 500)
	if n := len(f.entries(f.orgA, "test.room")); n != 1 || f.count(`SELECT count(*) FROM `+f.tbl("room")+` WHERE deleted_at IS NULL`) != 1 {
		t.Errorf("bulk+hook алдаа: audit=%d", n)
	}
}

func TestAuditWriteFailureFailsBusinessWrite(t *testing.T) {
	f := newAfx(t)
	// Зөвхөн энэ тестийн resource-д audit INSERT-ийг унагаах trigger (бусад тестэд нөлөөгүй).
	f.exec(`CREATE OR REPLACE FUNCTION khan.audit_test_fail() RETURNS trigger AS $$
		BEGIN IF NEW.resource = 'test.room' AND NEW.org_id = '` + f.orgA + `' THEN RAISE EXCEPTION 'audit down'; END IF; RETURN NEW; END; $$ LANGUAGE plpgsql`)
	f.exec(`DROP TRIGGER IF EXISTS audit_test_fail ON khan.audit_log`)
	f.exec(`CREATE TRIGGER audit_test_fail BEFORE INSERT ON khan.audit_log FOR EACH ROW EXECUTE FUNCTION khan.audit_test_fail()`)
	t.Cleanup(func() {
		f.db.Exec(`DROP TRIGGER IF EXISTS audit_test_fail ON khan.audit_log`)
		f.db.Exec(`DROP FUNCTION IF EXISTS khan.audit_test_fail()`)
	})

	app := auditApp(t, "/rooms", withAudit(f.roomResource(), nil))
	r := f.send(t, app, "POST", "/rooms", areq{org: f.orgA}, map[string]any{"name": "X"})
	if r.Status != 500 || strings.Contains(string(r.Raw), "audit down") {
		t.Errorf("audit унасан үед 500 (дотоод алдаа ил биш) хүлээсэн: %d %s", r.Status, r.Raw)
	}
	if f.count(`SELECT count(*) FROM `+f.tbl("room")+` WHERE org_id = ?`, f.orgA) != 0 {
		t.Error("audit бичигдээгүй ч бизнес бичилт үлдсэн")
	}
	if f.count(`SELECT count(*) FROM khan.outbox_events WHERE org_id = ?`, f.orgA) != 0 {
		t.Error("outbox event үлдсэн")
	}
	// Org B (trigger-т өртөхгүй) хэвийн
	mustStatus(t, f.send(t, app, "POST", "/rooms", areq{org: f.orgB}, map[string]any{"name": "B"}), 201)
}

func TestAuditPasswordNeverLogged(t *testing.T) {
	f := newAfx(t)
	r := f.accountResource()
	r.Audit = &AuditSpec{Enabled: true, Snapshot: true, DeletedHistory: true}
	app := auditApp(t, "/accounts", r)
	a := areq{org: f.orgA, user: uA}

	c := f.send(t, app, "POST", "/accounts", a, map[string]any{"login": "bat", "password": "SuperSecret-111", "display": "Бат"})
	mustStatus(t, c, 201)
	id := fmt.Sprint(c.JSON["id"])
	mustStatus(t, f.send(t, app, "PUT", "/accounts/"+id, a, map[string]any{"password": "SuperSecret-222", "display": "Бат2"}), 200)
	// ижил password дахин илгээх: hash (salt) шинэ тул «өөрчлөгдсөн» гэж тэмдэглэгдэнэ (утга нь «***»)
	mustStatus(t, f.send(t, app, "PUT", "/accounts/"+id, a, map[string]any{"password": "SuperSecret-222", "display": "Бат3"}), 200)
	var hashes []string // DB-д хадгалагдсан (hash) утга — audit-д огт байх ёсгүй
	f.must2(f.db.Raw(`SELECT password FROM `+f.tbl("account")+` WHERE org_id = ?`, f.orgA).Scan(&hashes).Error, t)
	if len(hashes) != 1 || len(hashes[0]) < 20 {
		t.Fatalf("hash: %v", hashes)
	}
	mustStatus(t, f.send(t, app, "DELETE", "/accounts/"+id, a, nil), 200)

	es := f.entries(f.orgA, "test.account")
	if len(es) != 4 {
		t.Fatalf("entries=%d", len(es))
	}
	var all string
	if err := f.db.Raw(`SELECT string_agg(changes::text || coalesce(snapshot::text,''), '|') FROM khan.audit_log WHERE org_id = ?`, f.orgA).Scan(&all).Error; err != nil {
		t.Fatal(err)
	}
	secrets := append([]string{"SuperSecret-111", "SuperSecret-222"}, hashes...)
	for _, secret := range secrets {
		if strings.Contains(all, secret) {
			t.Fatalf("нууц үг audit-д алдагдсан: %s", all)
		}
	}
	if cs := es[0].changes(t); cs["password"].Old != nil || cs["password"].New != "***" {
		t.Errorf("create password: %v", cs["password"])
	}
	if cs := es[1].changes(t); cs["password"].Old != "***" || cs["password"].New != "***" || cs["display"].New != "Бат2" {
		t.Errorf("update password өөрчлөгдсөнийг тэмдэглэх ёстой: %v", cs)
	}
	if cs := es[2].changes(t); cs["password"].New != "***" || cs["display"].New != "Бат3" {
		t.Errorf("update 2: %v", cs)
	}
	if s := es[3].snap(t); s["password"] != "***" {
		t.Errorf("delete snapshot password: %v", s["password"])
	}
	if es[0].RecordID != id {
		t.Errorf("IDInt record_id=%s want %s", es[0].RecordID, id)
	}
	// history endpoint ч нууцыг гаргахгүй
	h := f.send(t, app, "GET", "/accounts/"+id+"/history", a, nil)
	mustStatus(t, h, 200)
	if strings.Contains(string(h.Raw), "SuperSecret") || h.JSON["total"] != 4.0 {
		t.Errorf("history: %s", h.Raw)
	}
}

func TestAuditChildren(t *testing.T) {
	f := newAfx(t)
	r := f.invoiceResource()
	r.Audit = &AuditSpec{Enabled: true, Snapshot: true, Exclude: []string{"lines.price", "notes"}}
	app := auditApp(t, "/invoices", r)
	a := areq{org: f.orgA, user: uA}
	item := f.person(f.orgA, "Бараа", 1)
	line := func(qty int, price string) map[string]any {
		return map[string]any{"item_id": item, "qty": qty, "price": price, "position": 1}
	}

	c := f.send(t, app, "POST", "/invoices", a, map[string]any{"number": "INV-1", "lines": []any{line(2, "10.00")}, "notes": []any{map[string]any{"body": "нууц тэмдэглэл"}}})
	mustStatus(t, c, 201)
	id := c.str("id")
	es := f.entries(f.orgA, "test.invoice")
	cs := es[0].changes(t)
	lines, _ := cs["lines"].New.([]any)
	if len(lines) != 1 {
		t.Fatalf("create lines: %v", cs)
	}
	l0 := lines[0].(map[string]any)
	if l0["qty"] != jn("2") || l0["item_id"] != item || l0["id"] == nil {
		t.Errorf("line: %v", l0)
	}
	if _, has := l0["price"]; has {
		t.Error("Exclude lines.price ажиллаагүй")
	}
	for _, k := range []string{"org_id", "invoice_id", "created_at", "updated_at", "deleted_at"} {
		if _, has := l0[k]; has {
			t.Errorf("child %s түүхэнд орох ёсгүй", k)
		}
	}
	if _, has := cs["notes"]; has || strings.Contains(es[0].Changes+*es[0].Snapshot, "нууц тэмдэглэл") {
		t.Error("Exclude notes ажиллаагүй")
	}
	lineID := l0["id"].(string)

	// Зөвхөн эцгийн талбар → lines key байхгүй
	mustStatus(t, f.send(t, app, "PUT", "/invoices/"+id, a, map[string]any{"version": 1, "number": "INV-1B"}), 200)
	cs = f.entries(f.orgA, "test.invoice")[1].changes(t)
	if _, has := cs["lines"]; has || len(cs) != 1 || cs["number"].New != "INV-1B" {
		t.Errorf("зөвхөн number: %v", cs)
	}
	// children илгээсэн боловч өөрчлөлтгүй → entry нэмэгдэхгүй
	same := line(2, "10.00")
	same["id"] = lineID
	mustStatus(t, f.send(t, app, "PUT", "/invoices/"+id, a, map[string]any{"version": 2, "lines": []any{same}}), 200)
	if n := len(f.entries(f.orgA, "test.invoice")); n != 2 {
		t.Errorf("өөрчлөлтгүй children: entries=%d", n)
	}
	// child засвар + шинэ мөр
	same["qty"] = 5
	mustStatus(t, f.send(t, app, "PUT", "/invoices/"+id, a, map[string]any{"version": 3, "lines": []any{same, line(1, "3.00")}}), 200)
	cs = f.entries(f.orgA, "test.invoice")[2].changes(t)
	if len(cs) != 1 {
		t.Fatalf("lines л өөрчлөгдөх ёстой: %v", cs)
	}
	oldL, newL := cs["lines"].Old.([]any), cs["lines"].New.([]any)
	if len(oldL) != 1 || len(newL) != 2 || oldL[0].(map[string]any)["qty"] != jn("2") {
		t.Errorf("lines diff: %v", cs["lines"])
	}
	// hard delete (SoftDelete=false): children түүхэнд [rows, nil]
	mustStatus(t, f.send(t, app, "DELETE", "/invoices/"+id, a, nil), 200)
	d := f.entries(f.orgA, "test.invoice")[3]
	cs = d.changes(t)
	if d.Action != "delete" || cs["lines"].New != nil || len(cs["lines"].Old.([]any)) != 2 {
		t.Errorf("delete lines: %v", cs)
	}
	if f.count(`SELECT count(*) FROM `+f.tbl("invoice_line")) != 0 {
		t.Error("child устаагүй")
	}
}

func TestAuditBulk(t *testing.T) {
	f := newAfx(t)
	r := withAudit(f.roomResource(), &AuditSpec{Enabled: true, Snapshot: true})
	app := auditApp(t, "/rooms", r)
	a := areq{org: f.orgA, user: uA, reqID: "bulk-req"}
	mk := func(org, name string, cap int) string {
		c := f.send(t, app, "POST", "/rooms", areq{org: org}, map[string]any{"name": name, "capacity": cap})
		mustStatus(t, c, 201)
		return c.str("id")
	}
	r1, r2, r3 := mk(f.orgA, "A1", 1), mk(f.orgA, "A2", 7), mk(f.orgA, "A3", 3)
	other := mk(f.orgB, "B1", 1)
	base := len(f.entries(f.orgA, "test.room"))

	// bulk update: r2 аль хэдийн 7 → өөрчлөлтгүй (entry алгасна); өөр org-ийн id чимээгүй алгасагдана
	b := f.send(t, app, "POST", "/rooms/bulk", a, map[string]any{"action": "update", "ids": []string{r1, r2, r3, other}, "set": map[string]any{"capacity": 7}})
	mustStatus(t, b, 200)
	es := f.entries(f.orgA, "test.room")[base:]
	if len(es) != 2 {
		t.Fatalf("bulk_update entries=%d", len(es))
	}
	got := map[string]audit.Changes{}
	for _, e := range es {
		if e.Action != "bulk_update" || *e.RequestID != "bulk-req" || *e.ActorID != uA {
			t.Errorf("entry: %+v", e)
		}
		got[e.RecordID] = e.changes(t)
	}
	if got[r1]["capacity"].Old != jn("1") || got[r1]["capacity"].New != jn("7") || got[r3]["capacity"].Old != jn("3") || got[r2] != nil || len(got[r1]) != 1 {
		t.Errorf("bulk changes: %v", got)
	}
	if n := len(f.entries(f.orgB, "test.room")); n != 1 { // зөвхөн өөрийн create
		t.Errorf("orgB bulk-д өртөв: %d", n)
	}

	// bulk delete (hook-гүй → ExecBulk зам)
	base = len(f.entries(f.orgA, "test.room"))
	mustStatus(t, f.send(t, app, "POST", "/rooms/bulk", a, map[string]any{"action": "delete", "ids": []string{r1, r2}}), 200)
	es = f.entries(f.orgA, "test.room")[base:]
	if len(es) != 2 || es[0].Action != "bulk_delete" || es[0].changes(t)["name"].New != nil || es[0].snap(t)["name"] == nil {
		t.Errorf("bulk delete: %+v", es)
	}

	// bulk delete (hook-той → deleteOne зам) мөн bulk_delete
	r2h := f.roomResource()
	r2h.Audit = &AuditSpec{Enabled: true}
	r2h.Hooks.BeforeDelete = func(x *Ctx) error { return nil }
	app2 := auditApp(t, "/rooms", r2h)
	base = len(f.entries(f.orgA, "test.room"))
	mustStatus(t, f.send(t, app2, "POST", "/rooms/bulk", a, map[string]any{"action": "delete", "ids": []string{r3}}), 200)
	es = f.entries(f.orgA, "test.room")[base:]
	if len(es) != 1 || es[0].Action != "bulk_delete" || es[0].RecordID != r3 {
		t.Errorf("hook bulk delete: %+v", es)
	}
}

func TestAuditHistoryEndpoints(t *testing.T) {
	f := newAfx(t)
	app := auditApp(t, "/rooms", withAudit(f.roomResource(), &AuditSpec{Enabled: true, ListHistory: true}))
	a := areq{org: f.orgA, user: uA}
	b := areq{org: f.orgB, user: "bbbbbbbb-0000-0000-0000-00000000000b"}

	c := f.send(t, app, "POST", "/rooms", a, map[string]any{"name": "v0", "capacity": 0})
	mustStatus(t, c, 201)
	id := c.str("id")
	for v := 1; v <= 24; v++ {
		mustStatus(t, f.send(t, app, "PUT", "/rooms/"+id, a, map[string]any{"version": v, "capacity": v}), 200)
	}
	cb := f.send(t, app, "POST", "/rooms", b, map[string]any{"name": "B room"})
	mustStatus(t, cb, 201)
	idB := cb.str("id")
	other := f.send(t, app, "POST", "/rooms", a, map[string]any{"name": "other"})
	mustStatus(t, other, 201)

	// Нэг мөрийн түүх: 25 entry, шинэ нь эхэнд, хуудаслалт
	h := f.send(t, app, "GET", "/rooms/"+id+"/history?limit=10", a, nil)
	mustStatus(t, h, 200)
	if h.JSON["total"] != 25.0 || len(h.list("data")) != 10 || h.JSON["limit"] != 10.0 || h.JSON["page"] != 1.0 {
		t.Fatalf("p1: %s", h.Raw)
	}
	d0 := h.list("data")[0]
	if d0["action"] != "update" || d0["action_label"] != "Засварласан" || d0["actor_id"] != uA {
		t.Errorf("d0=%v", d0)
	}
	if ch := d0["changes"].(map[string]any)["capacity"].([]any); ch[1] != 24.0 {
		t.Errorf("хамгийн шинэ нь capacity=24 байх ёстой: %v", ch)
	}
	h3 := f.send(t, app, "GET", "/rooms/"+id+"/history?limit=10&page=3", a, nil)
	l3 := h3.list("data")
	if len(l3) != 5 || l3[4]["action"] != "create" || l3[4]["action_label"] != "Үүсгэсэн" {
		t.Errorf("p3: %s", h3.Raw)
	}
	if def := f.send(t, app, "GET", "/rooms/"+id+"/history", a, nil); len(def.list("data")) != audit.DefaultLimit {
		t.Errorf("анхдагч limit: %d", len(def.list("data")))
	}

	// Org B A-ийн мөрийн түүхийг харах боломжгүй
	x := f.send(t, app, "GET", "/rooms/"+id+"/history", b, nil)
	mustStatus(t, x, 404)
	if strings.Contains(string(x.Raw), uA) {
		t.Errorf("org B A-ийн түүх харав: %s", x.Raw)
	}
	// A нь B-ийн мөрийг харах боломжгүй
	if y := f.send(t, app, "GET", "/rooms/"+idB+"/history", a, nil); y.Status != 404 {
		t.Errorf("A B-ийн түүх харав: %s", y.Raw)
	}
	// B өөрийнхөө түүхийг харна
	if z := f.send(t, app, "GET", "/rooms/"+idB+"/history", b, nil); z.JSON["total"] != 1.0 {
		t.Errorf("B өөрийн түүх: %s", z.Raw)
	}

	// Resource-ийн түүх + шүүлт
	l := f.send(t, app, "GET", "/rooms/history", a, nil)
	mustStatus(t, l, 200)
	if l.JSON["total"] != 26.0 { // 25 + other create (B-гүй)
		t.Errorf("list total=%v", l.JSON["total"])
	}
	if l2 := f.send(t, app, "GET", "/rooms/history?actor_id="+uA+"&action=create", a, nil); l2.JSON["total"] != 2.0 {
		t.Errorf("actor+action: %s", l2.Raw)
	}
	if l3 := f.send(t, app, "GET", "/rooms/history?actor_id=nobody", a, nil); l3.JSON["total"] != 0.0 {
		t.Errorf("actor nobody: %s", l3.Raw)
	}
	if l4 := f.send(t, app, "GET", "/rooms/history?record_id="+id+"&from=2000-01-01&to=2999-12-31", a, nil); l4.JSON["total"] != 25.0 {
		t.Errorf("record+date: %s", l4.Raw)
	}
	if l5 := f.send(t, app, "GET", "/rooms/history?to=2000-01-01", a, nil); l5.JSON["total"] != 0.0 {
		t.Errorf("to=2000: %s", l5.Raw)
	}
	lb := f.send(t, app, "GET", "/rooms/history", b, nil)
	if lb.JSON["total"] != 1.0 || strings.Contains(string(lb.Raw), uA) {
		t.Errorf("B list: %s", lb.Raw)
	}

	// Эрх: JWT-гүй 401; read эрхгүй role 403; зөвхөн-унших role 200; id буруу 400
	if r := f.send(t, app, "GET", "/rooms/"+id+"/history", areq{}, nil); r.Status != 401 {
		t.Errorf("JWT-гүй: %d", r.Status)
	}
	if r := f.send(t, app, "GET", "/rooms/history", areq{}, nil); r.Status != 401 {
		t.Errorf("JWT-гүй list: %d", r.Status)
	}
	if r := f.send(t, app, "GET", "/rooms/"+id+"/history", areq{org: f.orgA, role: 9}, nil); r.Status != 403 {
		t.Errorf("эрхгүй role: %d", r.Status)
	}
	if r := f.send(t, app, "GET", "/rooms/history", areq{org: f.orgA, role: 9}, nil); r.Status != 403 {
		t.Errorf("эрхгүй role list: %d", r.Status)
	}
	if r := f.send(t, app, "GET", "/rooms/"+id+"/history", areq{org: f.orgA, role: 2}, nil); r.Status != 200 {
		t.Errorf("read-only role: %d", r.Status)
	}
	if r := f.send(t, app, "GET", "/rooms/not-a-uuid/history", a, nil); r.Status != 400 {
		t.Errorf("id буруу: %d", r.Status)
	}
	if r := f.send(t, app, "GET", "/rooms/"+id+"/history?page=0", a, nil); r.Status != 400 {
		t.Errorf("page=0: %d", r.Status)
	}
	// Устгагдсан мөрийн түүх: анхдагчаар 404 (GET /:id-тэй ижил), DeletedHistory-д л харагдана
	mustStatus(t, f.send(t, app, "DELETE", "/rooms/"+id, a, nil), 200)
	mustStatus(t, f.send(t, app, "GET", "/rooms/"+id, a, nil), 404)
	mustStatus(t, f.send(t, app, "GET", "/rooms/"+id+"/history", a, nil), 404)
	app2 := auditApp(t, "/rooms", withAudit(f.roomResource(), &AuditSpec{Enabled: true, DeletedHistory: true}))
	if r := f.send(t, app2, "GET", "/rooms/"+id+"/history", a, nil); r.Status != 200 || r.JSON["total"] != 26.0 {
		t.Errorf("устгасны дараа (DeletedHistory): %s", r.Raw)
	}
	// DeletedHistory нь өөр org-ийн мөрийг ил гаргахгүй: B-д A-ийн түүх хоосон (мөр B-д байхгүй)
	if r := f.send(t, app2, "GET", "/rooms/"+id+"/history", b, nil); r.JSON["total"] != 0.0 {
		t.Errorf("DeletedHistory org B: %s", r.Raw)
	}
}

func TestAuditHistoryIntIDAndRLS(t *testing.T) {
	f := newAfx(t)
	r := f.accountResource()
	r.Audit = &AuditSpec{Enabled: true}
	r.RLS = true
	app := auditApp(t, "/accounts", r)
	a := areq{org: f.orgA}
	c := f.send(t, app, "POST", "/accounts", a, map[string]any{"login": "x", "password": "password-1"})
	mustStatus(t, c, 201)
	id := fmt.Sprint(c.JSON["id"])
	if h := f.send(t, app, "GET", "/accounts/"+id+"/history", a, nil); h.JSON["total"] != 1.0 {
		t.Errorf("int id history: %s", h.Raw)
	}
	if h := f.send(t, app, "GET", "/accounts/"+id+"/history", areq{org: f.orgB}, nil); h.Status != 404 {
		t.Errorf("RLS B: %s", h.Raw)
	}
	for _, bad := range []string{"abc", "0", "-1"} {
		if h := f.send(t, app, "GET", "/accounts/"+bad+"/history", a, nil); h.Status != 400 {
			t.Errorf("%s: %d", bad, h.Status)
		}
	}
	mustStatus(t, f.send(t, app, "PUT", "/accounts/"+id, a, map[string]any{"display": "d"}), 200)
	if h := f.send(t, app, "GET", "/accounts/"+id+"/history", a, nil); h.JSON["total"] != 2.0 {
		t.Errorf("RLS update: %s", h.Raw)
	}
}

func TestAuditExcludeAndJSONFields(t *testing.T) {
	f := newAfx(t)
	app := auditApp(t, "/rooms", withAudit(f.roomResource(), &AuditSpec{Enabled: true, Exclude: []string{"meta", "created_note"}}))
	a := areq{org: f.orgA}
	c := f.send(t, app, "POST", "/rooms", a, map[string]any{"name": "N", "meta": map[string]any{"x": 1}, "created_note": "secret"})
	mustStatus(t, c, 201)
	id := c.str("id")
	cs := f.entries(f.orgA, "test.room")[0].changes(t)
	if _, has := cs["meta"]; has {
		t.Error("meta Exclude ажиллаагүй")
	}
	if _, has := cs["created_note"]; has {
		t.Error("created_note Exclude ажиллаагүй")
	}
	// Зөвхөн Exclude хийсэн талбар өөрчлөгдвөл entry бичигдэхгүй
	mustStatus(t, f.send(t, app, "PUT", "/rooms/"+id, a, map[string]any{"version": 1, "meta": map[string]any{"x": 2}}), 200)
	if n := len(f.entries(f.orgA, "test.room")); n != 1 {
		t.Errorf("excluded талбарын өөрчлөлт entry бичсэн: %d", n)
	}
	// Бодит талбар хамт өөрчлөгдөхөд meta орохгүй
	mustStatus(t, f.send(t, app, "PUT", "/rooms/"+id, a, map[string]any{"version": 2, "meta": map[string]any{"x": 3}, "capacity": 5}), 200)
	if cs := f.entries(f.orgA, "test.room")[1].changes(t); len(cs) != 1 || cs["capacity"].New != jn("5") {
		t.Errorf("changes: %v", cs)
	}

	// JSON түлхүүрийн дараалал өөрчлөгдсөн нь өөрчлөлт биш; утга өөрчлөгдвөл nested diff
	app2 := auditApp(t, "/rooms", withAudit(f.roomResource(), nil))
	c2 := f.send(t, app2, "POST", "/rooms", a, map[string]any{"name": "J", "meta": map[string]any{"b": 1, "a": map[string]any{"z": 1, "y": 2}}})
	mustStatus(t, c2, 201)
	id2 := c2.str("id")
	before := len(f.entries(f.orgA, "test.room"))
	mustStatus(t, f.send(t, app2, "PUT", "/rooms/"+id2, a, map[string]any{"version": 1, "meta": json.RawMessage(`{"a":{"y":2,"z":1},"b":1.0}`)}), 200)
	if n := len(f.entries(f.orgA, "test.room")); n != before {
		t.Errorf("JSON дараалал өөрчлөлт гэж тооцогдсон: %d", n-before)
	}
	mustStatus(t, f.send(t, app2, "PUT", "/rooms/"+id2, a, map[string]any{"version": 2, "meta": map[string]any{"b": 2, "a": map[string]any{"z": 1, "y": 2}}}), 200)
	cs = f.entries(f.orgA, "test.room")[before].changes(t)
	if m := cs["meta"].New.(map[string]any); m["b"] != jn("2") {
		t.Errorf("nested: %v", cs["meta"])
	}
}

func TestAuditConfigErrors(t *testing.T) {
	f := newAfx(t)
	for _, ex := range []string{"nope", "lines.nope", "nope.price", "lines.", ""} {
		r := f.invoiceResource()
		r.Audit = &AuditSpec{Enabled: true, Exclude: []string{ex}}
		if err := r.Check(); err == nil || !strings.Contains(err.Error(), "Audit.Exclude") {
			t.Errorf("%q: алдаа хүлээсэн: %v", ex, err)
		}
	}
	r := f.invoiceResource()
	r.Audit = &AuditSpec{Enabled: true, Exclude: []string{"number", "lines", "notes.body"}}
	if err := r.Check(); err != nil {
		t.Errorf("зөв тохиргоо: %v", err)
	}
	// Check нь DB/Can шаардахгүй хэвээр
	r.DB, r.Can = nil, nil
	if err := r.Check(); err != nil {
		t.Errorf("Check DB-гүй: %v", err)
	}
}

func TestAuditMountBeforeIDRoute(t *testing.T) {
	// /history нь /:id-ээс өмнө бүртгэгдэж, нэг мөрийн GET-д саад болохгүй.
	f := newAfx(t)
	app := auditApp(t, "/rooms", withAudit(f.roomResource(), &AuditSpec{Enabled: true, ListHistory: true}))
	c := f.send(t, app, "POST", "/rooms", areq{org: f.orgA}, map[string]any{"name": "N"})
	mustStatus(t, c, 201)
	mustStatus(t, f.send(t, app, "GET", "/rooms/"+c.str("id"), areq{org: f.orgA}, nil), 200)
	mustStatus(t, f.send(t, app, "GET", "/rooms/history", areq{org: f.orgA}, nil), 200)
}

func TestAuditCacheStillBumped(t *testing.T) {
	f := newCfx(t)
	f.exec(audit.SchemaSQL)
	t.Cleanup(func() { purgeAuditOrgs(f.db, f.orgA, f.orgB) })
	r := f.room(nil)
	r.Audit = &AuditSpec{Enabled: true}
	app := mountOn(t, "/rooms", r)
	c := do(t, app, "POST", "/rooms", f.orgA, 1, map[string]any{"name": "C"})
	mustStatus(t, c, 201)
	id := c.str("id")
	mustStatus(t, do(t, app, "GET", "/rooms/"+id, f.orgA, 1, nil), 200)
	mustStatus(t, do(t, app, "PUT", "/rooms/"+id, f.orgA, 1, map[string]any{"version": 1, "name": "C2"}), 200)
	if g := do(t, app, "GET", "/rooms/"+id, f.orgA, 1, nil); g.str("name") != "C2" {
		t.Errorf("cache bump алдагдсан: %s", g.Raw)
	}
}

// /history анхдагчаар нээгдэхгүй (ListHistory=false): /:id руу өнгөрч 400 (буруу id).
func TestAuditListHistoryOffByDefault(t *testing.T) {
	f := newAfx(t)
	app := auditApp(t, "/rooms", withAudit(f.roomResource(), nil))
	mustStatus(t, f.send(t, app, "GET", "/rooms/history", areq{org: f.orgA}, nil), 400)
}

// ListHistory + custom ListQuery/DetailQuery = Mount/Check алдаа (fail-closed).
func TestAuditListHistoryRejectsCustomQueries(t *testing.T) {
	f := newAfx(t)
	for name, mut := range map[string]func(*Resource[map[string]any]){
		"list": func(r *Resource[map[string]any]) {
			r.ListQuery = func(db *gorm.DB, org string) *gorm.DB { return db.Table(r.Table + " " + r.Alias) }
		},
		"detail": func(r *Resource[map[string]any]) {
			r.DetailQuery = func(db *gorm.DB, org string) *gorm.DB { return db.Table(r.Table + " " + r.Alias) }
		},
	} {
		r := withAudit(f.roomResource(), &AuditSpec{Enabled: true, ListHistory: true})
		r.Alias = "r"
		mut(r)
		if err := r.Check(); err == nil || !strings.Contains(err.Error(), "Audit.ListHistory") {
			t.Errorf("%s: алдаа хүлээсэн: %v", name, err)
		}
	}
}

// /:id/history нь DetailQuery-ийн мөрийн харагдацыг хүндэтгэнэ: нуугдсан мөр = GET /:id-тэй ижил 404,
// DeletedHistory үед ч амьд боловч нуугдсан мөр 404.
func TestAuditHistoryRespectsDetailQuery(t *testing.T) {
	f := newAfx(t)
	mk := func(spec *AuditSpec) *fiber.App {
		r := withAudit(f.roomResource(), spec)
		r.Alias = "r"
		r.DetailQuery = func(db *gorm.DB, org string) *gorm.DB {
			return db.Table(r.Table+" r").Where("r.name <> ?", "secret")
		}
		return auditApp(t, "/rooms", r)
	}
	app := mk(&AuditSpec{Enabled: true})
	appDel := mk(&AuditSpec{Enabled: true, DeletedHistory: true})
	a := areq{org: f.orgA, user: uA}
	plain := auditApp(t, "/rooms", withAudit(f.roomResource(), nil)) // DetailQuery-гүй: POST өөрийн мөрөө уншина
	pub := f.send(t, plain, "POST", "/rooms", a, map[string]any{"name": "public"})
	sec := f.send(t, plain, "POST", "/rooms", a, map[string]any{"name": "secret"})
	mustStatus(t, pub, 201)
	mustStatus(t, sec, 201)
	mustStatus(t, f.send(t, app, "GET", "/rooms/"+pub.str("id")+"/history", a, nil), 200)
	for _, ap := range []*fiber.App{app, appDel} {
		mustStatus(t, f.send(t, ap, "GET", "/rooms/"+sec.str("id"), a, nil), 404)
		h := f.send(t, ap, "GET", "/rooms/"+sec.str("id")+"/history", a, nil)
		mustStatus(t, h, 404)
		if strings.Contains(string(h.Raw), "secret") {
			t.Errorf("нуугдсан мөрийн түүх ил: %s", h.Raw)
		}
	}
}

// 403: ActionRead эрхгүй үед e.forbidden (OnForbidden) ашиглагдана.
func TestAuditHistoryUsesOnForbidden(t *testing.T) {
	f := newAfx(t)
	r := withAudit(f.roomResource(), &AuditSpec{Enabled: true, ListHistory: true})
	r.OnForbidden = func(c fiber.Ctx, _ Action) error { return c.Status(403).JSON(fiber.Map{"error": "custom-denied"}) }
	app := auditApp(t, "/rooms", r)
	for _, p := range []string{"/rooms/history", "/rooms/" + uA + "/history"} {
		h := f.send(t, app, "GET", p, areq{org: f.orgA, role: 9}, nil)
		if h.Status != 403 || !strings.Contains(string(h.Raw), "custom-denied") {
			t.Errorf("%s: %d %s", p, h.Status, h.Raw)
		}
	}
}

// Зэрэг бичилт: id (оруулалтын дараалал) эрэмбэ, created_at нь clock_timestamp (транзакцийн эхлэл биш).
func TestAuditHistoryOrderByID(t *testing.T) {
	f := newAfx(t)
	// Хоёр транзакц: T1 эхэлж (now() = эрт), T2 дараа эхэлж өмнө commit. clock_timestamp() ба id-аар
	// шинэ нь эхэнд байх ёстой.
	tx1 := f.db.Begin()
	t.Cleanup(func() { tx1.Rollback() })
	if err := tx1.Exec("SELECT now()").Error; err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	tx2 := f.db.Begin()
	ins := `INSERT INTO khan.audit_log (org_id, resource, record_id, action, changes) VALUES (?::uuid, 'a.b', 'r', ?, '{}')`
	f.must2(tx2.Exec(ins, f.orgA, "update").Error, t) // эхэнд (жижиг id)
	f.must2(tx2.Commit().Error, t)
	f.must2(tx1.Exec(ins, f.orgA, "delete").Error, t) // сүүлд (том id) — гэвч tx1 эрт эхэлсэн
	f.must2(tx1.Commit().Error, t)
	p, err := audit.List(context.Background(), f.db, audit.Filter{OrgID: f.orgA, RecordID: "r"})
	f.must2(err, t)
	if len(p.Data) != 2 || p.Data[0].Action != "delete" || p.Data[1].Action != "update" {
		t.Fatalf("эрэмбэ: %+v", p.Data)
	}
	if !p.Data[0].CreatedAt.After(p.Data[1].CreatedAt) {
		t.Errorf("created_at clock_timestamp байх ёстой: %v vs %v", p.Data[0].CreatedAt, p.Data[1].CreatedAt)
	}
}

// Page cap, before_id keyset, has_more.
func TestAuditHistoryPaginationBounds(t *testing.T) {
	f := newAfx(t)
	app := auditApp(t, "/rooms", withAudit(f.roomResource(), nil))
	a := areq{org: f.orgA, user: uA}
	c := f.send(t, app, "POST", "/rooms", a, map[string]any{"name": "v0", "capacity": 0})
	mustStatus(t, c, 201)
	id := c.str("id")
	for v := 1; v <= 6; v++ {
		mustStatus(t, f.send(t, app, "PUT", "/rooms/"+id, a, map[string]any{"version": v, "capacity": v}), 200)
	}
	base := "/rooms/" + id + "/history"
	mustStatus(t, f.send(t, app, "GET", base+"?page=1001", a, nil), 400)
	mustStatus(t, f.send(t, app, "GET", base+"?page=1000", a, nil), 200)
	mustStatus(t, f.send(t, app, "GET", base+"?before_id=-1", a, nil), 400)
	p1 := f.send(t, app, "GET", base+"?limit=3", a, nil)
	if p1.JSON["has_more"] != true || p1.JSON["total"] != 7.0 || p1.JSON["total_capped"] != false || len(p1.list("data")) != 3 {
		t.Fatalf("p1: %s", p1.Raw)
	}
	next := fmt.Sprint(int64(p1.JSON["next_before_id"].(float64)))
	p2 := f.send(t, app, "GET", base+"?limit=3&before_id="+next, a, nil)
	if len(p2.list("data")) != 3 || p2.JSON["has_more"] != true {
		t.Fatalf("p2: %s", p2.Raw)
	}
	p3 := f.send(t, app, "GET", base+"?limit=3&before_id="+fmt.Sprint(int64(p2.JSON["next_before_id"].(float64))), a, nil)
	if len(p3.list("data")) != 1 || p3.JSON["has_more"] != false || p3.list("data")[0]["action"] != "create" {
		t.Fatalf("p3: %s", p3.Raw)
	}
}
