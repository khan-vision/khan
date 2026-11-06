package crud

import (
	"fmt"
	"strings"

	"github.com/gofiber/fiber/v3"
	"github.com/khan-vision/khan/tenant"
	"gorm.io/gorm"
)

// Scope — resource-ийн хүснэгт байгууллагын (tenant) хүрээнд хэрхэн хуваагдахыг тодорхойлно.
// Zero утга нь ScopeTenant — одоогийн (анхдагч) үйлдэл, өөрчлөлтгүй. Гэрээ: docs/CRUD.md §2.1.
type Scope uint8

const (
	// ScopeTenant — org_id NOT NULL: мөр бүр нэг байгууллагынх. Унших/бичих бүр `org_id = JWT org`.
	ScopeTenant Scope = iota
	// ScopeShared — hybrid: org_id NULLABLE, NULL = бүх байгууллагад харагдах СИСТЕМИЙН мөр
	// (ж: attendance.lut_attendance_request). Унших нь `org_id = JWT org OR org_id IS NULL`; системийн
	// мөр tenant-д ЗӨВХӨН УНШИХ (update/delete/bulk → 403). Create үргэлж org_id = JWT org.
	// Resource.SystemCan өгвөл тэр эрхтэй хэрэглэгч (sysadmin) `POST /system`, PUT, DELETE-ээр системийн
	// мөрийг удирдана.
	ScopeShared
	// ScopeGlobal — org_id баганагүй платформ/лавлах хүснэгт. Нэвтэрсэн хэрэглэгч бүр Can(read)-ээр
	// уншина; бичилт нь Resource.GlobalWrites=true үед л (бүх байгууллагад нөлөөлнө — Can нь
	// sysadmin-д л true байх ёстой).
	ScopeGlobal
)

// String — "tenant" | "shared" | "global" (Meta, codegen).
func (s Scope) String() string {
	switch s {
	case ScopeTenant:
		return "tenant"
	case ScopeShared:
		return "shared"
	case ScopeGlobal:
		return "global"
	}
	return fmt.Sprintf("scope(%d)", uint8(s))
}

func (s Scope) valid() bool { return s <= ScopeGlobal }

const (
	// globalCacheOrg — ScopeGlobal resource-ийн cache generation/түлхүүрийн тогтмол namespace
	// (өгөгдөл org-оос үл хамаарах тул нэг cache бүх org-д хуваалцагдана).
	globalCacheOrg = "_global"
	// systemCacheOrg — ScopeShared resource-ийн системийн мөрийн generation-ийн namespace. Системийн
	// мөрийн бичилт үүнийг bump хийж, Shared resource бүрийн унших түлхүүр/ETag-д орсон тул бүх org-ийн
	// cache-ийг хүчингүй болгоно.
	systemCacheOrg = "_system"

	msgSystemRow = "Системийн мөрийг өөрчлөх боломжгүй"
)

// owner — бичих мөрийн эзэмшигч: org (tenant мөр) эсвэл системийн мөр (ScopeShared, org_id IS NULL).
// org нь ҮРГЭЛЖ хүсэлт гаргагчийн (JWT) org — аудит/event/ref шалгалтад хэрэглэгдэнэ.
type owner struct {
	org    string
	system bool
}

// orgValue — INSERT-ийн org_id утга (системийн мөрөнд NULL).
func (o owner) orgValue() any {
	if o.system {
		return nil
	}
	return o.org
}

// readWhere — унших нөхцөл: Tenant `org_id = ?`, Shared `(org_id = ? OR org_id IS NULL)`, Global — нөхцөлгүй.
// prefix нь "" эсвэл "t." (цэгтэй).
func (e *engine) readWhere(q *gorm.DB, prefix, orgID string) *gorm.DB {
	switch e.scope {
	case ScopeShared:
		if orgID == "" { // org-гүй системийн принсипал: зөвхөн системийн мөр
			return tenant.SystemRow(q).Where(prefix + "org_id IS NULL")
		}
		return q.Where(tenant.SharedCond(prefix), orgID)
	case ScopeGlobal:
		return q
	}
	return q.Where(prefix+"org_id = ?", orgID)
}

