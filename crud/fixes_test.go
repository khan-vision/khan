package crud

import (
	"fmt"
	"net/http/httptest"
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

// Bulk нь энгийн update-тэй ижил дүрэмтэй: version+1, updated_at/updated_by, мөр бүрт event,
// өөр org-ийн id-д юу ч хийхгүй. Bulk-ийн дараа хуучин version-оор PUT → 409.
func TestBulkBumpsVersionTimestampsAndEvents(t *testing.T) {
	f := newFx(t)
	app := mountOn(t, "/rooms", f.roomResource())
	var idsA []string
	for i := 0; i < 2; i++ {
		idsA = append(idsA, do(t, app, "POST", "/rooms", f.orgA, 1, map[string]any{"name": fmt.Sprint("a", i)}).str("id"))
	}
	idB := do(t, app, "POST", "/rooms", f.orgB, 1, map[string]any{"name": "b"}).str("id")
	room := f.tbl("room")
	f.exec(`UPDATE ` + room + ` SET updated_at = NULL, updated_by = NULL`)

	u := do(t, app, "POST", "/rooms/bulk", f.orgA, 1, map[string]any{
		"action": "update", "ids": append(append([]string{}, idsA...), idB), "set": map[string]any{"capacity": 7},
	})
	mustStatus(t, u, 200)
	if u.JSON["affected"] != float64(2) {
		t.Fatalf("affected: %s", u.Raw)
	}
	if n := f.count(`SELECT count(*) FROM `+room+` WHERE org_id = ? AND version = 2 AND updated_at IS NOT NULL AND updated_by IS NOT NULL`, f.orgA); n != 2 {
		t.Errorf("org A: version/updated_at/updated_by шинэчлэгдсэн мөр = %d, want 2", n)
	}
	if n := f.count(`SELECT count(*) FROM `+room+` WHERE id = ? AND version = 1 AND capacity IS NULL AND updated_at IS NULL`, idB); n != 1 {
		t.Error("org B-ийн мөр bulk-д өртөв")
	}
	if n := f.count(`SELECT count(*) FROM khan.outbox_events WHERE topic = 'test.room.updated' AND org_id = ? AND aggregate_id IN (?, ?) AND payload->'values'->>'capacity' = '7'`, f.orgA, idsA[0], idsA[1]); n != 2 {
		t.Errorf("bulk update event = %d, want 2", n)
	}
	if n := f.count(`SELECT count(*) FROM khan.outbox_events WHERE aggregate_id = ?`, idB); n != 1 { // зөвхөн created
		t.Errorf("org B-д bulk event бичигдэв (%d)", n)
	}
	// Bulk version-ийг нэмсэн тул хуучин version (1)-оор PUT → 409.
	mustStatus(t, do(t, app, "PUT", "/rooms/"+idsA[0], f.orgA, 1, map[string]any{"name": "x", "version": 1}), 409)
	mustStatus(t, do(t, app, "PUT", "/rooms/"+idsA[0], f.orgA, 1, map[string]any{"name": "x", "version": 2}), 200)

	d := do(t, app, "POST", "/rooms/bulk", f.orgA, 1, map[string]any{"action": "delete", "ids": idsA})
	mustStatus(t, d, 200)
	if n := f.count(`SELECT count(*) FROM khan.outbox_events WHERE topic = 'test.room.deleted' AND org_id = ?`, f.orgA); n != 2 {
		t.Errorf("bulk delete event = %d, want 2", n)
	}
}

// Жагсаалтын Money/Number нь float64-оор алдагдахгүй — яг decimal цифрүүд.
func TestListDecimalsExact(t *testing.T) {
	f := newFx(t)
	app := mountOn(t, "/rooms", f.roomResource())
	mustStatus(t, do(t, app, "POST", "/rooms", f.orgA, 1, map[string]any{"name": "max", "price": "9999999999999999.99"}), 201)
	mustStatus(t, do(t, app, "POST", "/rooms", f.orgA, 1, map[string]any{"name": "nul"}), 201)
	l := do(t, app, "GET", "/rooms?sort=name&dir=asc", f.orgA, 2, nil)
	mustStatus(t, l, 200)
	if !strings.Contains(string(l.Raw), `"price":9999999999999999.99`) {
		t.Fatalf("жагсаалтын үнэ яг биш: %s", l.Raw)
	}
	if !strings.Contains(string(l.Raw), `"price":null`) {
		t.Fatalf("null үнэ null хэвээр байх ёстой: %s", l.Raw)
	}
}

// rlsDB — superuser БИШ role-оор холбогдсон, нэг connection-той DB (SET ROLE session-д
// тогтоно). Superuser-т RLS үйлчилдэггүй тул бодит policy-г ингэж л шалгана.
func rlsDB(t *testing.T, f *fx) *gorm.DB {
	t.Helper()
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
	for _, tbl := range []string{"room", "person"} {
		sql, err := tenant.PolicySQL(f.tbl(tbl), tenant.PolicyOptions{Mode: tenant.PolicyStrict})
		if err != nil {
			t.Fatal(err)
		}
		f.exec(sql)
	}
	cfg.Pool = config.Pool{MaxOpen: 1, MaxIdle: 1}
	rdb, err := db.Open(cfg, db.WithLogger(logger.Discard))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close(rdb) })
	if err := rdb.Exec(`SET ROLE ` + role).Error; err != nil {
		t.Fatal(err)
	}
	// Policy бодитоор ажиллаж буйг баталгаажуулна: app.org_id-гүй бол юу ч харагдахгүй.
	var n int
	if err := rdb.Raw(`SELECT count(*) FROM ` + f.tbl("room")).Scan(&n).Error; err != nil || n != 0 {
		t.Fatalf("strict RLS ажиллахгүй байна: n=%d err=%v", n, err)
	}
	return rdb
}

