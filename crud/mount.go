package crud

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/gofiber/fiber/v3"
	"github.com/khan-vision/khan/audit"
	"github.com/khan-vision/khan/obs"
	"github.com/khan-vision/khan/query"
	"github.com/khan-vision/khan/tenant"
	"gorm.io/gorm"
)

// Mount — route-уудыг router дээр бүртгэнэ. Тохиргоо буруу бол error (panic биш);
// хүсэлтийн үед panic хийхгүй. Статик route-ууд `/:id`-ээс ӨМНӨ бүртгэгдэнэ.
//
// Route-ууд: GET / , POST /query , GET /export.xlsx (Export), POST /bulk (Bulk),
// GET /meta (ExposeMeta), GET /options/:field, GET|PUT|DELETE /:id, POST /.
func (r *Resource[T]) Mount(router fiber.Router) error {
	if router == nil {
		return errors.New("khan/crud: router nil")
	}
	e, err := r.build(true)
	if err != nil {
		return err
	}
	e.mount(router)
	return nil
}

func (e *engine) mount(router fiber.Router) {
	t := e.withTimeout
	router.Get("/", t(e.listHandler))
	router.Post("/query", t(e.listHandler))
	if e.export != nil {
		router.Get("/export.xlsx", t(e.exportHandler))
	}
	if e.bulk != nil {
		router.Post("/bulk", t(e.bulkHandler))
	}
	if e.exposeMeta {
		router.Get("/meta", t(e.metaHandler))
	}
	if len(e.refs) > 0 {
		router.Get("/options/:field", t(e.optionsHandler()))
	}
	if e.audit != nil {
		router.Get("/history", t(e.audit.handlers.List))
		router.Get("/:id/history", t(e.audit.handlers.Record))
	}
	router.Get("/:id", t(e.get))
	router.Post("/", t(e.create))
	router.Put("/:id", t(e.update))
	router.Delete("/:id", t(e.remove))
}

// withTimeout — handler-ийн context-д deadline тавина (удаан ILIKE/тайлан DB-г
// хязгааргүй эзлэхгүй). Хугацаа хэтэрвэл query цуцлагдаж generic 500 болно.
func (e *engine) withTimeout(h fiber.Handler) fiber.Handler {
	if e.timeout < 0 {
		return h
	}
	return func(c fiber.Ctx) error {
		parent := c.Context()
		ctx, cancel := context.WithTimeout(parent, e.timeout)
		defer cancel()
		c.SetContext(ctx)
		defer c.SetContext(parent)
		return h(c)
	}
}

// canMap — list/meta хариуны `can` ({read,create,update,delete}).
func (e *engine) canMap(c fiber.Ctx) map[string]bool {
	return map[string]bool{
		"read":   e.allowed(c, ActionRead),
		"create": e.allowed(c, ActionCreate),
		"update": e.allowed(c, ActionUpdate),
		"delete": e.allowed(c, ActionDelete),
	}
}

// gate — org (401) + Can (403) шалгана; ok=false бол хариу аль хэдийн бичигдсэн.
func (e *engine) gate(c fiber.Ctx, a Action) (orgID string, gdb *gorm.DB, ok bool) {
	orgID, ok = e.org(c)
	if !ok {
		return "", nil, false
	}
	if !e.allowed(c, a) {
		_ = e.forbidden(c, a)
		return "", nil, false
	}
	gdb = e.db()
	if gdb == nil {
		obs.With(c).Error("crud: DB nil", "resource", e.name)
		_ = jsonErr(c, fiber.StatusInternalServerError, msgInternal)
		return "", nil, false
	}
	return orgID, gdb.WithContext(c.Context()), true
}

// queryFail — query package-ийн алдаа: шүүлтийн алдаа → 400 (мессеж аюулгүй), бусад → generic 500.
func (e *engine) queryFail(c fiber.Ctx, a Action, err error) error {
	if query.IsFilterError(err) {
		return jsonErr(c, fiber.StatusBadRequest, err.Error())
	}
	return e.fail(c, a, err)
}

