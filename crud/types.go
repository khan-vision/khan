// Package crud нь schema-driven (declarative) CRUD endpoint-ийг typed, тесттэй,
// fail-closed хэлбэрээр өгнө. Resource тодорхойлоход GET/POST/PUT/DELETE, жагсаалт
// (query), options (lookup), meta, children (subform), hook, event, optimistic lock
// бэлэн болно. Гэрээ: docs/CRUD.md.
//
//	r := &crud.Resource[RoomRow]{
//	    Name: "organization.conference_room", Label: "Хурлын өрөө",
//	    Table: "organization.conference_room",
//	    Fields: []crud.Field{
//	        {Name: "room_name", Label: "Нэр", Kind: validate.String, Rules: validate.Rules{Required: true, MaxLen: 500}},
//	        {Name: "capacity", Label: "Багтаамж", Kind: validate.Int, Rules: validate.Rules{Nullable: true, Min: validate.Bound(0)}},
//	    },
//	    List: query.Spec{Search: []string{"t.room_name"}, DefaultOrder: "t.room_name"},
//	    Can:  func(c fiber.Ctx, a crud.Action) bool { return a == crud.ActionRead || isAdmin(c) },
//	    DB:   func() *gorm.DB { return gdb },
//	}
//	if err := r.Mount(app.Group("/api/organization/conference/rooms")); err != nil { log.Fatal(err) }
package crud

import (
	"github.com/gofiber/fiber/v3"
	"github.com/khan-vision/khan/lookup"
	"github.com/khan-vision/khan/query"
	"github.com/khan-vision/khan/validate"
	"gorm.io/gorm"
	"time"
)

// Action — Can-д дамжих үйлдөл.
type Action string

const (
	ActionCreate Action = "create"
	ActionRead   Action = "read"
	ActionUpdate Action = "update"
	ActionDelete Action = "delete"
)

// IDKind — мөрийн id-ийн төрөл.
type IDKind string

const (
	IDUUID IDKind = "uuid"
	IDInt  IDKind = "int"
)

// IDSpec — id баганын тодорхойлолт. Анхдагч: Column "id", Kind IDUUID.
type IDSpec struct {
	Column string
	Kind   IDKind
	// Generate — IDUUID-ийн шинэ id (nil → crypto/rand UUIDv4). IDInt үед ашиглагдахгүй
	// (DB default/serial; INSERT ... RETURNING).
	Generate func() string
}

// Field — бичигдэх талбар (write allowlist).
type Field struct {
	Name   string // JSON түлхүүр (snake_case)
	Column string // хоосон бол Name
	Label  string // Монгол шошго (алдааны мессеж, codegen); хоосон бол Name
	Kind   validate.Kind
	Rules  validate.Rules

	Default    any  // create үед body-д байхгүй бол (Mount үед Rules-аар шалгагдана)
	CreateOnly bool // update үед үл тоомсорлоно

	Ref    *lookup.Spec // FK: утга энэ spec-ээр (тухайн org-д) олдох ёстой; GET /options/<Name>
	Unique bool         // тухайн org дотор давтагдахгүй (soft-deleted-ийг тооцохгүй) → 422
	Input  string       // front_end KvField төрлийн override (ж: "employee")
}

// Child — subform (эцгийн дор мөртэй өгөгдөл).
type Child struct {
	Name       string // JSON түлхүүр, ж: "lines"
	Label      string
	Table      string // org_id-тай хүснэгт ("schema.name")
	FK         string // эцгийн id-г заах багана, ж: "invoice_id"
	ID         IDSpec
	Fields     []Field
	Order      string // эрэмбийн багана (хоосон → ID багана)
	MaxRows    int    // анхдагч 500
	SoftDelete bool
}

// Values — багана → parse+normalize хийгдсэн утга.
type Values map[string]any

// Ctx — hook / cross-field validate-д дамжих контекст.
type Ctx struct {
	Fiber    fiber.Ctx
	Tx       *gorm.DB // write транзакц (Validate-д nil)
	OrgID    string
	UserID   string
	Action   Action
	ID       string         // update/delete; create-д Before*-д UUID, IDInt үед After*-д
	Old      map[string]any // update/delete: түгжсэн хуучин мөр
	Resource string
	// System — ScopeShared: энэ нь системийн (org_id IS NULL) мөрийн бичилт (SystemCan-аар зөвшөөрөгдсөн).
	// Үүнд OrgID нь хүсэлт гаргагчийн org хэвээр — hook системийн мөрийг org-ийн өгөгдөлтэй
	// холбохгүй байхыг өөрөө шалгана.
	System bool
}