// ownWhere — бичих (мөр заах) нөхцөл: Tenant/Shared-ийн өөрийн мөр `org_id = ?`, системийн мөр
// `org_id IS NULL` (Guard-д SystemRow-оор ил тод), Global — нөхцөлгүй. Системийн мөр өөрчлөх нь
// ЗӨВХӨН энэ замаар; өөр org-ийн мөр хэзээ ч таарахгүй.
func (e *engine) ownWhere(q *gorm.DB, prefix string, o owner) *gorm.DB {
	switch {
	case e.scope == ScopeGlobal:
		return q
	case o.system:
		return tenant.SystemRow(q).Where(prefix + "org_id IS NULL")
	}
	return q.Where(prefix+"org_id = ?", o.org)
}

// rowOwner — түгжсэн (унших нөхцөлөөр олдсон) мөрийн эзэмшигч. Shared-д org_id NULL → системийн;
// өөр org-ийн утга ирвэл (унших нөхцөл зөрчигдсөн — боломжгүй) fail-closed errNotFound.
func (e *engine) rowOwner(orgID string, row map[string]any) (owner, error) {
	o := owner{org: orgID}
	if e.scope != ScopeShared {
		return o, nil
	}
	v, ok := row["org_id"]
	if !ok {
		return o, fmt.Errorf("khan/crud: %s: мөрийн org_id уншигдсангүй", e.name)
	}
	if v == nil {
		o.system = true
		return o, nil
	}
	if !strings.EqualFold(fmt.Sprint(normalizeScan(v)), orgID) { // uuid харьцуулалт том/жижиг үсэг үл ялгана
		return o, errNotFound
	}
	return o, nil
}

// authorizeRow — системийн мөрийг өөрчлөхийг SystemCan-аар шалгана. Tenant/өөрийн мөр, Global — нөлөөгүй.
func (e *engine) authorizeRow(c fiber.Ctx, a Action, o owner) error {
	if !o.system {
		return nil
	}
	if e.systemAllowed(c, a) {
		return nil
	}
	return Forbidden(msgSystemRow)
}

// systemAllowed — SystemCan тохируулагдсан бөгөөд үйлдлийг зөвшөөрч байна уу (nil → хэзээ ч).
func (e *engine) systemAllowed(c fiber.Ctx, a Action) bool {
	return e.systemCan != nil && e.systemCan(c, a)
}

// cacheOrg — cache generation/түлхүүр/ETag-ийн org namespace.
func (e *engine) cacheOrg(orgID string) string {
	if e.scope == ScopeGlobal {
		return globalCacheOrg
	}
	if orgID == "" { // org-гүй системийн принсипал (Shared): системийн мөрийн namespace
		return systemCacheOrg
	}
	return orgID
}

// resolveOrg — хүсэлтийн org. Анхдагч e.org (tenant.Require → 401). SystemPrincipal тохируулагдсан, scope нь
// Global/Shared, JWT-д хүчинтэй org_id БАЙХГҮЙ, принсипал таарвал org="" (org-гүй) гэж амжилттай буцна.
// Өөр бүх тохиолдолд (ScopeTenant, энгийн хэрэглэгч) e.org — өөрчлөлтгүй fail-closed.
func (e *engine) resolveOrg(c fiber.Ctx) (string, bool) {
	if e.sysPrincipal != nil && e.scope != ScopeTenant {
		if _, err := tenant.OrgID(c); err != nil {
			if _, authed := tenant.Claims(c); authed && e.sysPrincipal(c) {
				return "", true
			}
		}
	}
	return e.org(c)
}

// needOrg — org-гүй (системийн принсипал) хүсэлтийг org шаардсан үйлдэлд (энгийн мөр үүсгэх) 401-ээр зогсооно.
func needOrg(c fiber.Ctx, orgID string) bool {
	if orgID != "" {
		return true
	}
	_ = jsonErr(c, fiber.StatusUnauthorized, "Зөвшөөрөлгүй")
	return false
}

