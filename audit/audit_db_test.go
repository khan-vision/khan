package audit

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/golang-jwt/jwt/v5"
	"github.com/khan-vision/khan/db"
	"github.com/khan-vision/khan/obs"
	"github.com/khan-vision/khan/tenant"
	"github.com/khan-vision/khan/testkit"
	"gorm.io/gorm"
)

func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

type afx struct {
	t          *testing.T
	db         *gorm.DB
	orgA, orgB string
}

func newAfx(t *testing.T) *afx {
	t.Helper()
	gdb := testkit.DB(t)
	if err := gdb.Exec(SchemaSQL).Error; err != nil {
		t.Fatal(err)
	}
	f := &afx{t: t, db: gdb, orgA: newUUID(), orgB: newUUID()}
	t.Cleanup(func() {
		for _, o := range []string{f.orgA, f.orgB} {
			_, _ = Purge(context.Background(), gdb, time.Now(), ForOrg(o))
		}
	})
	return f
}

func (f *afx) tx(fn func(tx *gorm.DB) error) error {
	return db.Tx(context.Background(), f.db, fn)
}

func (f *afx) must(err error) {
	f.t.Helper()
	if err != nil {
		f.t.Fatal(err)
	}
}

func (f *afx) count(org string) int {
	var n int
	f.must(f.db.Raw("SELECT count(*) FROM khan.audit_log WHERE org_id = ?", org).Scan(&n).Error)
	return n
}

func TestSchemaIdempotent(t *testing.T) {
	f := newAfx(t)
	f.must(f.db.Exec(SchemaSQL).Error)
	f.must(f.db.Exec(SchemaSQL).Error)
}

func TestRecordBasicAndColumns(t *testing.T) {
	f := newAfx(t)
	long := strings.Repeat("я", 400) + "\x00bad\xff"
	f.must(f.tx(func(tx *gorm.DB) error {
		return Record(tx, Entry{
			OrgID: f.orgA, Resource: "finance.invoice", RecordID: "42", Action: ActionUpdate,
			Meta:     Meta{ActorID: "u1", ActorRole: "1", IP: "10.0.0.1", UserAgent: long, RequestID: "req-1"},
			Changes:  Changes{"amount": {Old: 10, New: 20}, "password": {Old: "h1", New: "h2"}},
			Snapshot: map[string]any{"amount": 20, "token": "t"},
		})
	}))
	page, err := List(context.Background(), f.db, Filter{OrgID: f.orgA, Resource: "finance.invoice", RecordID: "42", IncludeSnapshot: true})
	f.must(err)
	if page.Total != 1 || len(page.Data) != 1 {
		t.Fatalf("page=%+v", page)
	}
	it := page.Data[0]
	if it.ActorID != "u1" || it.ActorRole != "1" || it.IP != "10.0.0.1" || it.RequestID != "req-1" || it.Action != "update" || it.ActionLabel != "Засварласан" {
		t.Errorf("item=%+v", it)
	}
	if n := len([]rune(it.UserAgent)); n != maxUserAgent || strings.ContainsRune(it.UserAgent, 0) || strings.ContainsRune(it.UserAgent, '�') {
		t.Errorf("user_agent таслагдаагүй/цэвэрлэгдээгүй: %d", n)
	}
	cs, err := it.ParsedChanges()
	f.must(err)
	if cs["amount"].Old != json.Number("10") || cs["amount"].New != json.Number("20") {
		t.Errorf("changes=%v", cs)
	}
	if cs["password"].Old != Redacted || cs["password"].New != Redacted {
		t.Errorf("password нууцлагдаагүй: %v", cs["password"])
	}
	if strings.Contains(string(it.Snapshot), `"t"`) || !strings.Contains(string(it.Snapshot), Redacted) {
		t.Errorf("snapshot token ил: %s", it.Snapshot)
	}
	var raw string
	f.must(f.db.Raw("SELECT changes::text || coalesce(snapshot::text,'') FROM khan.audit_log WHERE org_id = ?", f.orgA).Scan(&raw).Error)
	for _, leak := range []string{"h1", "h2"} {
		if strings.Contains(raw, leak) {
			t.Errorf("DB-д %s алдагдсан: %s", leak, raw)
		}
	}
	// snapshot-гүй (анхдагч) → snapshot талбар хариунд байхгүй
	page, _ = List(context.Background(), f.db, Filter{OrgID: f.orgA})
	if len(page.Data[0].Snapshot) != 0 {
		t.Error("IncludeSnapshot=false үед snapshot ирж болохгүй")
	}
}

