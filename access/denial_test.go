package access

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/khan-vision/khan/audit"
	"github.com/khan-vision/khan/obs"
	"github.com/khan-vision/khan/testkit"
	"gorm.io/gorm"
)

type denialRec struct {
	mu sync.Mutex
	ds []Denial
}

func (r *denialRec) hook(_ fiber.Ctx, d Denial) {
	r.mu.Lock()
	r.ds = append(r.ds, d)
	r.mu.Unlock()
}

func (r *denialRec) all() []Denial {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Denial(nil), r.ds...)
}

func TestRequire_403Body(t *testing.T) {
	p, _ := httpPolicy(t)
	reg := func(app *fiber.App) {
		app.Get("/one", p.Require("finance", "invoices.post"), func(c fiber.Ctx) error { return c.SendString("ok") })
		app.Get("/any", p.Require("finance", "invoices.post", "periods.reopen"), func(c fiber.Ctx) error { return c.SendString("ok") })
	}
	app := appWith(claims("viewer", orgA), reg)
	st, body := do(t, app, "GET", "/one", nil)
	var m map[string]any
	_ = json.Unmarshal([]byte(body), &m)
	if st != 403 || len(m) != 3 || m["error"] != MsgForbidden || m["module"] != "finance" || m["capability"] != "invoices.post" {
		t.Fatalf("%d %s", st, body)
	}
	st, body = do(t, app, "GET", "/any", nil)
	m = nil
	_ = json.Unmarshal([]byte(body), &m)
	caps, _ := m["capabilities"].([]any)
	if st != 403 || m["capability"] != "invoices.post" || len(caps) != 2 || caps[1] != "periods.reopen" {
		t.Fatalf("%d %s", st, body)
	}
	if strings.Contains(body, "viewer") || strings.Contains(body, orgA) {
		t.Fatalf("хэрэглэгчийн мэдээлэл алдагдсан: %s", body)
	}
}

func TestDenyHook(t *testing.T) {
	rec := &denialRec{}
	p, src := httpPolicy(t, WithDenyHook(rec.hook), WithErrorTTL(0), WithTTL(0))
	src.Set(orgA, "denied", g(map[string]ModuleGrant{"finance": {Roles: []string{"viewer"}, Deny: []string{"invoices.view"}}}))
	var last Denial
	var gotLast bool
	reg := func(app *fiber.App) {
		app.Use(obs.RequestID())
		app.Get("/post", p.Require("finance", "invoices.post"), func(c fiber.Ctx) error { return c.SendString("ok") })
		app.Get("/view", p.Require("finance", "invoices.view"), func(c fiber.Ctx) error { return c.SendString("ok") })
		app.Get("/rep", func(c fiber.Ctx) error {
			p.ReportDenied(c, "finance", "invoices.post")
			if d, ok := LastDenial(c); ok {
				last, gotLast = d, true
			}
			return c.SendString("x")
		})
		app.Get("/ok", func(c fiber.Ctx) error {
			_, has := LastDenial(c)
			if has {
				return c.SendStatus(500)
			}
			return c.SendString("ok")
		})
	}
	do(t, appWith(claims("viewer", orgA), reg), "GET", "/post", map[string]string{"X-Request-ID": "rid-1"})
	do(t, appWith(claims("denied", orgA), reg), "GET", "/view", nil)
	do(t, appWith(nil, reg), "GET", "/post", nil)
	src.SetError(errors.New("db down"))
	do(t, appWith(claims("viewer", orgA), reg), "GET", "/post", nil)
	src.SetError(nil)
	// ReportDenied: бүх салбар
	do(t, appWith(claims("viewer", orgA), reg), "GET", "/rep", nil)
	do(t, appWith(nil, reg), "GET", "/rep", nil)
	src.SetError(errors.New("db down"))
	do(t, appWith(claims("viewer", orgA), reg), "GET", "/rep", nil)
	src.SetError(nil)
	do(t, appWith(claims("boss", orgA), reg), "GET", "/rep", nil) // allowed → hook-гүй
	if st, _ := do(t, appWith(claims("boss", orgA), reg), "GET", "/ok", nil); st != 200 {
		t.Fatal("LastDenial зөвшөөрсөн хүсэлтэд тохирсон")
	}

	ds := rec.all()
	if len(ds) != 7 {
		t.Fatalf("%d denial: %+v", len(ds), ds)
	}
	d0 := ds[0]
	if d0.Org != orgA || d0.User != "viewer" || d0.Module != "finance" || len(d0.Caps) != 1 || d0.Caps[0] != "invoices.post" ||
		d0.Reason != DenialNoGrant || d0.RequestID != "rid-1" || d0.Path != "/post" || d0.Method != "GET" {
		t.Fatalf("%+v", d0)
	}
	if ds[1].Reason != DenialDeny || ds[2].Reason != DenialUnauthenticated || ds[3].Reason != DenialSourceError ||
		ds[4].Reason != DenialNoGrant || ds[5].Reason != DenialUnauthenticated || ds[6].Reason != DenialSourceError {
		t.Fatalf("%+v", ds)
	}
	if !gotLast || last.Module != "finance" {
		t.Fatalf("LastDenial: %+v", last)
	}
}

