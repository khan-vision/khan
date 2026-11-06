package lookup

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/khan-vision/khan/tenant"
	"github.com/khan-vision/khan/testkit"
	"github.com/khan-vision/khan/validate"
	"gorm.io/gorm"
)

const (
	orgB  = "22222222-2222-2222-2222-222222222222"
	uBat  = "aaaaaaaa-0000-0000-0000-000000000001"
	uBold = "aaaaaaaa-0000-0000-0000-000000000002"
	uOth  = "aaaaaaaa-0000-0000-0000-000000000003" // org B
	uDel  = "aaaaaaaa-0000-0000-0000-000000000004" // soft-deleted
	uOff  = "aaaaaaaa-0000-0000-0000-000000000005" // status inactive
)

// lookupFixture — түр schema + хүснэгт; тест дуусахад schema-г устгана.
func lookupFixture(t *testing.T) (*gorm.DB, string) {
	t.Helper()
	db := testkit.DB(t)
	schema := fmt.Sprintf("khan_lk_%d", time.Now().UnixNano()%1000000000)
	must := func(sql string, args ...any) {
		t.Helper()
		if err := db.Exec(sql, args...).Error; err != nil {
			t.Fatal(err)
		}
	}
	must("DROP SCHEMA IF EXISTS " + schema + " CASCADE")
	must("CREATE SCHEMA " + schema)
	t.Cleanup(func() { db.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE") })
	must(`CREATE TABLE ` + schema + `.emp (
		id uuid PRIMARY KEY, org_id uuid NOT NULL, last_name text, first_name text,
		status text NOT NULL DEFAULT 'active', dept int, deleted_at timestamptz)`)
	must(`CREATE TABLE ` + schema + `.num (id bigint PRIMARY KEY, org_id uuid NOT NULL, name text)`)
	ins := func(id, org, last, first, status string, dept any, del any) {
		must(`INSERT INTO `+schema+`.emp (id, org_id, last_name, first_name, status, dept, deleted_at) VALUES (?,?,?,?,?,?,?)`,
			id, org, last, first, status, dept, del)
	}
	ins(uBat, orgA, "Бат", "Эрдэнэ", "active", 1, nil)
	ins(uBold, orgA, "Болд", "Сүх", "active", 2, nil)
	ins(uOth, orgB, "Бат", "Бусад", "active", 1, nil)
	ins(uDel, orgA, "Батаа", "Устсан", "active", 1, "2026-01-01")
	ins(uOff, orgA, "Баяр", "Идэвхгүй", "inactive", 1, nil)
	must(`INSERT INTO ` + schema + `.num VALUES (1, '` + orgA + `', 'one'), (2, '` + orgA + `', 'two'), (3, '` + orgB + `', 'three')`)
	return db, schema
}

func orgFn(org string) func(fiber.Ctx) (string, bool) {
	return func(c fiber.Ctx) (string, bool) {
		if org == "" {
			_ = c.SendStatus(401)
			return "", false
		}
		return org, true
	}
}

type optResp struct {
	Data []struct {
		Value       any    `json:"value"`
		Label       string `json:"label"`
		ParentValue any    `json:"parent_value"`
	} `json:"data"`
	Error string `json:"error"`
}

func call(t *testing.T, h fiber.Handler, qs string) (int, optResp, string) {
	t.Helper()
	app := fiber.New()
	app.Get("/options", h)
	resp, err := app.Test(httptest.NewRequest("GET", "/options"+qs, nil), fiber.TestConfig{Timeout: 0})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	var out optResp
	_ = json.Unmarshal(b, &out)
	return resp.StatusCode, out, string(b)
}

func empDB(db *gorm.DB, schema string) Spec {
	return Spec{Table: schema + ".emp", Label: []string{"last_name", "first_name"}, SoftDelete: true,
		Where: "status = 'active'", Parent: "dept", Kind: validate.UUID}
}

func TestHandlerDB(t *testing.T) {
	db, schema := lookupFixture(t)
	spec := empDB(db, schema)
	h := spec.Handler(func() *gorm.DB { return db }, orgFn(orgA))

	t.Run("жагсаалт: org, soft delete, Where-ээр шүүгдэнэ", func(t *testing.T) {
		code, out, body := call(t, h, "")
		if code != 200 || len(out.Data) != 2 {
			t.Fatalf("%d %s", code, body)
		}
		if out.Data[0].Label != "Бат Эрдэнэ" || out.Data[0].Value != uBat || out.Data[1].Label != "Болд Сүх" {
			t.Fatalf("%s", body)
		}
		if out.Data[0].ParentValue != float64(1) {
			t.Fatalf("parent_value: %s", body)
		}
	})
	t.Run("search", func(t *testing.T) {
		_, out, body := call(t, h, "?search="+url.QueryEscape("бат"))
		if len(out.Data) != 1 || out.Data[0].Value != uBat {
			t.Fatalf("%s", body)
		}
		_, out, _ = call(t, h, "?search="+url.QueryEscape("Бат Эрд")) // овог+нэр нэг мөрөөр
		if len(out.Data) != 1 {
			t.Fatalf("full-name search: %+v", out)
		}
	})
	t.Run("search wildcard escape", func(t *testing.T) {
		for _, s := range []string{"%", "_", `\`} {
			_, out, body := call(t, h, "?search="+url.QueryEscape(s))
			if len(out.Data) != 0 {
				t.Fatalf("%q wildcard биш литерал байх ёстой: %s", s, body)
			}
		}
	})
	t.Run("parent", func(t *testing.T) {
		_, out, body := call(t, h, "?parent=2")
		if len(out.Data) != 1 || out.Data[0].Value != uBold {
			t.Fatalf("%s", body)
		}
	})
	t.Run("ids: өөр org, устсан, буруу id гарахгүй", func(t *testing.T) {
		ids := strings.Join([]string{uBold, uOth, uDel, "not-a-uuid", uBat, uBat}, ",")
		code, out, body := call(t, h, "?ids="+ids)
		if code != 200 || len(out.Data) != 2 {
			t.Fatalf("%d %s", code, body)
		}
		_, out, _ = call(t, h, "?ids="+strings.ToUpper(uBat)) // том үсэг → жижиг
		if len(out.Data) != 1 {
			t.Fatalf("uppercase id: %+v", out)
		}
		_, out, body = call(t, h, "?ids=zzz")
		if len(out.Data) != 0 || !strings.Contains(body, `"data":[]`) {
			t.Fatalf("data:[] байх ёстой: %s", body)
		}
	})
	t.Run("ids > 200 → 400", func(t *testing.T) {
		many := make([]string, 201)
		for i := range many {
			many[i] = fmt.Sprintf("aaaaaaaa-0000-0000-0000-%012d", i)
		}
		code, _, body := call(t, h, "?ids="+strings.Join(many, ","))
		if code != 400 {
			t.Fatalf("%d %s", code, body)
		}
	})
	t.Run("limit clamp", func(t *testing.T) {
		_, out, _ := call(t, h, "?limit=1")
		if len(out.Data) != 1 {
			t.Fatalf("limit=1: %+v", out)
		}
		_, out, _ = call(t, h, "?limit=99999")
		if len(out.Data) != 2 {
			t.Fatalf("limit clamp: %+v", out)
		}
	})
	t.Run("parent дэмжигдээгүй spec → 400", func(t *testing.T) {
		s := spec
		s.Parent = ""
		code, _, _ := call(t, s.Handler(func() *gorm.DB { return db }, orgFn(orgA)), "?parent=1")
		if code != 400 {
			t.Fatalf("%d", code)
		}
	})
	t.Run("org B өөрийн мөрийг л харна", func(t *testing.T) {
		hb := spec.Handler(func() *gorm.DB { return db }, orgFn(orgB))
		_, out, body := call(t, hb, "")
		if len(out.Data) != 1 || out.Data[0].Value != uOth {
			t.Fatalf("%s", body)
		}
		_, out, _ = call(t, hb, "?ids="+uBat)
		if len(out.Data) != 0 {
			t.Fatal("B нь A-ийн id-г ids-ээр авч болохгүй")
		}
	})
	t.Run("401 org-гүй", func(t *testing.T) {
		code, _, _ := call(t, spec.Handler(func() *gorm.DB { return db }, orgFn("")), "")
		if code != 401 {
			t.Fatalf("%d", code)
		}
	})
	t.Run("буруу spec → 500 generic", func(t *testing.T) {
		code, _, body := call(t, Spec{Table: "bad;"}.Handler(func() *gorm.DB { return db }, orgFn(orgA)), "")
		if code != 500 || strings.Contains(body, "Table") || !strings.Contains(body, "Дотоод алдаа") {
			t.Fatalf("%d %s", code, body)
		}
	})
	t.Run("DB алдаа → дотоод мэдээлэл алдагдахгүй", func(t *testing.T) {
		s := Spec{Table: schema + ".no_such_table", Label: []string{"name"}}
		code, _, body := call(t, s.Handler(func() *gorm.DB { return db }, orgFn(orgA)), "")
		if code != 500 || strings.Contains(body, "no_such_table") || strings.Contains(strings.ToLower(body), "relation") {
			t.Fatalf("%d %s", code, body)
		}
	})
}

func TestHandlerIntKindAndTenantOff(t *testing.T) {
	db, schema := lookupFixture(t)
	spec := Spec{Table: schema + ".num", Label: []string{"name"}, Kind: validate.Int}
	h := spec.Handler(func() *gorm.DB { return db }, orgFn(orgA))
	_, out, body := call(t, h, "?ids=1,2,3,abc")
	if len(out.Data) != 2 || out.Data[0].Value != float64(1) {
		t.Fatalf("int ids (3 нь org B): %s", body)
	}
	spec.Tenant = TenantOff()
	_, out, _ = call(t, spec.Handler(func() *gorm.DB { return db }, orgFn(orgA)), "")
	if len(out.Data) != 3 {
		t.Fatalf("Tenant:false бүх мөр: %+v", out)
	}
}

func TestExistsDB(t *testing.T) {
	db, schema := lookupFixture(t)
	spec := empDB(db, schema)
	spec.Where = "" // Exists-д Where-ийг тусад нь шалгана

	miss, err := spec.Exists(db, orgA, []any{uBat, uBold, uBat})
	if err != nil || len(miss) != 0 {
		t.Fatalf("олдсон: %v %v", miss, err)
	}
	miss, err = spec.Exists(db, orgA, []any{uBat, uOth, uDel, "junk", nil, uOth})
	if err != nil {
		t.Fatal(err)
	}
	// өөр org, устсан, буруу формат → дутуу; давхардал нэг удаа; nil алгасна
	if fmt.Sprint(miss) != fmt.Sprint([]any{uOth, uDel, "junk"}) {
		t.Fatalf("missing=%v", miss)
	}
	if m, err := spec.Exists(db, orgA, nil); err != nil || m != nil {
		t.Fatalf("хоосон: %v %v", m, err)
	}
	if _, err := spec.Exists(db, "", []any{uBat}); err == nil {
		t.Fatal("org хоосон бол алдаа (fail-closed)")
	}
	if _, err := spec.Exists(nil, orgA, []any{uBat}); err == nil {
		t.Fatal("tx nil")
	}
	if _, err := (Spec{Table: "x;"}).Exists(db, orgA, []any{"a"}); err == nil {
		t.Fatal("буруу spec")
	}
	// Where нөхцөл Exists-д хүчинтэй
	spec.Where = "status = 'active'"
	miss, _ = spec.Exists(db, orgA, []any{uOff, uBat})
	if len(miss) != 1 || miss[0] != uOff {
		t.Fatalf("Where: %v", miss)
	}
	// Tenant:false
	spec.Tenant = TenantOff()
	miss, _ = spec.Exists(db, "", []any{uOth})
	if len(miss) != 0 {
		t.Fatalf("Tenant:false: %v", miss)
	}
}

func TestExistsInt(t *testing.T) {
	db, schema := lookupFixture(t)
	spec := Spec{Table: schema + ".num", Label: []string{"name"}, Kind: validate.Int}
	miss, err := spec.Exists(db, orgA, []any{int64(1), "2", 3, "x", 99})
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(miss) != fmt.Sprint([]any{3, "x", 99}) {
		t.Fatalf("missing=%v", miss)
	}
}

func TestExistsChunks(t *testing.T) {
	db, schema := lookupFixture(t)
	spec := Spec{Table: schema + ".num", Label: []string{"name"}, Kind: validate.Int}
	vals := make([]any, 0, 2500)
	for i := 1; i <= 2500; i++ {
		vals = append(vals, int64(i))
	}
	miss, err := spec.Exists(db, orgA, vals)
	if err != nil {
		t.Fatal(err)
	}
	if len(miss) != 2498 { // 1, 2 л байгаа
		t.Fatalf("missing=%d", len(miss))
	}
}

func TestTenantRequireIntegration(t *testing.T) {
	db, schema := lookupFixture(t)
	spec := empDB(db, schema)
	h := spec.Handler(func() *gorm.DB { return db }, tenant.Require)
	app := fiber.New()
	app.Get("/options", testkit.WithClaims(testkit.Claims("u1", orgA, 1)), h)
	app.Get("/anon", h)
	resp, _ := app.Test(httptest.NewRequest("GET", "/options", nil), fiber.TestConfig{Timeout: 0})
	if resp.StatusCode != 200 {
		t.Fatalf("%d", resp.StatusCode)
	}
	resp, _ = app.Test(httptest.NewRequest("GET", "/anon?org_id="+orgA, nil), fiber.TestConfig{Timeout: 0})
	if resp.StatusCode != 401 {
		t.Fatalf("?org_id-д итгэж болохгүй: %d", resp.StatusCode)
	}
}