func TestRecordNilChangesNullSnapshot(t *testing.T) {
	f := newAfx(t)
	f.must(f.tx(func(tx *gorm.DB) error {
		return Record(tx, Entry{OrgID: f.orgA, Resource: "x.y", RecordID: "1", Action: "post"})
	}))
	var snapNull bool
	var ch string
	f.must(f.db.Raw("SELECT snapshot IS NULL, changes::text FROM khan.audit_log WHERE org_id = ?", f.orgA).Row().Scan(&snapNull, &ch))
	if !snapNull || ch != "{}" {
		t.Errorf("snapNull=%v changes=%s", snapNull, ch)
	}
	page, _ := List(context.Background(), f.db, Filter{OrgID: f.orgA})
	if page.Data[0].ActionLabel != "post" || page.Data[0].ActorID != "" {
		t.Errorf("custom action: %+v", page.Data[0])
	}
}

func TestRecordValidation(t *testing.T) {
	f := newAfx(t)
	good := Entry{OrgID: f.orgA, Resource: "a.b", RecordID: "1", Action: ActionCreate}
	bads := map[string]func(*Entry){
		"org":      func(e *Entry) { e.OrgID = "" },
		"zero org": func(e *Entry) { e.OrgID = tenant.ZeroUUID },
		"resource": func(e *Entry) { e.Resource = "Bad Name" },
		"record":   func(e *Entry) { e.RecordID = "" },
		"long rec": func(e *Entry) { e.RecordID = strings.Repeat("x", 201) },
		"action":   func(e *Entry) { e.Action = "Bad-Action" },
		"no act":   func(e *Entry) { e.Action = "" },
	}
	for name, mut := range bads {
		e := good
		mut(&e)
		err := f.tx(func(tx *gorm.DB) error { return Record(tx, e) })
		if !errors.Is(err, ErrInvalidEntry) {
			t.Errorf("%s: ErrInvalidEntry хүлээсэн, %v", name, err)
		}
	}
	// org uuid биш → DB алдаа (бичигдэхгүй)
	e := good
	e.OrgID = "not-a-uuid"
	if err := f.tx(func(tx *gorm.DB) error { return Record(tx, e) }); err == nil {
		t.Error("uuid биш org: алдаа хүлээсэн")
	}
	if err := Record(f.db, good); !errors.Is(err, ErrNotInTx) {
		t.Errorf("транзакцгүй: %v", err)
	}
	if err := Record(nil, good); err == nil {
		t.Error("nil db")
	}
	f.must(f.tx(func(tx *gorm.DB) error { return RecordMany(tx, nil) }))
	// нэг нь буруу бол юу ч бичигдэхгүй
	err := f.tx(func(tx *gorm.DB) error {
		bad := good
		bad.RecordID = ""
		return RecordMany(tx, []Entry{good, bad})
	})
	if err == nil || f.count(f.orgA) != 0 {
		t.Errorf("err=%v count=%d", err, f.count(f.orgA))
	}
}

func TestRecordRollsBackWithBusinessTx(t *testing.T) {
	f := newAfx(t)
	boom := errors.New("boom")
	err := f.tx(func(tx *gorm.DB) error {
		if err := Record(tx, Entry{OrgID: f.orgA, Resource: "a.b", RecordID: "1", Action: ActionCreate}); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) || f.count(f.orgA) != 0 {
		t.Errorf("rollback болоогүй: err=%v n=%d", err, f.count(f.orgA))
	}
}

func TestRecordManyChunks(t *testing.T) {
	f := newAfx(t)
	n := 1203 // 500-ын chunk-ээс олон
	es := make([]Entry, n)
	for i := range es {
		es[i] = Entry{OrgID: f.orgA, Resource: "a.b", RecordID: fmt.Sprint(i), Action: ActionBulkUpdate,
			Changes: Changes{"v": {Old: i, New: i + 1}}}
	}
	f.must(f.tx(func(tx *gorm.DB) error { return RecordMany(tx, es) }))
	if got := f.count(f.orgA); got != n {
		t.Errorf("count=%d want %d", got, n)
	}
}

func TestAppendOnlyTrigger(t *testing.T) {
	f := newAfx(t)
	f.must(f.tx(func(tx *gorm.DB) error {
		return Record(tx, Entry{OrgID: f.orgA, Resource: "a.b", RecordID: "1", Action: ActionCreate})
	}))
	err := f.db.Exec("UPDATE khan.audit_log SET action = 'x' WHERE org_id = ?", f.orgA).Error
	if err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Errorf("UPDATE хориглох ёстой: %v", err)
	}
	if err := f.db.Exec("UPDATE khan.audit_log SET changes = '{}' WHERE org_id = ?", f.orgA).Error; err == nil {
		t.Error("UPDATE changes хориглох ёстой")
	}
}