func (e *engine) listHandler(c fiber.Ctx) error {
	orgID, _, ok := e.gate(c, ActionRead)
	if !ok {
		return nil
	}
	req, err := query.RequestFrom(c)
	if err != nil {
		return e.queryFail(c, ActionRead, err)
	}
	// load — жагсаалтын хариу (can-гүй). RLS үед count/totals/data бүгд НЭГ tenant.Tx дотор (app.org_id тавигдана).
	load := func(ctx context.Context) (fiber.Map, error) {
		rows := e.newRows()
		var resp fiber.Map
		err := e.read(ctx, orgID, func(db *gorm.DB) error {
			var err error
			if resp, err = query.RunRequest(req, func() *gorm.DB { return e.listQuery(db, orgID) }, rows, e.listSpec); err != nil {
				return err
			}
			return e.overlayListDecimals(db, orgID, rows)
		})
		return resp, err
	}
	if parts, cacheable := e.listParts(req); e.cacheReads() && cacheable {
		can := e.canMap(c)
		err := e.serveCached(c, orgID, parts, can, func(ctx context.Context) ([]byte, error) {
			resp, err := load(ctx)
			if err != nil {
				return nil, err
			}
			return c.App().Config().JSONEncoder(resp)
		}, e.withCan(c, can))
		if err != nil {
			return e.queryFail(c, ActionRead, err)
		}
		return nil
	}
	resp, err := load(c.Context())
	if err != nil {
		return e.queryFail(c, ActionRead, err)
	}
	resp["can"] = e.canMap(c)
	return c.JSON(resp)
}

func (e *engine) exportHandler(c fiber.Ctx) error {
	orgID, _, ok := e.gate(c, ActionRead)
	if !ok {
		return nil
	}
	req, err := query.RequestFrom(c)
	if err != nil {
		return e.queryFail(c, ActionRead, err)
	}
	err = e.read(c.Context(), orgID, func(db *gorm.DB) error {
		return query.ExportXLSX(c, req, func() *gorm.DB { return e.listQuery(db, orgID) }, e.listSpec, *e.export)
	})
	// query.ExportXLSX өөрөө алдааг err.Error()-ээр бичдэг — 500 бол дотоод мэдээллийг
	// хариунаас авч, лог руу шилжүүлээд ерөнхий мессеж тавина.
	if err == nil && c.Response().StatusCode() == fiber.StatusInternalServerError {
		obs.With(c).Error("crud: export алдаа", "resource", e.name, "body", string(c.Response().Body()))
		c.Response().ResetBody()
		return jsonErr(c, fiber.StatusInternalServerError, msgInternal)
	}
	return err
}

// bulkHandler — POST /bulk. Эрх нь үйлдлээр: delete → Can(delete), update (болон бусад) → Can(update).
func (e *engine) bulkHandler(c fiber.Ctx) error {
	var peek struct {
		Action string `json:"action"`
	}
	_ = json.NewDecoder(bytes.NewReader(c.Body())).Decode(&peek)
	a := ActionUpdate
	if strings.TrimSpace(peek.Action) == "delete" {
		a = ActionDelete
	}
	orgID, _, ok := e.gate(c, a)
	if !ok {
		return nil
	}
	req, err := query.ParseBulk(c.Body(), *e.bulk)
	if err != nil {
		return e.queryFail(c, a, err)
	}
	var n int64
	err = e.tx(c, orgID, func(tx *gorm.DB) (err error) {
		// Нөлөөлөх мөрүүдийг (тухайн org-ийнх л) түгжиж id-г нь авна — version/updated_at
		// болон event-ийг ЗӨВХӨН бодитоор өөрчлөгдсөн мөрөнд.
		ids, err := e.lockBulkIDs(tx, orgID, req.IDs)
		if err != nil || len(ids) == 0 {
			return err
		}
		// Hook/children-тэй resource-ийн bulk delete = мөр бүрт DELETE /:id-ийн ЯГ ижил
		// дүрэм (BeforeDelete-ээр «батлагдсан баримт устгахгүй» гэх мэтийг тойрохгүй).
		if req.Action == "delete" && e.bulkDeleteNeedsRows() {
			uid := userID(c)
			for _, id := range ids {
				if err := e.deleteOne(c, tx, orgID, uid, id, fmt.Sprint(id), true); err != nil {
					return err
				}
				n++
			}
			return nil
		}
		// ExecBulk-д зөвхөн түгжигдсэн (тухайн org-ийн, бодитоор байгаа) id-г өгнө.
		req.IDs = make([]string, len(ids))
		for i, id := range ids {
			req.IDs[i] = fmt.Sprint(id)
		}
		var auditBefore map[string]map[string]any
		if e.audit != nil { // бичилтээс ӨМНӨХ төлөв (мөрүүд түгжигдсэн)
			if auditBefore, err = e.auditStates(tx, orgID, ids, false); err != nil {
				return err
			}
		}
		if n, err = query.ExecBulk(tx, orgID, req, *e.bulk); err != nil {
			return err
		}
		if err := e.afterBulk(c, tx, orgID, req, ids); err != nil {
			return err
		}
		if e.audit == nil {
			return nil
		}
		if req.Action == "delete" {
			return e.auditBulk(tx, c, orgID, audit.ActionBulkDelete, ids, auditBefore, nil)
		}
		auditAfter, err := e.auditStates(tx, orgID, ids, false)
		if err != nil {
			return err
		}
		return e.auditBulk(tx, c, orgID, audit.ActionBulkUpdate, ids, auditBefore, auditAfter)
	})
	if err != nil {
		if ambiguousWrite(err) { // commit болсон байж магадгүй → хуучирлаас сэргийлж bump
			e.bumpCache(c, orgID)
		}
		return e.queryFail(c, a, err)
	}
	e.bumpCache(c, orgID) // commit-ийн ДАРАА
	return c.JSON(fiber.Map{"affected": n})
}