// Hooks — lifecycle hook-ууд. Before* нь Values-ийг өөрчилж болно (ж: дугаар олгох);
// hook-ийн нэмсэн утга validate хийгдэхгүй (итгэмжит код). org_id/id-г дарж болохгүй.
type Hooks struct {
	BeforeCreate, AfterCreate func(x *Ctx, v Values) error
	BeforeUpdate, AfterUpdate func(x *Ctx, v Values) error
	BeforeDelete, AfterDelete func(x *Ctx) error
}

// Resource — нэг хүснэгтийн CRUD тодорхойлолт. T нь жагсаалтын мөрийн төрөл
// (NewRows nil үед `*[]T`-д scan хийнэ; T=map[string]any байж болно).
// Mount хийсний дараа Resource-ийг өөрчлөхгүй — handler-ууд зөвхөн
// compile хийгдсэн өөрчлөгдөхгүй төлөвөөс уншина (concurrent-safe).
type Resource[T any] struct {
	Name     string // "organization.conference_room" (event topic, лог, meta)
	Label    string // "Хурлын өрөө" (404 мессеж, codegen)
	Endpoint string // "/api/organization/conference/rooms" (codegen/meta; Mount-д заавал биш)
	Table    string // "schema.name" заавал
	Alias    string // жагсаалтын alias, анхдагч "t"

	// Scope — хүснэгтийн tenant хүрээ. Zero = ScopeTenant (анхдагч, өөрчлөлтгүй). ScopeShared (hybrid:
	// org_id NULL = системийн мөр, tenant-д унших л), ScopeGlobal (org_id баганагүй). Scope.go, docs/CRUD.md §2.1.
	Scope Scope
	// SystemCan — ScopeShared: системийн (org_id IS NULL) мөрийг үүсгэх (`POST /system`), засах, устгах
	// эрх (ж: sysadmin). Can-тай ХАМТ шаардагдана (Can && SystemCan). nil → системийн мөр бичих БОЛОМЖГҮЙ
	// (анхдагч, fail-closed). RLS-тэй хамт хэрэглэхгүй (Mount алдаа). Зөвхөн ScopeShared-д.
	SystemCan func(c fiber.Ctx, a Action) bool
	// GlobalWrites — ScopeGlobal: бичих route-уудыг (POST/PUT/DELETE/bulk) нээнэ. Бичилт нь БҮХ
	// байгууллагад нөлөөлнө тул заавал ил тод баталгаажуулна; Can нь sysadmin-д л true байх ёстой.
	// false (анхдагч) → Global resource унших-л (бичих route mount хийгдэхгүй). Зөвхөн ScopeGlobal-д.
	GlobalWrites bool

	ID     IDSpec
	Fields []Field

	List        query.Spec
	ListQuery   func(db *gorm.DB, orgID string) *gorm.DB
	DetailQuery func(db *gorm.DB, orgID string) *gorm.DB
	Export      *query.ExportSpec
	Bulk        *query.BulkSpec

	Children []Child

	Can func(c fiber.Ctx, a Action) bool
	// OnForbidden — Can=false үед 403-ийн оронд хариуг өөрөө бичнэ (nil → `{"error":"Хандах эрхгүй байна"}`).
	// Зөвхөн БОДИТ татгалзалт (хүсэлтийн үйлдэл) дээр дуудагдана — `can` map тооцоход биш. access.CrudForbidden
	// нь module/capability-тай 403, Source алдаанд 503, deny hook-ийг өгнө.
	OnForbidden func(c fiber.Ctx, a Action) error
	Validate    func(x *Ctx, v Values) error
	Hooks       Hooks

	SoftDelete    bool
	Version       string // optimistic lock багана (ж: "version")
	Timestamps    *bool  // nil=true: created_at/updated_at
	TrackUser     bool   // created_by/updated_by
	RLS           bool
	Events        bool
	StrictUnknown bool
	MaxBody       int // анхдагч 1 MiB
	// Timeout — нэг хүсэлтийн DB ажлын дээд хугацаа (context deadline). 0 → 30с, <0 → хязгааргүй.
	Timeout    time.Duration
	ExposeMeta bool

	// Cache — унших endpoint-уудын cache + ETag/304; nil → унтраалттай. CacheSpec, docs/CACHE.md §4.
	Cache *CacheSpec

	// Audit — өөрчлөлтийн түүх (khan/audit): create/update/delete/bulk бүрийг бизнес транзакцдаа
	// `khan.audit_log`-д бичиж, `GET /history`, `GET /:id/history` нэмнэ. nil → унтраалттай,
	// нөлөөгүй. AuditSpec, docs/AUDIT.md.
	Audit *AuditSpec

	DB      func() *gorm.DB
	Org     func(c fiber.Ctx) (string, bool)
	NewRows func() any
}

// Off / On — `Timestamps: crud.Off()` гэх мэт *bool туслах.
func Off() *bool { b := false; return &b }
func On() *bool  { b := true; return &b }