// insertAt — тодорхой created_at-тай бичлэг (trigger UPDATE-ийг хориглодог тул шууд INSERT).
func (f *afx) insertAt(org, resource, record, action, actor, req string, at time.Time) {
	f.t.Helper()
	f.must(f.db.Exec(`INSERT INTO khan.audit_log (org_id, resource, record_id, action, actor_id, request_id, changes, created_at)
		VALUES (?::uuid, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), '{"n":[0,1]}'::jsonb, ?)`,
		org, resource, record, action, actor, req, at).Error)
}

func TestListFilterPaginationOrder(t *testing.T) {
	f := newAfx(t)
	base := time.Now().Add(-48 * time.Hour).UTC().Truncate(time.Second)
	for i := 0; i < 25; i++ {
		actor, act := "alice", "update"
		if i%5 == 0 {
			actor = "bob"
		}
		if i == 0 {
			act = "create"
		}
		f.insertAt(f.orgA, "fin.inv", "1", act, actor, fmt.Sprintf("r%d", i), base.Add(time.Duration(i)*time.Hour))
	}
	f.insertAt(f.orgA, "fin.inv", "2", "create", "bob", "", base)
	f.insertAt(f.orgA, "fin.other", "1", "create", "bob", "", base)
	f.insertAt(f.orgB, "fin.inv", "1", "create", "bob", "", base)
	ctx := context.Background()

	// Шинэ нь эхэнд; хуудаслалт
	p1, err := List(ctx, f.db, Filter{OrgID: f.orgA, Resource: "fin.inv", RecordID: "1", Limit: 10})
	f.must(err)
	if p1.Total != 25 || len(p1.Data) != 10 || p1.Page != 1 || p1.Limit != 10 {
		t.Fatalf("p1=%d/%d", p1.Total, len(p1.Data))
	}
	if p1.Data[0].RequestID != "r24" || p1.Data[9].RequestID != "r15" {
		t.Errorf("эрэмбэ: %s ... %s", p1.Data[0].RequestID, p1.Data[9].RequestID)
	}
	p3, _ := List(ctx, f.db, Filter{OrgID: f.orgA, Resource: "fin.inv", RecordID: "1", Limit: 10, Page: 3})
	if len(p3.Data) != 5 || p3.Data[4].RequestID != "r0" {
		t.Errorf("p3=%d", len(p3.Data))
	}
	p4, _ := List(ctx, f.db, Filter{OrgID: f.orgA, Resource: "fin.inv", RecordID: "1", Limit: 10, Page: 4})
	if len(p4.Data) != 0 || p4.Total != 25 {
		t.Errorf("p4=%d", len(p4.Data))
	}
	// Анхдагч/дээд limit
	pd, _ := List(ctx, f.db, Filter{OrgID: f.orgA, Resource: "fin.inv", RecordID: "1"})
	if pd.Limit != DefaultLimit || len(pd.Data) != DefaultLimit {
		t.Errorf("анхдагч limit: %d", len(pd.Data))
	}
	pm, _ := List(ctx, f.db, Filter{OrgID: f.orgA, Limit: 100000, Page: -3})
	if pm.Limit != MaxLimit || pm.Page != 1 {
		t.Errorf("max limit: %+v", pm.Limit)
	}
	// Шүүлтүүд
	if p, _ := List(ctx, f.db, Filter{OrgID: f.orgA, Resource: "fin.inv", ActorID: "bob"}); p.Total != 6 { // i=0,5,10,15,20 + record 2
		t.Errorf("actor total=%d", p.Total)
	}
	if p, _ := List(ctx, f.db, Filter{OrgID: f.orgA, Action: "create"}); p.Total != 3 {
		t.Errorf("action total=%d", p.Total)
	}
	if p, _ := List(ctx, f.db, Filter{OrgID: f.orgA, RequestID: "r7"}); p.Total != 1 {
		t.Errorf("request total=%d", p.Total)
	}
	if p, _ := List(ctx, f.db, Filter{OrgID: f.orgA, Resource: "fin.inv", RecordID: "1", From: base.Add(10 * time.Hour), To: base.Add(14 * time.Hour)}); p.Total != 4 {
		t.Errorf("хугацааны total=%d", p.Total) // 10,11,12,13 ([from,to))
	}
	// Org тусгаарлалт
	pb, _ := List(ctx, f.db, Filter{OrgID: f.orgB})
	if pb.Total != 1 {
		t.Errorf("orgB=%d", pb.Total)
	}
	// Хоосон org → алдаа (fail-closed)
	if _, err := List(ctx, f.db, Filter{}); err == nil {
		t.Error("OrgID хоосон: алдаа хүлээсэн")
	}
	if _, err := List(ctx, nil, Filter{OrgID: f.orgA}); err == nil {
		t.Error("nil db")
	}
	// Олдохгүй → хоосон (nil биш) жагсаалт
	pn, _ := List(ctx, f.db, Filter{OrgID: f.orgA, RecordID: "nope"})
	if pn.Data == nil || len(pn.Data) != 0 {
		t.Error("олдохгүй үед [] байх ёстой")
	}
	if b, _ := json.Marshal(pn); !strings.Contains(string(b), `"data":[]`) {
		t.Errorf("JSON: %s", b)
	}
}

