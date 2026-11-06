package codegen

import (
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/khan-vision/khan/crud"
	"github.com/khan-vision/khan/lookup"
	"github.com/khan-vision/khan/validate"
)

// Жинхэнэ crud.Resource.Meta() (DB/Can-гүйгээр) codegen-д шууд тохирно.
func TestFromRealResourceMeta(t *testing.T) {
	r := &crud.Resource[map[string]any]{
		Name: "test.room", Label: "Өрөө", Endpoint: "/rooms", Table: "test.room",
		Fields: []crud.Field{
			{Name: "name", Label: "Нэр", Kind: validate.String, Rules: validate.Rules{Required: true, MaxLen: 100}},
			{Name: "price", Label: "Үнэ", Kind: validate.Money, Rules: validate.Rules{Nullable: true, Min: validate.Bound(0)}},
			{Name: "owner_id", Label: "Эзэн", Kind: validate.UUID, Rules: validate.Rules{Nullable: true},
				Ref: &lookup.Spec{Table: "test.person", Label: []string{"name"}}},
		},
		Version: "version",
	}
	m := r.Meta()
	if len(m.Fields) != 3 {
		t.Fatalf("Meta: %+v", m)
	}
	ts, err := TS([]crud.Meta{m}, TSOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(ts), `khanOptions("/rooms/options/owner_id", query)`) || !strings.Contains(string(ts), "export interface RoomPayload") {
		t.Errorf("TS:\n%s", ts)
	}
	if _, err := OpenAPI([]crud.Meta{m}, Info{}); err != nil {
		t.Fatal(err)
	}
}

// Child-ийн id багана (payload-ийн түлхүүр) болон ExposeMeta нь бодит Resource-оос codegen-д хүрнэ.
func TestChildIDColumnAndExposeMeta(t *testing.T) {
	r := &crud.Resource[map[string]any]{
		Name: "test.invoice", Label: "Нэхэмжлэх", Endpoint: "/invoices", Table: "test.invoice", ExposeMeta: true,
		Fields: []crud.Field{{Name: "number", Label: "Дугаар", Kind: validate.String, Rules: validate.Rules{Required: true}}},
		Children: []crud.Child{{Name: "lines", Label: "Мөр", Table: "test.invoice_line", FK: "invoice_id",
			ID:     crud.IDSpec{Column: "line_id"},
			Fields: []crud.Field{{Name: "qty", Label: "Тоо", Kind: validate.Int, Rules: validate.Rules{Required: true}}}}},
	}
	m := r.Meta()
	if len(m.Children) != 1 || m.Children[0].ID.Column != "line_id" || !m.ExposeMeta {
		t.Fatalf("Meta: %+v", m)
	}
	ts, err := TS([]crud.Meta{m}, TSOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(ts), "line_id?: string") || !strings.Contains(string(ts), "line_id: z.string()") {
		t.Errorf("child id түлхүүр line_id биш:\n%s", ts)
	}
	oa, err := OpenAPI([]crud.Meta{m}, Info{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(oa), `"/invoices/meta"`) || !strings.Contains(string(oa), `"line_id"`) {
		t.Errorf("OpenAPI: ExposeMeta /meta эсвэл line_id алга")
	}
	m.ExposeMeta = false
	oa, _ = OpenAPI([]crud.Meta{m}, Info{})
	if strings.Contains(string(oa), `"/invoices/meta"`) {
		t.Error("ExposeMeta=false үед /meta бичигдэв")
	}
}

// Scope тайлбар: tenant → байхгүй; shared/global → TS comment + OpenAPI description, /system path.
func TestScopeDocs(t *testing.T) {
	mk := func(scope crud.Scope, sysCan bool) crud.Meta {
		r := &crud.Resource[map[string]any]{
			Name: "test.thing", Label: "Зүйл", Endpoint: "/things", Table: "test.thing", Scope: scope,
			Fields: []crud.Field{{Name: "name", Label: "Нэр", Kind: validate.String, Rules: validate.Rules{Required: true}}},
		}
		if sysCan {
			r.SystemCan = func(fiber.Ctx, crud.Action) bool { return false }
		}
		return r.Meta()
	}
	tenant, shared, sharedRO, global := mk(crud.ScopeTenant, false), mk(crud.ScopeShared, true), mk(crud.ScopeShared, false), mk(crud.ScopeGlobal, false)
	ts := func(m crud.Meta) string {
		b, err := TS([]crud.Meta{m}, TSOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	oa := func(m crud.Meta) string {
		b, err := OpenAPI([]crud.Meta{m}, Info{})
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	if strings.Contains(ts(tenant), "Scope") || strings.Contains(oa(tenant), "x-khan-scope") {
		t.Error("tenant resource-д scope тайлбар орсон")
	}
	if s := ts(shared); !strings.Contains(s, "thingScope = \"shared\"") || !strings.Contains(s, "/things/system") {
		t.Errorf("shared TS:\n%s", s)
	}
	if s := oa(shared); !strings.Contains(s, `"/things/system"`) || !strings.Contains(s, `"x-khan-scope": "shared"`) {
		t.Errorf("shared OpenAPI:\n%s", s)
	}
	if s := ts(sharedRO); !strings.Contains(s, "боломжгүй (403)") || strings.Contains(oa(sharedRO), "/things/system") {
		t.Errorf("shared read-only:\n%s", s)
	}
	if s := ts(global); !strings.Contains(s, "Унших-л") || !strings.Contains(oa(global), `"x-khan-scope": "global"`) {
		t.Errorf("global:\n%s", s)
	}
}
