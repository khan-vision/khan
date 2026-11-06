package access

import (
	"errors"

	"github.com/gofiber/fiber/v3"
	"github.com/khan-vision/khan/auth"
	"github.com/khan-vision/khan/crud"
	"github.com/khan-vision/khan/obs"
	"github.com/khan-vision/khan/tenant"
)

// Хариуны мессежүүд (UI-д шууд харагдана).
const (
	MsgForbidden    = "Хандах эрхгүй байна"
	MsgUnauthorized = "Зөвшөөрөлгүй"
	MsgUnavailable  = "Эрх шалгах боломжгүй байна"
	MsgMisconfig    = "Серверийн тохиргооны алдаа"
)

// ident — хүсэлтийн identity. ЗӨВХӨН JWT-ээс: org = tenant.OrgID, user = auth.User(c).ID.
// X-Org-ID header, ?org_id, body-д итгэхгүй.
type ident struct {
	org, user string
	portal    bool
}

func identityOf(c fiber.Ctx) (ident, bool) {
	org, err := tenant.OrgID(c)
	if err != nil {
		return ident{}, false
	}
	u, ok := auth.User(c)
	if !ok || u.ID == "" {
		return ident{}, false
	}
	return ident{org: org, user: u.ID, portal: u.PortalOnly}, true
}

func authUserID(c fiber.Ctx) (string, bool) {
	u, ok := auth.User(c)
	return u.ID, ok && u.ID != ""
}

// localsKey — Policy тус бүрийн, хүсэлт доторх cache-ийн түлхүүр (өөр Policy-тэй холилдохгүй).
type localsKey struct{ p *Policy }

type reqEntry struct {
	id  ident
	res *resolved
}

// requestResolved — хүсэлтийн хугацаанд нэг л удаа ачаална (c.Locals); crud-ийн Can нэг
// хүсэлтэд олон дуудагддаг тул хүсэлтийн доторх шийдвэр тогтвортой, хурдан.
func (p *Policy) requestResolved(c fiber.Ctx, id ident) (*resolved, error) {
	if e, ok := c.Locals(localsKey{p}).(*reqEntry); ok && e.id == id {
		return e.res, nil
	}
	res, err := p.resolveUser(c.Context(), id.org, id.user)
	if err != nil {
		return nil, err
	}
	c.Locals(localsKey{p}, &reqEntry{id: id, res: res})
	return res, nil
}

// decideReq — HTTP хүсэлтийн шийдвэр. ok=false → identity байхгүй (401).
func (p *Policy) decideReq(c fiber.Ctx, module string, caps []string) (d Decision, ok bool, err error) {
	id, ok := identityOf(c)
	if !ok {
		return Decision{Reason: ReasonNoIdentity}, false, nil
	}
	res, err := p.requestResolved(c, id)
	if err != nil {
		obs.With(c).Error("access: эрх уншиж чадсангүй — татгалзав (fail-closed)", "error", err)
		return Decision{Reason: ReasonError}, true, err
	}
	d, err = p.decide(c.Context(), id.org, res, id.portal, module, caps)
	if err != nil {
		obs.With(c).Error("access: эрх шалгаж чадсангүй — татгалзав (fail-closed)", "error", err)
	}
	return d, true, err
}

// Can — нэвтэрсэн хэрэглэгч module-ийн cap-ийг хийж чадах эсэх. Identity байхгүй, Source
// алдаа, manifest-д байхгүй нэр → false.
func (p *Policy) Can(c fiber.Ctx, module, capability string) bool {
	d, ok, err := p.decideReq(c, module, []string{capability})
	return ok && err == nil && d.Allowed
}

// CanAny — caps-ийн аль нэг нь.
func (p *Policy) CanAny(c fiber.Ctx, module string, caps ...string) bool {
	d, ok, err := p.decideReq(c, module, caps)
	return ok && err == nil && d.Allowed
}