func TestPurge(t *testing.T) {
	f := newAfx(t)
	now := time.Now().UTC()
	for i := 0; i < 12; i++ {
		f.insertAt(f.orgA, "a.b", fmt.Sprint(i), "create", "", "", now.Add(-time.Duration(100+i)*24*time.Hour))
	}
	f.insertAt(f.orgA, "a.c", "x", "create", "", "", now.Add(-200*24*time.Hour))
	f.insertAt(f.orgA, "a.b", "new", "create", "", "", now.Add(-time.Hour))
	f.insertAt(f.orgB, "a.b", "old", "create", "", "", now.Add(-300*24*time.Hour))
	ctx := context.Background()
	cutoff := now.Add(-30 * 24 * time.Hour)

	if _, err := Purge(ctx, f.db, time.Time{}); !errors.Is(err, ErrPurgeCutoff) {
		t.Errorf("zero cutoff: %v", err)
	}
	if _, err := Purge(ctx, f.db, now.Add(time.Hour)); !errors.Is(err, ErrPurgeCutoff) {
		t.Errorf("ирээдүй cutoff: %v", err)
	}
	if _, err := Purge(ctx, nil, cutoff); err == nil {
		t.Error("nil db")
	}
	if _, err := Purge(ctx, f.db, cutoff); !errors.Is(err, ErrPurgeScope) {
		t.Errorf("хүрээгүй Purge: %v", err)
	}
	if f.count(f.orgA) != 14 {
		t.Error("хүрээгүй Purge устгасан")
	}
	// Resource + org шүүлт, жижиг batch (олон давталт)
	n, err := Purge(ctx, f.db, cutoff, ForOrg(f.orgA), ForResource("a.b"), Batch(5))
	f.must(err)
	if n != 12 {
		t.Errorf("устгасан=%d want 12", n)
	}
	if f.count(f.orgA) != 2 || f.count(f.orgB) != 1 {
		t.Errorf("A=%d B=%d", f.count(f.orgA), f.count(f.orgB))
	}
	// Зөвхөн A org (бүх resource)
	n, err = Purge(ctx, f.db, cutoff, ForOrg(f.orgA), Batch(-1))
	f.must(err)
	if n != 1 || f.count(f.orgA) != 1 || f.count(f.orgB) != 1 {
		t.Errorf("n=%d A=%d B=%d", n, f.count(f.orgA), f.count(f.orgB))
	}
	// Цуцлагдсан ctx
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := Purge(cctx, f.db, cutoff, ForOrg(f.orgB)); !errors.Is(err, context.Canceled) {
		t.Errorf("cancel: %v", err)
	}
	if f.count(f.orgB) != 1 {
		t.Error("цуцлагдсан Purge устгасан")
	}
	n, err = Purge(ctx, f.db, cutoff, ForOrg(f.orgB))
	f.must(err)
	if n != 1 {
		t.Errorf("orgB n=%d", n)
	}
}

// ─── HTTP ───

func claimsMW(c fiber.Ctx) error {
	if org := c.Get("X-T-Org"); org != "" {
		c.Locals(tenant.LocalsKey, &jwt.Token{Claims: jwt.MapClaims{"id": "u-1", "org_id": org, "role": 1}, Valid: true})
	}
	return c.Next()
}