func (e *engine) metaHandler(c fiber.Ctx) error {
	if _, _, ok := e.gate(c, ActionRead); !ok {
		return nil
	}
	return c.JSON(struct {
		Meta
		Can map[string]bool `json:"can"`
	}{Meta: e.meta(), Can: e.canMap(c)})
}

// optionsHandler — GET /options/:field. Ref талбар бүрийн lookup handler-ийг Mount үед бэлдэнэ.
// RLS үед ref хүснэгтийн policy ажиллахын тулд хүсэлт бүрийг tenant.Tx дотор гүйцэтгэнэ.
func (e *engine) optionsHandler() fiber.Handler {
	handlers := make(map[string]fiber.Handler, len(e.refs))
	for key, f := range e.refs {
		handlers[key] = f.ref.Handler(e.db, e.org)
	}
	return func(c fiber.Ctx) error {
		orgID, gdb, ok := e.gate(c, ActionRead)
		if !ok {
			return nil
		}
		f, found := e.refs[c.Params("field")]
		if !found {
			return jsonErr(c, fiber.StatusNotFound, "Олдсонгүй")
		}
		// run — lookup handler хариугаа c-д өөрөө бичнэ.
		run := func() error {
			if !e.rls {
				return handlers[c.Params("field")](c)
			}
			err := tenant.Tx(c.Context(), gdb, orgID, func(tx *gorm.DB) error {
				return f.ref.Handler(func() *gorm.DB { return tx }, e.org)(c)
			})
			if err != nil {
				return e.fail(c, ActionRead, err)
			}
			return nil
		}
		if !e.cacheReads() || utf8.RuneCountInString(c.Query("search")) > maxCachedSearch {
			return run()
		}
		// Cache: хариуг c-ээс барьж авна. 200 биш хариу (400 параметр, 500) cache-д орохгүй — дамжуулна.
		parts := []string{"options", f.key, c.Query("search"), c.Query("parent"), c.Query("ids"), c.Query("limit")}
		err := e.serveCached(c, orgID, parts, e.canMap(c), func(context.Context) ([]byte, error) {
			if err := run(); err != nil {
				return nil, err
			}
			resp := c.Response()
			if resp.StatusCode() != fiber.StatusOK {
				return nil, &relayErr{status: resp.StatusCode(), ctype: string(resp.Header.ContentType()), body: bytes.Clone(resp.Body())}
			}
			return bytes.Clone(resp.Body()), nil
		}, nil)
		var rel *relayErr
		if errors.As(err, &rel) {
			return writeRelay(c, rel)
		}
		return err
	}
}