func TestDenyHook_SourceErrorReport(t *testing.T) {
	rec := &denialRec{}
	p, src := httpPolicy(t, WithDenyHook(rec.hook), WithTTL(0), WithErrorTTL(0))
	src.SetError(errors.New("db down"))
	app := appWith(claims("viewer", orgA), func(app *fiber.App) {
		app.Get("/rep", func(c fiber.Ctx) error { p.ReportDenied(c, "finance", "invoices.post"); return c.SendString("x") })
	})
	do(t, app, "GET", "/rep", nil)
	if ds := rec.all(); len(ds) != 1 || ds[0].Reason != DenialSourceError {
		t.Fatalf("%+v", ds)
	}
}

func TestDenyHook_PanicDoesNotChangeStatus(t *testing.T) {
	p, _ := httpPolicy(t, WithDenyHook(func(fiber.Ctx, Denial) { panic("boom") }))
	app := appWith(claims("viewer", orgA), func(app *fiber.App) {
		app.Get("/post", p.Require("finance", "invoices.post"), func(c fiber.Ctx) error { return c.SendString("ok") })
	})
	if st, _ := do(t, app, "GET", "/post", nil); st != 403 {
		t.Fatalf("status=%d", st)
	}
}

func TestWithDenyHook_Nil(t *testing.T) {
	if _, err := New(testRegistry(t), NewMemorySource(), WithDenyHook(nil)); err == nil {
		t.Fatal("nil hook алдаа өгөөгүй")
	}
}

func TestCrudCan_NoHook(t *testing.T) {
	rec := &denialRec{}
	p, _ := httpPolicy(t, WithDenyHook(rec.hook))
	can := p.CrudCan("finance", ActionCaps{Read: []string{"invoices.view"}, Create: []string{"invoices.create"}})
	app := appWith(claims("viewer", orgA), func(app *fiber.App) {
		app.Get("/x", func(c fiber.Ctx) error {
			if can(c, "create") {
				return c.SendStatus(500)
			}
			return c.SendString("ok")
		})
	})
	do(t, app, "GET", "/x", nil)
	if len(rec.all()) != 0 {
		t.Fatal("CrudCan hook дуудсан")
	}
}

// ─── AuditDenials ───

func auditFixture(t *testing.T) (*gorm.DB, string) {
	t.Helper()
	db := testkit.DB(t)
	if err := db.Exec(audit.SchemaSQL).Error; err != nil {
		t.Fatal(err)
	}
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	org := fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
	t.Cleanup(func() {
		// audit_log нь append-only (trigger) — зөвхөн audit.Purge-ээр, тухайн org-оор цэвэрлэнэ.
		if _, err := audit.Purge(context.Background(), db, time.Now(), audit.ForOrg(org)); err != nil {
			t.Errorf("audit цэвэрлэгээ: %v", err)
		}
	})
	return db, org
}

type auditRow struct {
	Resource, RecordID, Action, ActorID, RequestID string
	Changes                                        string
}

