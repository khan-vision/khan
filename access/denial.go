package access

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/khan-vision/khan/audit"
	"github.com/khan-vision/khan/obs"
	"github.com/khan-vision/khan/tenant"
	"gorm.io/gorm"
)

// DenialReason — татгалзалтын шалтгаан ([Denial]).
type DenialReason string

// Татгалзалтын шалтгаанууд.
const (
	// DenialNoGrant — эрх олгоогүй (манифестэд байхгүй нэр ч энд орно).
	DenialNoGrant DenialReason = "no-grant"
	// DenialDeny — тодорхой deny (эсвэл [OwnerDeniable] owner-ийн deny).
	DenialDeny DenialReason = "deny"
	// DenialSourceError — Source алдаа (503).
	DenialSourceError DenialReason = "source-error"
	// DenialUnauthenticated — JWT-д org/user байхгүй (401).
	DenialUnauthenticated DenialReason = "unauthenticated"
)

// Denial — HTTP helper-ийн татгалзсан шийдвэрийн тайлан ([WithDenyHook]).
// Хэрэглэгчийн өгөгдөл, олгосон эрхийн дэлгэрэнгүй АГУУЛААГҮЙ.
type Denial struct {
	Org, User string // identity (JWT-ээс); тодорхойгүй бол хоосон
	Module    string
	Caps      []string // шаардсан capability-ууд (any-of)
	Reason    DenialReason
	RequestID string // obs
	Path      string
	Method    string
}

// DenyHook — татгалзал бүрт дуудагдах функц.
type DenyHook func(c fiber.Ctx, d Denial)

// WithDenyHook — [Policy.Require] (401/403/503) болон [Policy.ReportDenied] татгалзал бүрт hook дуудна.
// Hook нь хариуг ӨӨРЧЛӨХГҮЙ: panic recover хийгдэж логлогдоно. Hook синхрон дуудагдана (c-г
// зөвхөн дуудлагын дотор ашигла); удаан ажил бол өөрөө goroutine-д гарга ([AuditDenials] нь богино
// timeout-той).
//
// [Policy.Can]/[Policy.CanAny]/[Policy.CrudCan]/Me-д hook ДУУДАГДАХГҮЙ: crud нь `can` map-д
// дөрвөн үйлдлийг бүгдийг шалгадаг тул false нь хүсэлтийн татгалзал биш. Crud-ийн өөрийн 403-ыг
// бүртгэх бол app-аасаа [Policy.ReportDenied] дууд.
func WithDenyHook(h func(c fiber.Ctx, d Denial)) Option {
	return func(o *options) {
		if h == nil {
			o.errs = append(o.errs, errors.New("access: DenyHook nil"))
			return
		}
		o.denyHook = h
	}
}

type deniedKey struct{}

// LastDenial — энэ хүсэлтэд [Policy.Require] эсвэл [Policy.ReportDenied] татгалзсан бол тайланг буцаана
// (app-ийн error handler, лог).
func LastDenial(c fiber.Ctx) (Denial, bool) {
	d, ok := c.Locals(deniedKey{}).(Denial)
	return d, ok
}

// ReportDenied — өөр код (ж: crud-ийн 403) татгалзсан үед hook-д мэдэгдэнэ. Reason нь
// шалгаж байгаа caps-аас тодорхойлогдоно (Source алдаа/deny/no-grant/unauthenticated).
func (p *Policy) ReportDenied(c fiber.Ctx, module string, caps ...string) {
	d, ok, err := p.decideReq(c, module, caps)
	switch {
	case !ok:
		p.notifyDeny(c, module, caps, DenialUnauthenticated)
	case err != nil:
		p.notifyDeny(c, module, caps, DenialSourceError)
	case !d.Allowed:
		p.notifyDeny(c, module, caps, reasonOf(d))
	}
}

func reasonOf(d Decision) DenialReason {
	if d.Reason == ReasonDenied {
		return DenialDeny
	}
	return DenialNoGrant
}

func (p *Policy) notifyDeny(c fiber.Ctx, module string, caps []string, r DenialReason) {
	d := Denial{
		Module: module, Caps: append([]string(nil), caps...), Reason: r,
		RequestID: obs.RequestIDFrom(c.Context()), Path: c.Path(), Method: c.Method(),
	}
	d.Org, d.User = partialIdentity(c)
	c.Locals(deniedKey{}, d)
	if p.denyHook == nil {
		return
	}
	defer func() {
		if rec := recover(); rec != nil {
			p.log.Error("access: DenyHook panic — үл тооцов", "panic", fmt.Sprint(rec), "module", module)
		}
	}()
	p.denyHook(c, d)
}