func (f *afx) app(t *testing.T, cfg HandlerConfig) *fiber.App {
	t.Helper()
	cfg.DB = func() *gorm.DB { return f.db }
	h, err := Handler(cfg)
	f.must(err)
	app := fiber.New()
	g := app.Group("/api", claimsMW)
	g.Get("/history", h.List)
	g.Get("/:id/history", h.Record)
	return app
}

func get(t *testing.T, app *fiber.App, path, org string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	if org != "" {
		req.Header.Set("X-T-Org", org)
	}
	res, err := app.Test(req, fiber.TestConfig{Timeout: 0})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(res.Body)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return res.StatusCode, m
}

func TestHandlerConfigErrors(t *testing.T) {
	ok := HandlerConfig{Resource: "a.b", DB: func() *gorm.DB { return nil }, Can: func(fiber.Ctx) bool { return true }}
	for name, mut := range map[string]func(*HandlerConfig){
		"resource": func(c *HandlerConfig) { c.Resource = "Bad" },
		"db":       func(c *HandlerConfig) { c.DB = nil },
		"can":      func(c *HandlerConfig) { c.Can = nil },
	} {
		c := ok
		mut(&c)
		if _, err := Handler(c); err == nil {
			t.Errorf("%s: алдаа хүлээсэн", name)
		}
	}
	if _, err := Handler(ok); err != nil {
		t.Fatal(err)
	}
}

func TestHandlerRecordAndListIsolation(t *testing.T) {
	f := newAfx(t)
	now := time.Now().UTC()
	for i := 0; i < 3; i++ {
		f.insertAt(f.orgA, "fin.inv", "7", "update", "alice", "", now.Add(-time.Duration(3-i)*time.Hour))
	}
	f.insertAt(f.orgA, "fin.inv", "8", "create", "bob", "", now)
	f.insertAt(f.orgA, "fin.other", "7", "create", "bob", "", now)
	f.insertAt(f.orgB, "fin.inv", "7", "create", "eve", "", now)
	app := f.app(t, HandlerConfig{Resource: "fin.inv", Can: func(fiber.Ctx) bool { return true }})

	code, m := get(t, app, "/api/7/history", f.orgA)
	if code != 200 || m["total"] != 3.0 || len(m["data"].([]any)) != 3 {
		t.Fatalf("A record: %d %v", code, m)
	}
	first := m["data"].([]any)[0].(map[string]any)
	if first["action_label"] != "Засварласан" || first["actor_id"] != "alice" {
		t.Errorf("item=%v", first)
	}
	// Org B өөрийн түүхийг л харна: A-ийн «7»-ийг ХАРАХГҮЙ
	code, m = get(t, app, "/api/7/history", f.orgB)
	if code != 200 || m["total"] != 1.0 {
		t.Fatalf("B record: %d %v", code, m)
	}
	if m["data"].([]any)[0].(map[string]any)["actor_id"] != "eve" {
		t.Error("B өөр org-ийн мөр харав")
	}
	// B-д A-ийн «8» байхгүй
	if _, m = get(t, app, "/api/8/history", f.orgB); m["total"] != 0.0 || len(m["data"].([]any)) != 0 {
		t.Errorf("B 8: %v", m)
	}
	// List: resource-оор, actor-оор
	if _, m = get(t, app, "/api/history", f.orgA); m["total"] != 4.0 {
		t.Errorf("A list: %v", m)
	}
	if _, m = get(t, app, "/api/history?actor_id=bob", f.orgA); m["total"] != 1.0 {
		t.Errorf("actor: %v", m)
	}
	if _, m = get(t, app, "/api/history?record_id=7&action=update&limit=2&page=2", f.orgA); m["total"] != 3.0 || len(m["data"].([]any)) != 1 {
		t.Errorf("page2: %v", m)
	}
	today := now.Format("2006-01-02")
	if _, m = get(t, app, "/api/history?from="+today+"&to="+today, f.orgA); m["total"] == nil {
		t.Errorf("date: %v", m)
	}
	if _, m = get(t, app, "/api/history?to=2000-01-01", f.orgA); m["total"] != 0.0 {
		t.Errorf("to өнгөрсөн: %v", m)
	}
	if _, m = get(t, app, "/api/history?from="+now.Add(-time.Hour*24).Format(time.RFC3339), f.orgA); m["total"] != 4.0 {
		t.Errorf("from rfc3339: %v", m)
	}
	// snapshot
	f.must(f.db.Exec(`INSERT INTO khan.audit_log (org_id, resource, record_id, action, snapshot) VALUES (?::uuid,'fin.inv','9','create','{"a":1}'::jsonb)`, f.orgA).Error)
	_, m = get(t, app, "/api/9/history", f.orgA)
	if _, has := m["data"].([]any)[0].(map[string]any)["snapshot"]; has {
		t.Error("snapshot анхдагчаар ирж болохгүй")
	}
	_, m = get(t, app, "/api/9/history?snapshot=1", f.orgA)
	if m["data"].([]any)[0].(map[string]any)["snapshot"] == nil {
		t.Error("snapshot=1 үед ирэх ёстой")
	}
}