// AnyPrincipal — нэвтэрсэн (JWT claims-тэй) хэн ч. Single-tenant төслийн Resource.SystemPrincipal.
func AnyPrincipal(c fiber.Ctx) bool { _, ok := tenant.Claims(c); return ok }

// SingleTenant — org_id-гүй (single-tenant) төслийн Resource загвар: Scope=ScopeGlobal, GlobalWrites=true,
// SystemPrincipal=AnyPrincipal (JWT-д org шаардахгүй). Can заавал өөрөө тавина. Унших-л бол дараа нь
// `r.GlobalWrites = false`. docs/CRUD.md «Single-tenant төслүүд».
func (r *Resource[T]) SingleTenant() *Resource[T] {
	r.Scope, r.GlobalWrites = ScopeGlobal, true
	if r.SystemPrincipal == nil {
		r.SystemPrincipal = AnyPrincipal
	}
	return r
}

// writable — бичих route-ууд mount хийгдэх эсэх (Global-д зөвхөн GlobalWrites үед).
func (e *engine) writable() bool { return e.scope != ScopeGlobal || e.globalWrites }

// checkScope — Scope-той холбоотой тохиргооны зөрчлийг Mount/Check үед барина (fail-closed).
func (e *engine) checkScope() error {
	if e.scope != ScopeShared && e.systemCan != nil {
		return fmt.Errorf("SystemCan зөвхөн ScopeShared-д (одоо %s)", e.scope)
	}
	if e.scope == ScopeTenant && e.sysPrincipal != nil {
		return fmt.Errorf("SystemPrincipal зөвхөн ScopeShared/ScopeGlobal-д (ScopeTenant org заавал)")
	}
	if e.scope != ScopeGlobal && e.globalWrites {
		return fmt.Errorf("GlobalWrites зөвхөн ScopeGlobal-д (одоо %s)", e.scope)
	}
	switch e.scope {
	case ScopeShared:
		if e.systemCan != nil && e.rls {
			// Системийн мөр (org_id NULL) бичихэд RLS-ийн bypass хэрэгтэй, гэхдээ hook/children-тэй бүтэн
			// транзакцыг bypass-д оруулахгүй. Системийн мөрийг миграци/админ хэрэгслээр удирд.
			return fmt.Errorf("ScopeShared: SystemCan ба RLS хамт дэмжигдэхгүй (tenant.PolicyOptions.SystemRows-тэй RLS нь унших-л)")
		}
	case ScopeGlobal:
		if e.rls {
			return fmt.Errorf("ScopeGlobal: RLS хэрэглэх боломжгүй (хүснэгтэд org_id байхгүй)")
		}
		if e.cache != nil && len(e.cache.dependsOn) > 0 {
			// Global cache нь бүх org-д хуваалцагдана; org-хамааралтай resource-ийн generation оруулбал
			// өгөгдөл org хооронд холилдох эрсдэлтэй.
			return fmt.Errorf("ScopeGlobal: Cache.DependsOn дэмжигдэхгүй")
		}
		if e.bulk != nil && !e.globalWrites {
			return fmt.Errorf("ScopeGlobal: Bulk нь GlobalWrites шаардана")
		}
		for i := range e.fields {
			if err := globalRef(&e.fields[i]); err != nil {
				return err
			}
		}
		for ci := range e.children {
			for i := range e.children[ci].fields {
				if err := globalRef(&e.children[ci].fields[i]); err != nil {
					return fmt.Errorf("child %q: %w", e.children[ci].Name, err)
				}
			}
		}
	}
	return nil
}

// globalRef — Global мөр org-д хамаарсан лавлах руу заах ёсгүй (org хооронд өгөгдөл холилдоно).
func globalRef(f *fieldPlan) error {
	if f.ref != nil && f.ref.TenantScoped() {
		return fmt.Errorf("ScopeGlobal: талбар %q: Ref нь Tenant: lookup.TenantOff() байх ёстой", f.Name)
	}
	return nil
}