// Require — caps-ийн АЛЬ НЭГИЙГ (any-of) шаардах Fiber middleware.
//
//   - JWT-д org эсвэл user байхгүй → 401 `{"error":"Зөвшөөрөлгүй"}`
//   - эрхгүй → 403 `{"error":"Хандах эрхгүй байна","module":"<key>","capability":"<эхний cap>"}`;
//     any-of (нэгээс олон cap) бол нэмэлтээр `"capabilities":[...]` (бүгд). module/cap нь нууц
//     биш (manifest нээлттэй); хэрэглэгчийн өгөгдөл, олгосон эрхийн мэдээлэл ХЭЗЭЭ Ч орохгүй.
//   - Source алдаа → 503 (татгалзана, лог бичнэ; 403 биш — түр зуурын, FE дахин оролдоно)
//   - татгалзал бүр [WithDenyHook]-д дамжина
//   - module/cap manifest-д байхгүй (үсгийн алдаа) → 500 (бүртгэхэд лог) — ХЭЗЭЭ Ч нэвтрүүлэхгүй.
//     Эхлэхэд [Policy.Verify]-аар барьж болно.
//
// Fiber v3-т `Group(prefix, mw)` нь `Use`-тэй адил тул эцэг prefix-д залгахдаа анхаар.
func (p *Policy) Require(module string, caps ...string) fiber.Handler {
	caps = append([]string(nil), caps...)
	if err := p.Verify(module, caps...); err != nil {
		p.log.Error("access: Require буруу тохируулагдсан — бүх хүсэлтийг татгалзана", "error", err)
		return func(c fiber.Ctx) error { return jsonErr(c, fiber.StatusInternalServerError, MsgMisconfig) }
	}
	return func(c fiber.Ctx) error {
		d, ok, err := p.decideReq(c, module, caps)
		switch {
		case !ok:
			p.notifyDeny(c, module, caps, DenialUnauthenticated)
			return jsonErr(c, fiber.StatusUnauthorized, MsgUnauthorized)
		case err != nil:
			p.notifyDeny(c, module, caps, DenialSourceError)
			return jsonErr(c, fiber.StatusServiceUnavailable, MsgUnavailable)
		case !d.Allowed:
			p.notifyDeny(c, module, caps, reasonOf(d))
			return c.Status(fiber.StatusForbidden).JSON(forbiddenBody(module, caps))
		}
		return c.Next()
	}
}

func jsonErr(c fiber.Ctx, status int, msg string) error {
	return c.Status(status).JSON(fiber.Map{"error": msg})
}

// ActionCaps — crud үйлдэл бүрд шаардах capability-ууд (any-of). Хоосон үйлдэл = ТАТГАЛЗАНА
// (fail-closed): үйлдлийг зөвшөөрөхийн тулд тодорхой cap заа.
type ActionCaps struct {
	Read, Create, Update, Delete []string
}

func (a ActionCaps) forAction(act crud.Action) []string {
	switch act {
	case crud.ActionRead:
		return a.Read
	case crud.ActionCreate:
		return a.Create
	case crud.ActionUpdate:
		return a.Update
	case crud.ActionDelete:
		return a.Delete
	}
	return nil
}

// CrudCan — `crud.Resource.Can`-д шууд тохирох функц: үйлдлийг cap-д буулгаж шалгана.
//
//	Can: pol.CrudCan("finance", access.ActionCaps{
//	    Read:   {"invoices.view"},
//	    Create: {"invoices.create"},
//	    Update: {"invoices.edit"},
//	    Delete: {"invoices.delete"},
//	}),
//
// Манифестд байхгүй cap, тодорхойгүй/хоосон үйлдэл → үргэлж false (лог бичигдэнэ).
func (p *Policy) CrudCan(module string, m ActionCaps) func(fiber.Ctx, crud.Action) bool {
	m = ActionCaps{
		Read: append([]string(nil), m.Read...), Create: append([]string(nil), m.Create...),
		Update: append([]string(nil), m.Update...), Delete: append([]string(nil), m.Delete...),
	}
	valid := map[crud.Action]bool{}
	for _, act := range []crud.Action{crud.ActionRead, crud.ActionCreate, crud.ActionUpdate, crud.ActionDelete} {
		caps := m.forAction(act)
		if len(caps) == 0 {
			continue
		}
		if err := p.Verify(module, caps...); err != nil {
			p.log.Error("access: CrudCan буруу тохируулагдсан — үйлдлийг татгалзана", "action", string(act), "error", err)
			continue
		}
		valid[act] = true
	}
	return func(c fiber.Ctx, act crud.Action) bool {
		if !valid[act] {
			return false
		}
		return p.CanAny(c, module, m.forAction(act)...)
	}
}

