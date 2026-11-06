package audit

import (
	"errors"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/khan-vision/khan/obs"
	"github.com/khan-vision/khan/tenant"
	"gorm.io/gorm"
)

// HandlerConfig — түүх унших HTTP handler-ийн тохиргоо (нэг resource-д).
type HandlerConfig struct {
	Resource string                           // заавал: "finance.invoice"
	DB       func() *gorm.DB                  // заавал
	Org      func(c fiber.Ctx) (string, bool) // nil → tenant.Require (JWT-ээс; 401 бичнэ)
	// Can — унших эрх. ЗААВАЛ (fail-closed): түүх нь хуучин утгуудыг агуулдаг тул
	// resource-ийг унших эрхээс сулруулахгүй.
	Can func(c fiber.Ctx) bool
	// RLS — унших query-г tenant.Tx дотор (app.org_id тавьж) ажиллуулна.
	RLS bool
	// IDParam — Record handler-ийн path параметр (анхдагч "id").
	IDParam string
	// ParseID — path id-г шалгаж каноник текст болгоно (nil → хоосон биш, ≤200 тэмдэгт).
	// crud нь id-ийн төрлөөр (uuid/int) шалгадаг.
	ParseID func(raw string) (string, bool)
	// Visible — Record handler-ийн мөр харагдах эсэхийн шалгалт (resource-ийн DetailQuery-ийн
	// мөрийн харагдац). Унших query-тэй ИЖИЛ db (RLS үед tenant.Tx)-ээр дуудагдана; false бол 404
	// (NotFound). nil бол шалгалтгүй (зөвхөн ActionRead + org). Алдаа → 500.
	Visible func(c fiber.Ctx, db *gorm.DB, orgID, recordID string) (bool, error)
	// NotFound / Forbidden — 404 / 403 хариуг дүрслэх (nil → анхдагч JSON). crud нь өөрийн
	// форматыг (OnForbidden) дамжуулна.
	NotFound  fiber.Handler
	Forbidden fiber.Handler
}

// Handlers — хоёр read handler.
type Handlers struct {
	// Record — нэг мөрийн түүх (route: GET /:id/history).
	Record fiber.Handler
	// List — resource-ийн бүх түүх (route: GET /history). Query: actor_id, action, record_id,
	// request_id, from, to.
	List fiber.Handler
}

// Handler — Handlers үүсгэнэ. Хариу: `{"data":[Item…],"total":N,"page":P,"limit":L}`,
// ШИНЭ нь эхэнд. Query: page, limit (≤100), from/to (RFC3339 эсвэл YYYY-MM-DD; to нь өдрийг
// оролцуулна), snapshot=1 (snapshot оруулах), before_id (keyset: id < before_id; page ≤ 1000). Өөр org-ийн түүх хэзээ ч гарахгүй —
// org нь ЗӨВХӨН JWT-ээс (хоосон хариу, 404 биш: түүх нь устгагдсан мөрийнх ч байж болно).
func Handler(cfg HandlerConfig) (Handlers, error) {
	switch {
	case !resourceRe.MatchString(cfg.Resource):
		return Handlers{}, errors.New("audit: HandlerConfig.Resource буруу: " + cfg.Resource)
	case cfg.DB == nil:
		return Handlers{}, errors.New("audit: HandlerConfig.DB заавал")
	case cfg.Can == nil:
		return Handlers{}, errors.New("audit: HandlerConfig.Can заавал (fail-closed)")
	}
	if cfg.Org == nil {
		cfg.Org = tenant.Require
	}
	if cfg.IDParam == "" {
		cfg.IDParam = "id"
	}
	if cfg.ParseID == nil {
		cfg.ParseID = func(raw string) (string, bool) {
			return raw, raw != "" && len(raw) <= maxRecordID
		}
	}
	h := &handler{cfg}
	return Handlers{
		Record: func(c fiber.Ctx) error { return h.serve(c, true) },
		List:   func(c fiber.Ctx) error { return h.serve(c, false) },
	}, nil
}

type handler struct{ cfg HandlerConfig }

func bad(c fiber.Ctx, msg string) error {
	return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": msg})
}