func auditRows(t *testing.T, db *gorm.DB, org string) []auditRow {
	t.Helper()
	var rows []auditRow
	err := db.Raw(`SELECT resource, record_id, action, COALESCE(actor_id,'') actor_id, COALESCE(request_id,'') request_id, changes::text changes
		FROM khan.audit_log WHERE org_id = ? ORDER BY id`, org).Scan(&rows).Error
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestAuditDenials(t *testing.T) {
	db, org := auditFixture(t)
	now := time.Date(2026, 11, 6, 9, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	advance := func(d time.Duration) { mu.Lock(); now = now.Add(d); mu.Unlock() }

	src := NewMemorySource()
	src.Set(org, "viewer", g(map[string]ModuleGrant{"finance": {Roles: []string{"viewer"}}}))
	p, _ := newTestPolicy(t, src, WithDenyHook(auditDenials(db, clock, time.Second)))
	app := appWith(claims("viewer", org), func(app *fiber.App) {
		app.Use(obs.RequestID())
		app.Get("/post", p.Require("finance", "invoices.post"), func(c fiber.Ctx) error { return c.SendString("ok") })
		app.Get("/reopen", p.Require("finance", "periods.reopen", "invoices.post"), func(c fiber.Ctx) error { return c.SendString("ok") })
	})
	for i := 0; i < 3; i++ { // rate-limit: 1 мөр
		if st, _ := do(t, app, "GET", "/post", map[string]string{"X-Request-ID": "rid-9"}); st != 403 {
			t.Fatal(st)
		}
	}
	rows := auditRows(t, db, org)
	if len(rows) != 1 {
		t.Fatalf("rate-limit ажиллаагүй: %+v", rows)
	}
	r := rows[0]
	if r.Resource != "finance" || r.Action != "access_denied" || r.RecordID != "no-grant:invoices.post" || r.ActorID != "viewer" || r.RequestID != "rid-9" || r.Changes != "{}" {
		t.Fatalf("%+v", r)
	}
	do(t, app, "GET", "/reopen", nil) // өөр cap → шинэ түлхүүр
	if n := len(auditRows(t, db, org)); n != 2 {
		t.Fatalf("n=%d", n)
	}
	advance(2 * time.Minute)
	do(t, app, "GET", "/post", nil)
	if n := len(auditRows(t, db, org)); n != 3 {
		t.Fatalf("минутын дараа бичигдээгүй: %d", n)
	}
}

func TestAuditDenials_SkipsAndFailures(t *testing.T) {
	db, org := auditFixture(t)
	h := AuditDenials(db)
	app := appWith(claims("viewer", org), func(app *fiber.App) {
		app.Get("/x", func(c fiber.Ctx) error {
			h(c, Denial{Org: "", Module: "finance", Reason: DenialUnauthenticated}) // org-гүй
			h(c, Denial{Org: org, Module: "", Reason: DenialNoGrant})               // module-гүй
			h(c, Denial{Org: org, Module: "Bad Module", Caps: []string{"x"}})       // audit validate алдаа → лог
			h(c, Denial{Org: org, Module: "finance", Caps: []string{strings.Repeat("я", 300)}, Reason: DenialDeny})
			return c.SendString("ok")
		})
	})
	do(t, app, "GET", "/x", nil)
	rows := auditRows(t, db, org)
	if len(rows) != 1 || len([]rune(rows[0].RecordID)) != auditMaxRecordN {
		t.Fatalf("%+v", rows)
	}
}

func TestAuditDenials_BoundedMap(t *testing.T) {
	now := time.Now()
	n := 0
	h := throttledAudit(func() time.Time { return now }, func(audit.Entry) error { n++; return nil })
	app := appWith(claims("viewer", orgA), func(app *fiber.App) {
		app.Get("/x", func(c fiber.Ctx) error {
			for i := 0; i < auditMaxKeys+5; i++ { // map дүүрч, цэвэрлэгдэнэ
				h(c, Denial{Org: orgA, User: fmt.Sprint(i), Module: "finance", Caps: []string{"c"}})
			}
			now = now.Add(2 * time.Minute) // хугацаа дууссаныг цэвэрлэх салбар
			for i := 0; i < auditMaxKeys+5; i++ {
				h(c, Denial{Org: orgA, User: "n" + fmt.Sprint(i), Module: "finance", Caps: []string{"c"}})
			}
			return c.SendString("ok")
		})
	})
	app.Get("/y", func(c fiber.Ctx) error { return nil })
	req := httptest.NewRequest("GET", "/x", nil)
	resp, err := app.Test(req, fiber.TestConfig{Timeout: 30 * time.Second})
	if err != nil || resp.StatusCode != 200 || n != 2*(auditMaxKeys+5) {
		t.Fatalf("%v n=%d", err, n)
	}
}

func TestAuditDenials_DBError(t *testing.T) {
	db, org := auditFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h := auditDenials(db.WithContext(ctx), time.Now, time.Second) // цуцлагдсан ctx → бичилт алдаатай, panic-гүй
	app := appWith(claims("viewer", org), func(app *fiber.App) {
		app.Get("/x", func(c fiber.Ctx) error {
			h(c, Denial{Org: org, Module: "finance", Caps: []string{"a"}, Reason: DenialNoGrant})
			return c.SendString("ok")
		})
	})
	if st, _ := do(t, app, "GET", "/x", nil); st != 200 {
		t.Fatal(st)
	}
}
