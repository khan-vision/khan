// resource.go — нэг жагсаалтын resource-ийн list / query / export / bulk
// endpoint-уудыг НЭГ дор холбох helper (KvGrid v2 + KvCrudPage).
//
//	query.Resource{
//	    Spec:     spec,
//	    NewQuery: func(orgID string) *gorm.DB { return DB.DB.Table("x r").Where("r.org_id = ?", orgID) },
//	    Export:   &query.ExportSpec{...},
//	    Bulk:     &query.BulkSpec{...},
//	    DB:       func() *gorm.DB { return DB.DB },
//	    Org:      orgctx.Require,
//	    CanWrite: canWrite,
//	}.Mount(router)
//
// org_id ҮРГЭЛЖ JWT-ээс (Org → анхдагч tenant.Require). X-Org-ID header /
// ?org_id query-г хэзээ ч уншихгүй.
package query

import (
	"github.com/khan-vision/khan/tenant"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
)

// Resource — list/query/export/bulk endpoint-уудын тохиргоо.
type Resource struct {
	Spec Spec
	// NewQuery(orgID) — orgID ҮРГЭЛЖ JWT-ээс (orgctx.Require). Caller нь Where("<alias>.org_id = ?", orgID) заавал хийнэ.
	NewQuery func(orgID string) *gorm.DB
	Export   *ExportSpec // nil → /export.xlsx 404
	Bulk     *BulkSpec   // nil → /bulk 404
	// DB — bulk exec-ийн DB. query нь глобал DB ашигладаггүй (DB-гүй тест)
	// тул Bulk өгсөн бол caller `func() *gorm.DB { return DB.DB }` заавал өгнө.
	DB       func() *gorm.DB
	Can      func(c *fiber.Ctx) map[string]bool // optional → list response gets "can"
	CanWrite func(c *fiber.Ctx) bool            // bulk gate; nil → deny (403)
	NewRows  func() interface{}                 // &[]Row{}; nil → *[]map[string]interface{}
	// Org — JWT-ээс org_id унших (401-г өөрөө бичнэ). nil → tenant.Require
	// (orgctx.Require-тэй ижил дүрэм). Production-д orgctx.Require өгөхийг зөвлөнө.
	Org func(c *fiber.Ctx) (string, bool)
}

func (r Resource) org(c *fiber.Ctx) (string, bool) {
	if r.Org != nil {
		return r.Org(c)
	}
	return tenant.Require(c)
}

func (r Resource) queryFor(orgID string) func() *gorm.DB {
	return func() *gorm.DB { return r.NewQuery(orgID) }
}

// List — GET (query string) ба POST /query (JSON body).
func (r Resource) List(c *fiber.Ctx) error {
	orgID, ok := r.org(c)
	if !ok {
		return nil
	}
	req, err := RequestFrom(c)
	if err != nil {
		return Fail(c, err)
	}
	var dest interface{}
	if r.NewRows != nil {
		dest = r.NewRows()
	} else {
		dest = &[]map[string]interface{}{}
	}
	resp, err := RunRequest(req, r.queryFor(orgID), dest, r.Spec)
	if err != nil {
		return Fail(c, err)
	}
	if r.Can != nil {
		resp["can"] = r.Can(c)
	}
	return c.JSON(resp)
}

// ExportXLSX — GET /export.xlsx (list-тэй ижил query параметр).
func (r Resource) ExportXLSX(c *fiber.Ctx) error {
	if r.Export == nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "Олдсонгүй"})
	}
	orgID, ok := r.org(c)
	if !ok {
		return nil
	}
	req, err := RequestFrom(c)
	if err != nil {
		return Fail(c, err)
	}
	return ExportXLSX(c, req, r.queryFor(orgID), r.Spec, *r.Export)
}

// BulkHandler — POST /bulk → {"affected": n}.
func (r Resource) BulkHandler(c *fiber.Ctx) error {
	if r.Bulk == nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "Олдсонгүй"})
	}
	orgID, ok := r.org(c)
	if !ok {
		return nil
	}
	if r.CanWrite == nil || !r.CanWrite(c) {
		return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "Хандах эрхгүй байна"})
	}
	if r.DB == nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "bulk DB тохируулаагүй"})
	}
	req, err := ParseBulk(c.Body(), *r.Bulk)
	if err != nil {
		return Fail(c, err)
	}
	n, err := ExecBulk(r.DB(), orgID, req, *r.Bulk)
	if err != nil {
		return Fail(c, err)
	}
	return c.JSON(fiber.Map{"affected": n})
}

// Mount — GET "/", POST "/query", GET "/export.xlsx", POST "/bulk".
// `/:id` зэрэг параметртэй route-оос ӨМНӨ дуудна. Spec-ийн алдаа энд panic болно.
func (r Resource) Mount(g fiber.Router) {
	if r.NewQuery == nil {
		panic("khan/query: Resource.NewQuery хоосон")
	}
	if r.Export != nil {
		r.Export.Validate()
	}
	if r.Bulk != nil {
		r.Bulk.Validate()
		if r.DB == nil {
			panic("khan/query: Resource.Bulk өгсөн бол Resource.DB заавал")
		}
	}
	g.Get("/", r.List)
	g.Post("/query", r.List)
	g.Get("/export.xlsx", r.ExportXLSX)
	g.Post("/bulk", r.BulkHandler)
}