func TestHandlerAuthAndValidation(t *testing.T) {
	f := newAfx(t)
	can := false
	app := f.app(t, HandlerConfig{Resource: "fin.inv", Can: func(fiber.Ctx) bool { return can },
		ParseID: func(s string) (string, bool) { return s, s != "bad" }})
	if code, _ := get(t, app, "/api/1/history", ""); code != 401 {
		t.Errorf("JWT-гүй: %d", code)
	}
	if code, _ := get(t, app, "/api/history", ""); code != 401 {
		t.Errorf("JWT-гүй list: %d", code)
	}
	if code, _ := get(t, app, "/api/1/history", f.orgA); code != 403 {
		t.Errorf("эрхгүй: %d", code)
	}
	can = true
	for _, p := range []string{
		"/api/bad/history", "/api/1/history?page=0", "/api/1/history?limit=0", "/api/1/history?page=99999999",
		"/api/1/history?from=zzz", "/api/1/history?to=2026-13-45", "/api/history?actor_id=a%00b", "/api/history?record_id=%00",
	} {
		if code, m := get(t, app, p, f.orgA); code != 400 {
			t.Errorf("%s: %d %v", p, code, m)
		}
	}
	// client-ийн X-Org-ID үл тоогдоно
	req := httptest.NewRequest("GET", "/api/1/history", nil)
	req.Header.Set("X-Org-ID", f.orgB)
	if res, _ := app.Test(req, fiber.TestConfig{Timeout: 0}); res.StatusCode != 401 {
		t.Errorf("X-Org-ID итгэгдсэн: %d", res.StatusCode)
	}
}

func TestHandlerDBFailures(t *testing.T) {
	f := newAfx(t)
	// DB nil
	h, err := Handler(HandlerConfig{Resource: "a.b", DB: func() *gorm.DB { return nil }, Can: func(fiber.Ctx) bool { return true }})
	f.must(err)
	app := fiber.New()
	app.Get("/h", claimsMW, h.List)
	req := httptest.NewRequest("GET", "/h", nil)
	req.Header.Set("X-T-Org", f.orgA)
	if res, _ := app.Test(req, fiber.TestConfig{Timeout: 0}); res.StatusCode != 500 {
		t.Errorf("DB nil: %d", res.StatusCode)
	}
	// Хаагдсан холболт → 500, дотоод алдаа body-д ИЛ БИШ
	closed := testkit.DB(t)
	f.must(db.Close(closed))
	h2, err := Handler(HandlerConfig{Resource: "a.b", Can: func(fiber.Ctx) bool { return true }, DB: func() *gorm.DB { return closed }})
	f.must(err)
	app2 := fiber.New()
	app2.Get("/h", claimsMW, h2.List)
	res, _ := app2.Test(httptest.NewRequest("GET", "/h", nil).WithContext(context.Background()), fiber.TestConfig{Timeout: 0})
	_ = res
	req = httptest.NewRequest("GET", "/h", nil)
	req.Header.Set("X-T-Org", f.orgA)
	res, _ = app2.Test(req, fiber.TestConfig{Timeout: 0})
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode != 500 || !strings.Contains(string(raw), "Дотоод алдаа") || strings.Contains(string(raw), "sql") {
		t.Errorf("closed DB: %d %s", res.StatusCode, raw)
	}
}

func TestHandlerRLSPath(t *testing.T) {
	f := newAfx(t)
	f.insertAt(f.orgA, "fin.inv", "1", "create", "a", "", time.Now())
	app := f.app(t, HandlerConfig{Resource: "fin.inv", RLS: true, Can: func(fiber.Ctx) bool { return true }})
	if code, m := get(t, app, "/api/1/history", f.orgA); code != 200 || m["total"] != 1.0 {
		t.Errorf("RLS: %d %v", code, m)
	}
	if _, m := get(t, app, "/api/1/history", f.orgB); m["total"] != 0.0 {
		t.Errorf("RLS B: %v", m)
	}
}