// partialIdentity — байгаа хэсгээр нь (org л байж болно).
func partialIdentity(c fiber.Ctx) (org, user string) {
	if o, err := tenant.OrgID(c); err == nil {
		org = o
	}
	if u, ok := authUserID(c); ok {
		user = u
	}
	return org, user
}

// forbiddenBody — 403 JSON: module/capability нь нууц биш (manifest нээлттэй), FE мессеж зохиоход ашигладаг.
func forbiddenBody(module string, caps []string) fiber.Map {
	m := fiber.Map{"error": MsgForbidden, "module": module}
	if len(caps) > 0 {
		m["capability"] = caps[0]
	}
	if len(caps) > 1 {
		m["capabilities"] = caps
	}
	return m
}

// ─── AuditDenials ───

// AuditAction — [AuditDenials]-ийн бичих audit action.
const AuditAction audit.Action = "access_denied"

const (
	auditTimeout    = 2 * time.Second
	auditThrottle   = time.Minute
	auditMaxKeys    = 10_000
	auditMaxRecordN = 200
)

// AuditDenials — [WithDenyHook]-д шууд залгах hook: татгалзал бүрийг `audit.Entry` (action
// "access_denied", resource = модуль, changes nil, meta = audit.MetaFrom) болгож, ТУСДАА богино
// транзакцад (2с timeout) бичнэ. Бичилт амжилтгүй бол зөвхөн лог — хариу өөрчлөгдөхгүй.
//
// Log flooding-ээс сэргийлж (org, user, module, caps) тус бүрд минутад НЭГ л мөр бичнэ
// (санах ойн, хязгаартай map; 10k түлхүүрээс хэтэрвэл хугацаа дууссаныг, дараа нь бүгдийг цэвэрлэнэ).
// Org тодорхойгүй (unauthenticated) татгалзал audit хүснэгтэд (org_id NOT NULL) бичигдэхгүй.
//
// audit.Entry.RecordID хоосон байж болохгүй тул `<reason>:<cap1,cap2>` гэж бичнэ.
func AuditDenials(db *gorm.DB) func(fiber.Ctx, Denial) {
	return auditDenials(db, time.Now, auditTimeout)
}

func auditDenials(db *gorm.DB, now func() time.Time, timeout time.Duration) func(fiber.Ctx, Denial) {
	return throttledAudit(now, func(e audit.Entry) error {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error { return audit.Record(tx, e) })
	})
}

func throttledAudit(now func() time.Time, record func(audit.Entry) error) func(fiber.Ctx, Denial) {
	var mu sync.Mutex
	seen := map[string]time.Time{}
	allow := func(key string) bool {
		mu.Lock()
		defer mu.Unlock()
		t := now()
		if last, ok := seen[key]; ok && t.Sub(last) < auditThrottle {
			return false
		}
		if len(seen) >= auditMaxKeys {
			for k, v := range seen {
				if t.Sub(v) >= auditThrottle {
					delete(seen, k)
				}
			}
			if len(seen) >= auditMaxKeys {
				clear(seen)
			}
		}
		seen[key] = t
		return true
	}
	return func(c fiber.Ctx, d Denial) {
		if d.Org == "" || d.Module == "" {
			return
		}
		caps := strings.Join(d.Caps, ",")
		if !allow(strings.Join([]string{d.Org, d.User, d.Module, caps}, "\x00")) {
			return
		}
		rec := string(d.Reason) + ":" + caps
		if r := []rune(rec); len(r) > auditMaxRecordN {
			rec = string(r[:auditMaxRecordN])
		}
		e := audit.Entry{OrgID: d.Org, Resource: d.Module, RecordID: rec, Action: AuditAction, Meta: audit.MetaFrom(c)}
		if e.RequestID == "" {
			e.RequestID = d.RequestID
		}
		if err := record(e); err != nil {
			slog.Default().Error("access: татгалзлыг audit-д бичиж чадсангүй", "module", d.Module, "error", err)
		}
	}
}