// RLS: true үед жагсаалт, export, options, detail нь tenant.Tx дотор (strict policy-д ч ажиллана).
func TestRLSStrictReadPaths(t *testing.T) {
	f := newFx(t)
	// Өгөгдлийг superuser-ээр бэлдэнэ.
	plain := mountOn(t, "/rooms", f.roomResource())
	pA := f.person(f.orgA, "Бат", 1)
	f.person(f.orgB, "Дорж", 1)
	idA := do(t, plain, "POST", "/rooms", f.orgA, 1, map[string]any{"name": "a", "owner_id": pA, "price": 12.5}).str("id")
	do(t, plain, "POST", "/rooms", f.orgB, 1, map[string]any{"name": "b"})

	rdb := rlsDB(t, f)
	r := f.roomResource()
	r.RLS, r.DB = true, func() *gorm.DB { return rdb }
	app := mountOn(t, "/rooms", r)

	l := do(t, app, "GET", "/rooms", f.orgA, 2, nil)
	mustStatus(t, l, 200)
	if rows := l.list("data"); len(rows) != 1 || rows[0]["id"] != idA || l.JSON["total"] != float64(1) {
		t.Fatalf("RLS жагсаалт: %s", l.Raw)
	}
	if !strings.Contains(string(l.Raw), `"price":12.50`) {
		t.Errorf("RLS жагсаалтын decimal overlay: %s", l.Raw)
	}
	mustStatus(t, do(t, app, "GET", "/rooms/"+idA, f.orgA, 2, nil), 200)
	mustStatus(t, do(t, app, "GET", "/rooms/"+idA, f.orgB, 2, nil), 404)

	o := do(t, app, "GET", "/rooms/options/owner_id", f.orgA, 2, nil)
	mustStatus(t, o, 200)
	if rows := o.list("data"); len(rows) != 1 || rows[0]["label"] != "Бат" {
		t.Fatalf("RLS options: %s", o.Raw)
	}

	req := httptest.NewRequest("GET", "/rooms/export.xlsx", nil)
	req.Header.Set("X-T-Org", f.orgA)
	req.Header.Set("X-T-Role", "2")
	res, err := app.Test(req, fiber.TestConfig{Timeout: 0})
	if err != nil || res.StatusCode != 200 || res.Header.Get("X-Kv-Export-Rows") != "1" {
		t.Fatalf("RLS export: %v status=%v rows=%q", err, res.StatusCode, res.Header.Get("X-Kv-Export-Rows"))
	}
}