func TestMetaFrom(t *testing.T) {
	app := fiber.New()
	app.Use(obs.RequestID(), claimsMW)
	var got Meta
	app.Get("/m", func(c fiber.Ctx) error { got = MetaFrom(c); return c.SendStatus(200) })
	req := httptest.NewRequest("GET", "/m", nil)
	req.Header.Set("X-T-Org", newUUID())
	req.Header.Set("User-Agent", "UA/1.0")
	req.Header.Set("X-Request-ID", "rid-123")
	if _, err := app.Test(req, fiber.TestConfig{Timeout: 0}); err != nil {
		t.Fatal(err)
	}
	if got.ActorID != "u-1" || got.ActorRole != "1" || got.UserAgent != "UA/1.0" || got.RequestID != "rid-123" || got.IP == "" {
		t.Errorf("meta=%+v", got)
	}
	// Нэвтрээгүй
	got = Meta{}
	req = httptest.NewRequest("GET", "/m", nil)
	if _, err := app.Test(req, fiber.TestConfig{Timeout: 0}); err != nil {
		t.Fatal(err)
	}
	if got.ActorID != "" || got.ActorRole != "" || got.RequestID == "" {
		t.Errorf("anon meta=%+v", got)
	}
}

func TestActionLabels(t *testing.T) {
	for a, want := range map[Action]string{ActionCreate: "Үүсгэсэн", ActionUpdate: "Засварласан", ActionDelete: "Устгасан",
		ActionRestore: "Сэргээсэн", ActionBulkUpdate: "Бөөнөөр засварласан", ActionBulkDelete: "Бөөнөөр устгасан", "approve": "approve"} {
		if a.Label() != want {
			t.Errorf("%s: %s", a, a.Label())
		}
	}
}

func TestAppendOnlyDeleteTruncateGuard(t *testing.T) {
	f := newAfx(t)
	f.must(f.tx(func(tx *gorm.DB) error {
		return Record(tx, Entry{OrgID: f.orgA, Resource: "a.b", RecordID: "1", Action: ActionCreate})
	}))
	err := f.db.Exec("DELETE FROM khan.audit_log WHERE org_id = ?", f.orgA).Error
	if err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Errorf("DELETE хориглох ёстой: %v", err)
	}
	err = f.db.Exec("TRUNCATE khan.audit_log").Error
	if err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Errorf("TRUNCATE хориглох ёстой: %v", err)
	}
	// Тохиргоо off / өөр утга бол хориглоно; нэг транзакцийн local тохиргоо дараагийн транзакцад үлдэхгүй
	f.must(f.tx(func(tx *gorm.DB) error { return tx.Exec("SELECT set_config('khan.audit_purge','on',true)").Error }))
	if err := f.db.Exec("DELETE FROM khan.audit_log WHERE org_id = ?", f.orgA).Error; err == nil {
		t.Error("local тохиргоо транзакцаас гадуур үлдсэн")
	}
	if err := f.tx(func(tx *gorm.DB) error {
		tx.Exec("SELECT set_config('khan.audit_purge','off',true)")
		return tx.Exec("DELETE FROM khan.audit_log WHERE org_id = ?", f.orgA).Error
	}); err == nil {
		t.Error("purge=off үед DELETE хориглох ёстой")
	}
	if f.count(f.orgA) != 1 {
		t.Error("мөр устсан")
	}
	// AllOrgs ил зөвшөөрөлтэй Purge ажиллана
	n, err := Purge(context.Background(), f.db, time.Now(), AllOrgs(), Batch(1))
	f.must(err)
	if n < 1 || f.count(f.orgA) != 0 {
		t.Errorf("AllOrgs purge n=%d", n)
	}
}