func (h *handler) serve(c fiber.Ctx, record bool) error {
	orgID, ok := h.cfg.Org(c)
	if !ok {
		return nil
	}
	if !h.cfg.Can(c) {
		if h.cfg.Forbidden != nil {
			return h.cfg.Forbidden(c)
		}
		return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "Хандах эрхгүй байна"})
	}
	f, msg := h.filter(c, orgID, record)
	if msg != "" {
		return bad(c, msg)
	}
	gdb := h.cfg.DB()
	if gdb == nil {
		obs.With(c).Error("audit: DB nil", "resource", h.cfg.Resource)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Дотоод алдаа гарлаа"})
	}
	var page Page
	visible := true
	run := func(tx *gorm.DB) (err error) {
		if record && h.cfg.Visible != nil {
			if visible, err = h.cfg.Visible(c, tx, orgID, f.RecordID); err != nil || !visible {
				return err
			}
		}
		page, err = List(c.Context(), tx, f)
		return err
	}
	var err error
	if h.cfg.RLS {
		err = tenant.Tx(c.Context(), gdb, orgID, run)
	} else {
		err = run(gdb.WithContext(c.Context()))
	}
	if err == nil && !visible {
		if h.cfg.NotFound != nil {
			return h.cfg.NotFound(c)
		}
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "Олдсонгүй"})
	}
	if err != nil {
		obs.With(c).Error("audit: түүх уншиж чадсангүй", "resource", h.cfg.Resource, "err", err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Дотоод алдаа гарлаа"})
	}
	return c.JSON(page)
}

// filter — query параметрүүдээс Filter. msg != "" бол 400 мессеж.
func (h *handler) filter(c fiber.Ctx, orgID string, record bool) (Filter, string) {
	f := Filter{
		OrgID:    orgID,
		NoOrg:    orgID == "",
		Resource: h.cfg.Resource,
		Page:     fiber.Query[int](c, "page", 1),
		Limit:    fiber.Query[int](c, "limit", DefaultLimit),
	}
	if f.Page < 1 {
		return f, "page буруу"
	}
	if f.Page > MaxPage {
		return f, "page хэт гүн (≤ 1000): before_id ашиглана уу"
	}
	f.BeforeID = fiber.Query[int64](c, "before_id", 0)
	if f.BeforeID < 0 {
		return f, "before_id буруу"
	}
	if f.Limit < 1 {
		return f, "limit буруу"
	}
	switch strings.ToLower(c.Query("snapshot")) {
	case "1", "true":
		f.IncludeSnapshot = true
	}
	if record {
		id, ok := h.cfg.ParseID(c.Params(h.cfg.IDParam))
		if !ok {
			return f, "id буруу"
		}
		f.RecordID = id
	} else if v := c.Query("record_id"); v != "" {
		f.RecordID = v
	}
	f.ActorID = c.Query("actor_id")
	f.Action = c.Query("action")
	f.RequestID = c.Query("request_id")
	for _, v := range []string{f.RecordID, f.ActorID, f.Action, f.RequestID} {
		if strings.IndexByte(v, 0) >= 0 { // NUL → Postgres 22021 (500) биш 400
			return f, "шүүлт буруу"
		}
	}
	var err error
	if f.From, _, err = parseTimeParam(c.Query("from"), false); err != nil {
		return f, "from буруу (RFC3339 эсвэл YYYY-MM-DD)"
	}
	if f.To, _, err = parseTimeParam(c.Query("to"), true); err != nil {
		return f, "to буруу (RFC3339 эсвэл YYYY-MM-DD)"
	}
	return f, ""
}

// parseTimeParam — RFC3339 эсвэл YYYY-MM-DD (UTC). endOfDay=true үед өдөр дангаараа бол
// маргаашийн 00:00 (to нь өдрийг оролцуулна). Хоосон → zero.
func parseTimeParam(s string, endOfDay bool) (time.Time, bool, error) {
	if s == "" {
		return time.Time{}, false, nil
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, true, nil
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return time.Time{}, false, err
	}
	if endOfDay {
		t = t.AddDate(0, 0, 1)
	}
	return t, true, nil
}