// Me — нэвтэрсэн хэрэглэгчийн effective эрх (frontend `/me`). Identity байхгүй бол ErrNoOrg.
func (p *Policy) Me(c fiber.Ctx) (Snapshot, error) {
	id, ok := identityOf(c)
	if !ok {
		return Snapshot{Modules: map[string]ModuleSnapshot{}}, ErrNoOrg
	}
	res, err := p.requestResolved(c, id)
	if err != nil {
		obs.With(c).Error("access: эрх уншиж чадсангүй", "error", err)
		return Snapshot{Modules: map[string]ModuleSnapshot{}}, err
	}
	return p.snapshotOf(res), nil
}

// MeHandler — `GET /me`: [Snapshot] JSON (401 identity-гүй, 503 Source алдаа).
// Хариуг `Cache-Control: no-store` болгоно (эрх хэрэглэгч бүрийнх).
func (p *Policy) MeHandler() fiber.Handler {
	return func(c fiber.Ctx) error {
		snap, err := p.Me(c)
		if err != nil {
			if errors.Is(err, ErrNoOrg) {
				return jsonErr(c, fiber.StatusUnauthorized, MsgUnauthorized)
			}
			return jsonErr(c, fiber.StatusServiceUnavailable, MsgUnavailable)
		}
		c.Set(fiber.HeaderCacheControl, "no-store")
		return c.JSON(snap)
	}
}

// ManifestHandler — `GET /modules`: бүх manifest (эрх тохируулах UI-д). Нэвтэрсэн
// хэрэглэгчид л өгнө (401 өөрөөс).
func (p *Policy) ManifestHandler() fiber.Handler {
	return func(c fiber.Ctx) error {
		if _, ok := identityOf(c); !ok {
			return jsonErr(c, fiber.StatusUnauthorized, MsgUnauthorized)
		}
		return c.JSON(p.reg.All())
	}
}

// CrudForbidden — `crud.Resource.OnForbidden`-д: crud-ийн БОДИТ татгалзалт дээр (хүсэлтийн
// үйлдэл) [Policy.Require]-тэй ижил хариу ба deny hook. `can` map тооцоолоход дуудагдахгүй тул
// хуурамч denial бүртгэгдэхгүй.
//
//	caps := access.ActionCaps{Read: {"invoices.view"}, Create: {"invoices.create"}}
//	Can:         pol.CrudCan("finance", caps),
//	OnForbidden: pol.CrudForbidden("finance", caps),
//
// Хариу: нэвтрээгүй → 401, Source алдаа → 503 (түр зуурын), бусад → 403
// `{"error","module","capability"[,"capabilities"]}`.
func (p *Policy) CrudForbidden(module string, m ActionCaps) func(fiber.Ctx, crud.Action) error {
	m = ActionCaps{
		Read: append([]string(nil), m.Read...), Create: append([]string(nil), m.Create...),
		Update: append([]string(nil), m.Update...), Delete: append([]string(nil), m.Delete...),
	}
	return func(c fiber.Ctx, act crud.Action) error {
		caps := m.forAction(act)
		if len(caps) == 0 {
			p.notifyDeny(c, module, caps, DenialNoGrant)
			return c.Status(fiber.StatusForbidden).JSON(forbiddenBody(module, caps))
		}
		_, ok, err := p.decideReq(c, module, caps)
		switch {
		case !ok:
			p.notifyDeny(c, module, caps, DenialUnauthenticated)
			return jsonErr(c, fiber.StatusUnauthorized, MsgUnauthorized)
		case err != nil:
			p.notifyDeny(c, module, caps, DenialSourceError)
			return jsonErr(c, fiber.StatusServiceUnavailable, MsgUnavailable)
		}
		p.ReportDenied(c, module, caps...)
		return c.Status(fiber.StatusForbidden).JSON(forbiddenBody(module, caps))
	}
}