func TestSchemaUpgradesOldDefaultAndTriggerWithoutRelocking(t *testing.T) {
	f := newAfx(t)
	// Хуучин төлөв: now() default, зөвхөн UPDATE trigger
	f.must(f.db.Exec(`ALTER TABLE khan.audit_log ALTER COLUMN created_at SET DEFAULT now()`).Error)
	f.must(f.db.Exec(`CREATE OR REPLACE TRIGGER audit_log_no_update BEFORE UPDATE ON khan.audit_log FOR EACH ROW EXECUTE FUNCTION khan.audit_log_immutable()`).Error)
	f.must(f.db.Exec(SchemaSQL).Error)
	var def string
	f.must(f.db.Raw(`SELECT pg_get_expr(d.adbin, d.adrelid) FROM pg_attrdef d JOIN pg_attribute a ON a.attrelid=d.adrelid AND a.attnum=d.adnum
		WHERE d.adrelid='khan.audit_log'::regclass AND a.attname='created_at'`).Scan(&def).Error)
	if !strings.HasPrefix(def, "clock_timestamp()") {
		t.Errorf("default=%q", def)
	}
	var tg int
	f.must(f.db.Raw(`SELECT tgtype FROM pg_trigger WHERE tgrelid='khan.audit_log'::regclass AND tgname='audit_log_no_update'`).Scan(&tg).Error)
	if tg&24 != 24 {
		t.Errorf("trigger UPDATE+DELETE биш: %d", tg)
	}
	// Дахин ажиллуулахад trigger-ийн oid өөрчлөгдөхгүй (drop/create хийгдэхгүй)
	var oid1, oid2 int64
	q := `SELECT oid::bigint FROM pg_trigger WHERE tgrelid='khan.audit_log'::regclass AND tgname='audit_log_no_update'`
	f.must(f.db.Raw(q).Scan(&oid1).Error)
	f.must(f.db.Exec(SchemaSQL).Error)
	f.must(f.db.Raw(q).Scan(&oid2).Error)
	if oid1 != oid2 {
		t.Errorf("trigger дахин үүссэн: %d → %d", oid1, oid2)
	}
}

func TestListOrderByIDAndBounds(t *testing.T) {
	f := newAfx(t)
	base := time.Now().Add(-time.Hour).UTC()
	// id өсөх дарааллаар, created_at НЭГЭН ЗЭРэг БУРУУ (хожуу id нь эрт цагтай): эрэмбэ id-аар
	f.insertAt(f.orgA, "a.b", "1", "create", "", "first", base.Add(10*time.Minute))
	f.insertAt(f.orgA, "a.b", "1", "update", "", "second", base)
	ctx := context.Background()
	p, err := List(ctx, f.db, Filter{OrgID: f.orgA, RecordID: "1"})
	f.must(err)
	if p.Data[0].RequestID != "second" || p.Data[1].RequestID != "first" {
		t.Errorf("id DESC эрэмбэ: %s,%s", p.Data[0].RequestID, p.Data[1].RequestID)
	}
	if _, err := List(ctx, f.db, Filter{OrgID: f.orgA, Page: MaxPage + 1}); !errors.Is(err, ErrPageTooDeep) {
		t.Errorf("page cap: %v", err)
	}
	// Keyset + has_more
	for i := 0; i < 5; i++ {
		f.insertAt(f.orgA, "a.b", "2", "update", "", fmt.Sprintf("k%d", i), base)
	}
	k1, err := List(ctx, f.db, Filter{OrgID: f.orgA, RecordID: "2", Limit: 2})
	f.must(err)
	if !k1.HasMore || len(k1.Data) != 2 || k1.NextBeforeID != k1.Data[1].ID || k1.Total != 5 || k1.TotalCapped {
		t.Fatalf("k1=%+v", k1)
	}
	k3, err := List(ctx, f.db, Filter{OrgID: f.orgA, RecordID: "2", Limit: 2, BeforeID: k1.NextBeforeID})
	f.must(err)
	k3, err = List(ctx, f.db, Filter{OrgID: f.orgA, RecordID: "2", Limit: 2, BeforeID: k3.NextBeforeID})
	f.must(err)
	if k3.HasMore || len(k3.Data) != 1 || k3.Data[0].RequestID != "k0" || k3.NextBeforeID != 0 {
		t.Fatalf("k3=%+v", k3)
	}
	// Total cap: MaxTotal+1 мөр оруулж (generate_series) — total=MaxTotal, capped
	f.must(f.db.Exec(`INSERT INTO khan.audit_log (org_id, resource, record_id, action)
		SELECT ?::uuid, 'a.big', 'x', 'update' FROM generate_series(1, ?)`, f.orgA, MaxTotal+5).Error)
	big, err := List(ctx, f.db, Filter{OrgID: f.orgA, Resource: "a.big", Limit: 10})
	f.must(err)
	if big.Total != MaxTotal || !big.TotalCapped || !big.HasMore {
		t.Errorf("big total=%d capped=%v more=%v", big.Total, big.TotalCapped, big.HasMore)
	}
}
