package access

import (
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/khan-vision/khan/crud"
	"github.com/khan-vision/khan/validate"
	"gorm.io/gorm"
)

// crud.Resource + CrudCan + CrudForbidden: БОДИТ татгалзалт дээр л 403 (module/capability) ба
// deny hook нэг удаа. 403 нь DB-д хүрэхээс өмнө тул DB nil байж болно.
func TestCrudForbiddenEndToEnd(t *testing.T) {
	rec := &denialRec{}
	p, _ := httpPolicy(t, WithDenyHook(rec.hook))
	caps := ActionCaps{
		Read: []string{"invoices.view"}, Create: []string{"invoices.create"},
		Update: []string{"invoices.edit"}, Delete: []string{"invoices.delete"},
	}
	res := &crud.Resource[map[string]any]{
		Name: "finance.invoice", Label: "Нэхэмжлэх", Table: "finance.invoice",
		Fields:      []crud.Field{{Name: "number", Label: "Дугаар", Kind: validate.String}},
		Can:         p.CrudCan("finance", caps),
		OnForbidden: p.CrudForbidden("finance", caps),
		DB:          func() *gorm.DB { return nil },
	}
	app := appWith(claims("viewer", orgA), func(app *fiber.App) {
		if err := res.Mount(app.Group("/inv")); err != nil {
			t.Fatal(err)
		}
	})

	status, body := do(t, app, "POST", "/inv", nil)
	if status != 403 || !strings.Contains(body, `"module":"finance"`) || !strings.Contains(body, `"capability":"invoices.create"`) {
		t.Fatalf("create: %d %s", status, body)
	}
	if d := rec.all(); len(d) != 1 || d[0].Module != "finance" || d[0].Caps[0] != "invoices.create" || d[0].Reason != DenialNoGrant {
		t.Fatalf("hook: %+v", d)
	}
	status, body = do(t, app, "DELETE", "/inv/11111111-1111-1111-1111-111111111111", nil)
	if status != 403 || !strings.Contains(body, `"capability":"invoices.delete"`) || len(rec.all()) != 2 {
		t.Fatalf("delete: %d %s hooks=%d", status, body, len(rec.all()))
	}

	// Нэвтрээгүй хүсэлтийг crud өөрөө 401-ээр зогсооно (OnForbidden-д хүрэхгүй).
	anon := appWith(nil, func(app *fiber.App) {
		if err := res.Mount(app.Group("/inv")); err != nil {
			t.Fatal(err)
		}
	})
	if status, _ := do(t, anon, "POST", "/inv", nil); status != 401 {
		t.Fatalf("anon: %d", status)
	}
}

// Тодорхойгүй (хоосон cap-тай) үйлдэл → 403, hook-д no-grant.
func TestCrudForbiddenEmptyCaps(t *testing.T) {
	rec := &denialRec{}
	p, _ := httpPolicy(t, WithDenyHook(rec.hook))
	h := p.CrudForbidden("finance", ActionCaps{Read: []string{"invoices.view"}})
	app := appWith(claims("viewer", orgA), func(app *fiber.App) {
		app.Post("/x", func(c fiber.Ctx) error { return h(c, crud.ActionCreate) })
	})
	if status, _ := do(t, app, "POST", "/x", nil); status != 403 || len(rec.all()) != 1 || rec.all()[0].Reason != DenialNoGrant {
		t.Fatalf("status=%d hooks=%+v", status, rec.all())
	}
}
