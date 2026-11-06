package crud

import (
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/khan-vision/khan/validate"
	"gorm.io/gorm"
)

// OnForbidden нь бодит татгалзалт бүрд (list/get/create/update/delete) ажиллаж, үйлдлийг
// зөв дамжуулна; `can` map тооцоолоход дуудагдахгүй. Тохируулаагүй бол анхдагч 403.
func TestOnForbidden(t *testing.T) {
	var got []Action
	r := &Resource[map[string]any]{
		Name: "t.x", Label: "X", Table: "t.x",
		Fields: []Field{{Name: "name", Kind: validate.String}},
		Can:    func(fiber.Ctx, Action) bool { return false },
		OnForbidden: func(c fiber.Ctx, a Action) error {
			got = append(got, a)
			return c.Status(fiber.StatusTeapot).JSON(fiber.Map{"error": "no", "action": string(a)})
		},
		DB: func() *gorm.DB { return nil },
	}
	app := mountOn(t, "/x", r)
	id := "11111111-1111-1111-1111-111111111111"
	for _, tc := range []struct {
		method, path string
		want         Action
	}{
		{"GET", "/x", ActionRead}, {"GET", "/x/" + id, ActionRead}, {"POST", "/x", ActionCreate},
		{"PUT", "/x/" + id, ActionUpdate}, {"DELETE", "/x/" + id, ActionDelete},
	} {
		got = nil
		res := do(t, app, tc.method, tc.path, newUUID(), 1, map[string]any{"name": "a"})
		if res.Status != fiber.StatusTeapot || len(got) != 1 || got[0] != tc.want {
			t.Errorf("%s %s: status=%d actions=%v", tc.method, tc.path, res.Status, got)
		}
	}

	r2 := *r
	r2.OnForbidden = nil
	app2 := mountOn(t, "/x", &r2)
	if res := do(t, app2, "POST", "/x", newUUID(), 1, map[string]any{"name": "a"}); res.Status != 403 {
		t.Fatalf("анхдагч 403: %d", res.Status)
	}
}
