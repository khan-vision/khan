# crud / validate / lookup — дизайны гэрээ

Энэ баримт нь `crud`, `validate`, `lookup`, `codegen`, `crudtest` package-уудын **нэг эх сурвалж** юм.
Хэрэгжүүлэлт энэ гэрээнээс зөрвөл баримтыг эхэлж засна.

Зорилго нь Lambda-ийн schema-driven CRUD санааг (form/grid schema → endpoint, relation-validated select, hook,
subform) **typed, тесттэй, fail-closed** хэлбэрээр хэрэгжүүлэх явдал. Ингэснээр P4-д 120 form + 101 grid-ийг
`useKvCrud`/KvCrudPage-д native REST болгон хурдан шилжүүлнэ. Pilot нь
`main/modules/organization/conference/confcore`: нэг resource-д ~400 мөр гар parse/validate код зарцуулдаг
бөгөөд `err.Error()`-ийг хэрэглэгчид шууд буцаадаг. `crud` үүнийг ~40 мөр declarative тодорхойлолтоор орлоно.

## 1. Wire гэрээ (front_end `useKvCrud`-тэй ИЖИЛ)

Resource `Endpoint` (ж: `/api/organization/conference/rooms`) дээр mount хийгдэх route-ууд. Бүртгэлийн
дараалал: статик route-ууд нь `/:id`-ээс ӨМНӨ.

| Method | Path | Тайлбар |
|---|---|---|
| GET | `/` | Жагсаалт: `{data,total,page,limit,totals?,filterable,can}` — `query` package-ийн `RequestFrom`/`RunRequest`-ийг дахин ашиглана (`ActionRead`) |
| POST | `/query` | Жагсаалт, JSON body (`ActionRead`) |
| GET | `/export.xlsx` | `Export` өгсөн бол (`ActionRead`) |
| POST | `/bulk` | `Bulk` өгсөн бол. Эрх нь үйлдлээр: `delete` → `Can(ActionDelete)`, бусад (`update` ба үл мэдэгдэх) → `Can(ActionUpdate)`. RLS үед `tenant.Tx` дотор |
| GET | `/meta` | `Meta` + `can` — `ExposeMeta: true` үед л (`ActionRead`) |
| GET | `/options/:field` | Ref талбарын сонголт (`lookup`, `ActionRead`): `?search=&parent=&ids=a,b&limit=` → `{data:[{value,label,parent_value?}]}`. `:field` = талбарын нэр, child-ийн хувьд `<child>.<field>` (ж: `lines.item_id`) |
| GET | `/:id` | Дэлгэрэнгүй: мөр + children массив (child бүр `[]`-ээс эхлэн, `Order`-оор) |
| POST | `/` | Үүсгэх → **201** + дэлгэрэнгүй мөр |
| PUT | `/:id` | Засах (хэсэгчилсэн — body-д байгаа зарласан талбар л) → 200 + дэлгэрэнгүй мөр |
| DELETE | `/:id` | Устгах (soft эсвэл hard) → 200 `{"deleted": "<id>"}` (id үргэлж мөр) |

`query.Resource.Mount`-ийг ШУУД ашиглахгүй: тэр нь (a) read gate-гүй, (b) 500 үед `err.Error()`-ийг body-д бичдэг
(DB-ийн дотоод мэдээлэл алдагдана), (c) bulk-д үйлдэл тус бүрийн эрхгүй. Тиймээс crud нь `query`-ийн экспортлогдсон
функцүүдийг (`RequestFrom`, `RunRequest`, `ExportXLSX`, `ParseBulk`, `ExecBulk`) өөрийн gate/алдааны map-тайгаар дуудна.

Хариуны хэлбэр (detail): мөр нь `Field.Name` түлхүүртэй (Column ≠ Name үед Name), `Password` баганыг ХЭЗЭЭ Ч буцаахгүй,
Date → `"YYYY-MM-DD"`, JSON → түүхий JSON, Money/Number → яг цифртэй JSON тоо (float64-аар алдагдахгүй). Жагсаалтын мөр
(`T`/map) нь `ListQuery`-ийн SELECT-ээр тодорхойлогдоно; `T` нь map үед Money/Number баганыг id-гаар нь яг decimal-аар
дахин уншиж давхарлана (мөрөнд id байх ёстой).

**Алдааны хэлбэр** (front_end `serverErrors.ts` уншина):

| Status | Body | Хэзээ |
|---|---|---|
| 400 | `{"error":"..."}` | JSON буруу, id-ийн формат буруу, body хэт том |
| 401 | `{"error":"Зөвшөөрөлгүй"}` | org/JWT алга (`tenant.Require`) |
| 403 | `{"error":"Хандах эрхгүй байна"}` | `Can(c, action) == false` |
| 404 | `{"error":"<Label> олдсонгүй"}` | Мөр байхгүй ЭСВЭЛ өөр org-ийнх (ялгахгүй — мэдээлэл алдагдуулахгүй) |
| 409 | `{"error":"..."}` | Version зөрсөн, hook-ийн `Conflict(...)`, unique constraint (23505), FK (23503), deadlock/lock timeout. Constraint-ийн нэр/текст хэзээ ч body-д орохгүй |
| 422 | `{"error":"Мэдээлэл буруу","errors":{"field":["msg"]}}` | Талбарын шалгалт, ref-ийн org, unique, `version` байхгүй, children. Child талбарын түлхүүр: `lines.0.qty` (UForm нэршил). DB-ийн хэт урт/хэт том утга (22001/22003) → 422 `{"error":"Утга хэт урт эсвэл хэт том байна"}` |
| 500 | `{"error":"Дотоод алдаа гарлаа"}` | Бусад. **Жинхэнэ алдааг хэзээ ч body-д бичихгүй** — `obs.From(ctx)`-оор логлоно |

## 2. Аюулгүй байдлын инвариантууд (тест бүрээр батлагдана)

1. **org_id зөвхөн JWT-ээс** (`Org`, анхдагч `tenant.Require`). Body-ийн `org_id` үл тоогдоно. Бүх SELECT,
   UPDATE, DELETE нь `<table>.org_id = ?` нөхцөлтэй. INSERT нь `org_id`-ийг үргэлж серверээс тавина.
2. **Write allowlist**: зөвхөн `Fields`-д зарласан талбар бичигдэнэ. Хамгаалагдсан баганууд (`id`, `org_id`,
   `created_at`, `updated_at`, `deleted_at`, `created_by`, `updated_by`, version багана) нь Field болж зарлагдах
   боломжгүй (Mount алдаа). Body-д байгаа зарлаагүй түлхүүрийг анхдагчаар үл тоомсорлоно (хуучин формууд бүх
   state-ээ илгээдэг). `StrictUnknown: true` үед 422 буцаана.
3. **Relation-validated ref**: `Ref` талбарын утга нь `lookup.Spec`-ийн нөхцөлөөр (тухайн org, `Where`) олдох
   ёстой. Олдохгүй бол 422 (өөр org-ийн id-г зааж холбох боломжгүй).
4. **Can fail-closed**: `Can` nil бол Mount алдаа өгнө. Унших үйлдэлд `ActionRead`, бичихэд
   `ActionCreate/Update/Delete`.
5. **404 = байхгүй ∪ өөр org** — ялгаварлахгүй.
6. **Идентификаторууд** (Table, Alias, Column) Mount үед regex-ээр шалгагдана (`^[a-z_][a-z0-9_]*$`,
   table нь `schema.name`). SQL-д хэрэглэгчийн өгөгдөл зөвхөн parameter-ээр орно.
7. **Body хэмжээ**: `MaxBody` (анхдагч 1 MiB) → 400.
8. **Транзакц**: create/update/delete + children + hook + event нь НЭГ транзакцад. Update/Delete нь хуучин мөрийг
   `SELECT ... FOR UPDATE`-ээр түгжинэ. `RLS: true` үед БҮХ зам (write, GET `/:id`, жагсаалт, export, options, bulk)
   `tenant.Tx` дотор ажиллаж `app.org_id` тавигдана — strict policy-тэй, superuser биш role-оор тестлэгдсэн.
   Bulk нь энгийн update-тэй ижил дүрэмтэй: зөвхөн тухайн org-ийн мөрийг түгжиж, `updated_at/updated_by`, `version+1`,
   мөр бүрт `<Name>.updated|deleted` event.
9. **Орчны дахин хамгаалалт**: `ListQuery`/`DetailQuery` нь custom байсан ч crud `Alias.org_id = ?` (+ `Alias.deleted_at IS NULL`)
   нөхцөлийг ҮРГЭЛЖ нэмнэ. Custom query хүснэгтээ `Alias`-аар нэрлэх ёстой.
10. **Хариуны нууц**: `Password` төрлийн багана detail/children/event-ээс хасагдана. Password талбартай resource нь
    hash-ийг сонгохгүй `ListQuery` заавал өгнө (үгүй бол Mount алдаа) — анхдагч `SELECT *` hash-ийг жагсаалтад гаргана.
11. **Bulk-аар validation тойрохгүй**: `Bulk.Fields` дахь багана нь `Ref`/`Unique`/`Password` талбар эсвэл `Version` багана бол Mount алдаа.
    `Bulk.Table/IDCol/SoftDelete/IntIDs` нь resource-той таарах ёстой (хоосон бол resource-ээс авна).
12. **Tenant Guard нийцтэй**: бүх write нь GORM builder (`Create(map)`, `Where("org_id = ? ...").Updates/Delete`) тул
    `tenant.Guard` (Strict) crud-ийн query бүрийг хянаж чадна (тест strict Guard-тай ажилладаг).

## 3. Go API

```go
package crud

type Action string // "create" | "read" | "update" | "delete"

type Resource[T any] struct {
    Name     string // resource түлхүүр, ж: "organization.conference_room" (event topic, лог, meta)
    Label    string // Монгол нэр: "Хурлын өрөө" (404 мессеж, codegen)
    Endpoint string // ж: "/api/organization/conference/rooms" (codegen/meta; Mount-д заавал биш)
    Table    string // "organization.conference_room" (schema.name заавал)
    Alias    string // жагсаалтын alias, анхдагч "t"

    ID     IDSpec  // {Column:"id", Kind: IDUUID|IDInt, Generate: nil=crypto/rand UUIDv4 (IDUUID) / DB default + RETURNING (IDInt)}
    Fields []Field // бичигдэх талбарууд (allowlist)

    List        query.Spec                              // Search/Sort/Filters/Totals/DefaultOrder
    ListQuery   func(db *gorm.DB, orgID string) *gorm.DB // nil → db.Table(Table+" "+Alias) (org_id, deleted_at нөхцлийг crud нэмнэ). List.DefaultOrder хоосон бол Alias.id ASC
    DetailQuery func(db *gorm.DB, orgID string) *gorm.DB // nil → ListQuery. Detail = DetailQuery + Where(Alias.id = ?)
    Export      *query.ExportSpec
    Bulk        *query.BulkSpec

    Children []Child // subform (ж: invoice → lines)

    Can      func(c fiber.Ctx, a Action) bool // ЗААВАЛ
    Validate func(x *Ctx, v Values) error     // cross-field; validate.Errors буцаавал 422. Талбарын алдаа байхгүй үед, write транзакц
                                                // дотор (update-д мөр түгжигдсэн, x.Old байна), x.Tx = nil. Update-д v нь зөвхөн ирсэн талбар
    Hooks    Hooks

    SoftDelete    bool   // deleted_at = now() (жагсаалт/detail/update-д deleted_at IS NULL автоматаар)
    Version       string // optimistic lock багана (ж: "version"); create-д 1; PUT body-д `version` (бүхэл) заавал (алга → 422), зөрвөл 409; update бүр +1
    Timestamps    *bool  // nil=true: created_at/updated_at = now() (DB цаг). Children хүснэгтэд ч ижил. crud.Off()/crud.On()
    TrackUser     bool   // created_by/updated_by = auth Principal.ID (хоосон бол NULL)
    RLS           bool   // транзакцад tenant.WithOrg
    Events        bool   // "<Name>.created|updated|deleted" event-ийг events.Publish-ээр (нэг транзакцад)
    StrictUnknown bool   // зарлаагүй ТҮЛХҮҮР бүрт 422 (id, org_id, created_at... зэрэг хамгаалагдсан түлхүүр ч). Зарласан талбар, children, Version зөвшөөрөгдөнө
    MaxBody       int    // анхдагч 1 MiB; create/update body-д
    ExposeMeta    bool
    Cache         *crud.CacheSpec // nil → унтраалттай. GET /, POST /query, GET /:id, GET /options/:field-ийн хариуны cache + ETag/304;
                                  // бичилт commit болсны дараа bump. Гэрээ: docs/CACHE.md §4

    DB  func() *gorm.DB                  // ЗААВАЛ
    Org func(c fiber.Ctx) (string, bool) // nil → tenant.Require
    NewRows func() any                   // жагсаалтын scan dest (nil → *[]T, T=map бол map)
}

func (r *Resource[T]) Mount(router fiber.Router) error // тохиргоо буруу бол error (panic биш)
func (r *Resource[T]) Check() error                    // Mount-гүйгээр тохиргоог шалгана (DB, Can шаардахгүй) — codegen/тест
func (r *Resource[T]) Meta() Meta                      // codegen/meta-д; DB, Can шаардахгүй; буруу тохиргоонд зөвхөн Name/Label/Endpoint/Table

type Field struct {
    Name   string // JSON түлхүүр
    Column string // nil → Name
    Label  string // Монгол шошго (алдааны мессеж, codegen)
    Kind   validate.Kind
    Rules  validate.Rules // Required, Nullable, MinLen, MaxLen, Min, Max, Pattern, Enum, Scale, Trim...
    Default    any  // create үед body-д байхгүй бол
    CreateOnly bool // update үед үл тоомсорлоно
    Ref    *lookup.Spec // FK: утга нь энэ spec-ээр (тухайн org-д) олдох ёстой; GET /options/<Name>
    Unique bool         // тухайн org дотор давтагдахгүй (soft-deleted-ийг тооцохгүй) → 422
    Input  string       // front_end KvField төрлийн override (ж: "employee"); хоосон бол Kind-аас
}

type Child struct {
    Name    string  // JSON түлхүүр, ж: "lines"
    Label   string
    Table   string  // org_id-тай хүснэгт
    FK      string  // эцгийн id-г заах багана, ж: "invoice_id"
    ID      IDSpec
    Fields  []Field
    Order   string  // эрэмбийн багана (хоосон → ID); detail-д `ORDER BY Order, ID`
    MaxRows int     // анхдагч 500 → 422
    SoftDelete bool
}
// Children-ийн update стратеги (lambda subform-тай ижил утга): body-д ирсэн массив = бүрэн жагсаалт.
// id-тай мөр → (тухайн эцэг+org-д байгаа бол, үгүй бол 422 `lines.i.id`) update (хэсэгчилсэн); id-гүй → insert;
// DB-д байгаа боловч массивт ороогүй → delete (SoftDelete бол deleted_at). Массив body-д огт байхгүй (key алга ЭСВЭЛ null)
// бол children өөрчлөгдөхгүй; `[]` бол бүгдийг устгана. Create-д мөрийн id үл тооно. Эцгийг hard delete хийхэд
// бүх child мөр (soft эсэхээс үл хамааран) эхлээд устна; soft delete-д child хөндөгдөхгүй.
// Хязгаарлалт: child ID зөвхөн UUID; child талбарт `Unique` дэмжигдэхгүй (Mount алдаа); nested children байхгүй.

type Ctx struct {
    Fiber    fiber.Ctx
    Tx       *gorm.DB // write транзакц (Validate-д nil)
    OrgID    string
    UserID   string
    Action   Action
    ID       string         // update/delete
    Old      map[string]any // update/delete: түгжсэн хуучин мөр
    Resource string
}

type Values map[string]any // багана → утга (parse+normalize хийгдсэн)

type Hooks struct {
    BeforeCreate, AfterCreate func(x *Ctx, v Values) error
    BeforeUpdate, AfterUpdate func(x *Ctx, v Values) error
    BeforeDelete, AfterDelete func(x *Ctx) error
}
// Hook алдаа: validate.Errors → 422; *crud.Error (Conflict/Forbidden/BadRequest/NotFound) → тухайн status;
// бусад (gorm.ErrRecordNotFound ч) → 500 (логлоно). Before* нь Values-ийг өөрчилж болно (ж: дугаар олгох); Values нь
// зөвхөн бизнес баганууд (org_id/id/timestamps/version-ийг сервер тавина, hook дарж чадахгүй).
// Дараалал (нэг tx): [update: lock + version] → Validate → ref/unique/children шалгалт → Before* → write → children →
// After* → event → detail унших. Аль нэг алдаа бүгдийг rollback хийнэ (event ч).

func Conflict(msg string) error; func Forbidden(msg string) error; func BadRequest(msg string) error; func NotFound(msg string) error
```

### Meta (codegen / GET /meta)

```go
type Meta struct {
    Name, Label, Endpoint, Table string
    ID         IDMeta        // {Column, Kind}
    Version    string        // optimistic lock багана ("" = байхгүй)
    SoftDelete bool
    Fields     []FieldMeta   // Name, Label, Kind, Input, Required, Nullable, CreateOnly, Unique, MinLen, MaxLen (бодит), Min, Max,
                             // Scale (Number/Money), Pattern, PatternMsg, Enum, Default, Ref *RefMeta{Options, Parent}
    Children   []ChildMeta   // Name, Label, MaxRows, Fields
    Filterable map[string]query.FilterKind // List.Filters
    Sort       []string      // List.Sort түлхүүрүүд (эрэмблэсэн)
    Search     bool
    Totals     []string
    Export, Bulk bool; BulkFields []string; BulkDelete bool
    StrictUnknown bool
}
```

`RefMeta.Options` = `Endpoint + "/options/" + <key>` (key = талбарын нэр, child-д `<child>.<field>`). Meta нь JSON-д тогтвортой
(slice-ууд эрэмблэгдсэн, `null` биш хоосон массив), DB/Can-гүйгээр бүтнэ.

### Field-ийн нарийн дүрэм

- `Name`, `Column`: `^[a-z_][a-z0-9_]*$`; `Name`/`Column` хамгаалагдсан нэр, ID/Version/FK багана байж болохгүй; давхцахгүй.
- `Ref` бүхий талбарын `Kind` нь UUID/Int/String; `Ref.Kind` хоосон бол `Field.Kind`-ээр автоматаар дүүрнэ (зөрвөл Mount алдаа).
- `Default` нь Mount үед `Rules`-аар шалгагдаж normalize хийгдэнэ (буруу бол Mount алдаа); create-д талбар ирээгүй
  (эсвэл null/"" ирсэн боловч Nullable биш, String/Text биш) үед хэрэглэгдэнэ. `Password`-д Default боломжгүй.
- Unique: `org_id = ? AND col = ? [AND id <> self] [AND deleted_at IS NULL]` шалгалт (422) + DB-ийн unique index-ийн
  уралдааны 23505 → 409. NULL утга давхардал биш.

## 4. validate

```go
type Kind string // String Text Int Number Money Bool Date DateTime Time UUID Enum JSON Password Phone Email

type Rules struct {
    Required bool   // create үед заавал (update үед ирсэн бол хоосон байж болохгүй)
    Nullable bool   // null/"" → NULL зөвшөөрөх
    Trim     *bool  // String/Text: nil=true
    MinLen, MaxLen int // rune-ээр; String анхдагч MaxLen 255, Text 10000
    Min, Max *float64
    Scale    int     // Number: бутархайн дээд орон; Money үргэлж 2
    Pattern  *regexp.Regexp
    PatternMsg string
    Enum     []string
}

func Parse(kind Kind, raw json.RawMessage, r Rules, label string) (value any, present bool, msg string)
func Bound(f float64) *float64 // Rules.Min/Max-д
func IsUUID(s string) bool
func (r Rules) Check(k Kind) error // програмчлалын алдаа (Mount)
```

**`present` / хоосон утгын дүрэм** (`Parse`):

| raw | үр дүн |
|---|---|
| хоосон (түлхүүр алга) | `(nil, false, "")` — Required-ийг create-д caller шалгана |
| `null` / `""` (Password: зөвхөн `""`; String/Text: Trim-ийн дараа) | `Nullable` → `(nil, true)` = NULL; `Required` → алдаа `"<Label> заавал"`; String/Text → `("", true)`; бусад → `(nil, false)` = "илгээгээгүй" (create-д Default/DB default, update-д өөрчлөхгүй) |
| бусад | төрлөөр задлана; алдаа бол `msg` |

`Min/Max` нь Int/Number/Money-д (бутархай хязгаарыг `0.1` мэт яг decimal-аар харьцуулна, float-ийн алдаагүй). `Number`-ийн
`Scale` 0 бол 6 (дээд 18), бүхэл хэсэг ≤ 18 орон; Money бүхэл хэсэг ≤ 16 орон. `Enum` нь Enum болон String-д.
`JSON` дахь `""` нь хүчинтэй JSON мөр (хоосон биш). Тоон мөрийн тэргүүн тэг (`"07"`) зөвшөөрнө, `+5`, `.5`, `5.` үгүй.
`-0` → `0`/`"0.00"`. Phone: Монгол 8 орон (`+976` хасагдана) эсвэл E.164 `+…`. Password: trim хийхгүй, ≥ 8, ≤ 72 байт.

- **Money**: JSON тоо эсвэл тоон мөр → `string` ("1234.50", 2 оронтой), |x| < 10^16 (numeric(18,2)),
  бутархай 2-оос их бол алдаа. Float-аар дамжуулахгүй.
- **Number**: Scale-аас их бутархай бол алдаа → `string` (decimal, float биш).
- **Int**: JSON бүхэл тоо эсвэл бүхэл тоон мөр → `int64`. 1.5 эсвэл 1e400 бол алдаа.
- **Bool**: зөвхөн JSON bool (хуучин формын 0/1-ийг зөвшөөрнө).
- **Date**: `YYYY-MM-DD` → `string` (огноо хэвээр, timezone хөрвүүлэхгүй). **DateTime**: RFC3339 → `time.Time`.
- **UUID**: каноник 8-4-4-4-12 → жижиг үсэг. **Enum**: `Enum`-д байх ёстой.
- **JSON**: дурын JSON → `json.RawMessage` (дээд 256 KiB). **Password**: MinLen 8 → bcrypt hash (`auth.HashPassword`).
- **Phone**: Монгол дугаар (8 орон, + олон улсын); **Email**: энгийн шалгалт, жижиг үсэг.

```go
type Errors map[string][]string
func NewErrors() Errors
func (e Errors) Add(field, msg string)    // ижил мессежийг давхардуулахгүй
func (e Errors) Merge(o Errors); func (e Errors) Len() int
func (e Errors) Err() error               // хоосон бол nil
func (e Errors) Error() string            // тогтвортой (талбараар эрэмблэсэн)
func Write(c fiber.Ctx, err error) error  // (wrap хийгдсэн ч) validate.Errors → 422 envelope; бусад алдааг өөрчлөхгүй буцаана
```

## 5. lookup

```go
type Spec struct {
    Table  string   // "organization.employee"
    Value  string   // "id"
    Label  []string // ["last_name","first_name"] → concat_ws(' ', ...)
    Search []string // ILIKE баганууд (CAST(col AS text) … OR …). nil → Label-ийн нэгдсэн мөр (concat_ws) — "Овог Нэр" гэж хайж болно
    Parent string   // optional parent багана → parent_value, `?parent=` (CAST(parent AS text) = ?)
    Where  string   // тогтмол нөхцөл — доорх grammar-аар шалгагдана; ж: "status = 'active'"
    Tenant *bool    // nil=true: org_id = ? нөхцөл (lookup.TenantOff()). Tenant:false үед ч Handler нэвтрэлт (org) шаардана
    SoftDelete bool // deleted_at IS NULL
    Order  string   // "col [asc|desc], ..." (ident л); анхдагч Label[0]; үргэлж Value-аар tie-break
    Limit  int      // `?limit` өгөөгүй үеийн хэмжээ, анхдагч 50; `?limit` дээд 200
    Kind   validate.Kind // Value баганын төрөл: UUID | Int | (хоосон=String). ids/Exists-ийн утгыг үүгээр задална
}
func (s Spec) Validate() error // Table/багана/Order/Where/Limit/Kind — crud.Mount дууддаг
func ValidateWhere(where string) error
func (s Spec) Handler(db func() *gorm.DB, org func(fiber.Ctx) (string, bool)) fiber.Handler
func (s Spec) Exists(tx *gorm.DB, orgID string, values []any) (missing []any, err error) // relation validation
type Option struct{ Value any; Label string; ParentValue any } // JSON: value,label,parent_value(omitempty)
```

- `?ids=a,b` (дээд 200, давхардал/буруу формат нь чимээгүй хасагдана) нь `search`/`parent`-ийг үл тооно — form-ийн анхны label-д.
- `?search=` дахь `%`, `_`, `\` нь литерал (escape хийгдсэн); дээд 100 rune. `?parent=` нь Parent тохируулаагүй Spec-д 400.
- `Exists`: утгуудыг `Kind`-аар задална (буруу формат → DB-д очихгүй "олдсонгүй"), nil-г алгасна, 1000-аар chunk хийнэ,
  оролтын дарааллаар давхардалгүй `missing` буцаана. org хоосон (Tenant үед) → алдаа (fail-closed).
- **Where grammar** (`ValidateWhere`): `expr := and (OR and)*`, `and := unary (AND unary)*`, `unary := NOT unary | '(' expr ')' | ident cond`,
  `cond := (= | <> | != | < | <= | > | >=) literal | IS [NOT] NULL | [NOT] IN '(' literal {, literal} ')'`,
  `literal := 'мөр' | тоо | TRUE | FALSE`. ident нь жижиг үсэгтэй энгийн нэр. `;`, `--`, `/*`, функц, subquery, cast, `?`, `\`,
  багана=багана, LIKE, дээд 500 тэмдэгт/120 token/8 гүн/IN 50-г зөвшөөрөхгүй.
- DB алдаа 500 + `{"error":"Дотоод алдаа гарлаа"}`; жинхэнэ алдаа `obs` логт.

## 6. codegen (front_end-д)

```go
codegen.TS(metas []crud.Meta, opts codegen.TSOptions) ([]byte, error)   // тогтвортой, `// Code generated by khan codegen. DO NOT EDIT.`
codegen.OpenAPI(metas []crud.Meta, info codegen.Info) ([]byte, error)   // OpenAPI 3.1 JSON
codegen.WriteFile(path string, data []byte) error                       // өөрчлөлтгүй бол файлыг хөндөхгүй (түр файл + rename)
```

`khan` CLI нь апп-ийн resource-ийг импортлож чадахгүй тул `khan gen` БАЙХГҮЙ. Апп өөрийн `cmd/kvgen/main.go`-той байна:

```go
// cmd/kvgen/main.go — `go run ./cmd/kvgen` (Resource.Meta() нь DB/Can шаардахгүй)
func main() {
    metas := []crud.Meta{rooms.Meta(), invoices.Meta()} // resource-уудаа импортолно
    ts, err := codegen.TS(metas, codegen.TSOptions{Names: map[string]string{"finance.item": "FinanceItem"}})
    if err != nil { log.Fatal(err) }
    if err := codegen.WriteFile("front_end/src/generated/crud.ts", ts); err != nil { log.Fatal(err) }
    api, err := codegen.OpenAPI(metas, codegen.Info{Title: "Khan ERP", Version: "1.0.0", MetaRoutes: true})
    if err != nil { log.Fatal(err) }
    if err := codegen.WriteFile("docs/openapi.json", api); err != nil { log.Fatal(err) }
}
```

Гаралтыг git-д commit хийнэ; CI-д `go run ./cmd/kvgen && git diff --exit-code` гэж drift шалгана.

**TS (resource бүрд; identifier = `Name`-ийн сүүлийн сегмент, `conference_room` → `ConferenceRoom`/`conferenceRoom`; resource-ууд нэрээр эрэмблэгдэнэ):**

- `export interface <P>Payload` — create хүсэлт (Required бөгөөд Default-гүй бол заавал, үгүй бол `?`; Nullable → `| null`; child → `lines?: <P>LinesPayload[]`);
  `<P>UpdatePayload = Partial<Omit<<P>Payload, CreateOnly…>> & { version: number }`
- `export interface <P>Row` — GET `/:id` хариу: `id` (Column нэрээр), `org_id?`, `version`, зарласан талбарууд (Required биш бол `| null`; Password БАЙХГҮЙ), children (`<P>LinesRow[]`), timestamps/by `?`
- `export const <p>Schema = z.object({...})` (zod v4; `StrictUnknown` бол `z.strictObject`), `<p>UpdateSchema` (`.partial().omit(createOnly).extend({version})`), child бүрд `<p><Child>Schema`
- `export const <p>Fields: KvFieldDef[]` (name, label, type, required, rules, options (Enum), `load` (Ref)), child бүрд `<p><Child>Fields`
- `export const <p>Endpoint`, `<p>Filterable` (`as const`), `<p>Defaults` (Field.Default)
- Нэр давхцвал (resource хоорондоо, эсвэл resource ба child) **алдаа**; `TSOptions.Names` (resource Name → Pascal) -ээр ялгана. Import замууд `TSOptions.ZodImport/AxiosImport/KvFieldImport/AntRuleImport`.

Kind → KvField type: String/UUID/Email→text, Text/JSON→textarea, Int/Number→number, **Money→amount**, Bool→switch, Date→date,
DateTime→datetime, Time→time, Enum→select (`options`), Phone→phone, Password→password, Ref→select + `load`. `Input` override төрлийг дарна;
`Input` нь select-төрлийн биш (ж: `employee`) Ref-д `load` үүсгэхгүй (domain editor өөрийн эх сурвалжтай). Ref-ийн `load`:
`axios.get('<Meta.Ref.Options>', {params:{search}})` → `res.data.data` (`{value,label,parent_value}`); org-ийг сервер JWT-ээр хязгаарлана.

**Decimal (Money/Number) — шийдвэр:** сервер хариунд яг цифртэй JSON ТОО буцаадаг, хүсэлтэд JSON тоо ЭСВЭЛ тоон мөр хүлээн авдаг. Иймээс
`Row` → `number`, `Payload` → `number | string`; zod нь `z.union([z.number(), z.string()])` дээр `superRefine`-аар тоон хэлбэр
(`^-?\d+(\.\d+)?$`), бутархайн орон (Money ≤ 2, Number ≤ Scale), `Min/Max`-ийг шалгана (бутархайн оронг МӨРӨӨР тоолно). KvForm-ийн
`rules` нь ижил шалгалтыг `validator` дүрмээр өгнө (ant `min/max` мөрөнд урт гэж тооцогддог тул ашиглахгүй).
**Нарийвчлал:** JS `number` ~15-16 мэдэгдэхүйц цифрээс хэтэрвэл алддаг (`numeric(18,2)`-ийн дээд 9999999999999999.99); том дүнг
ЗААВАЛ тоон мөрөөр илгээ, хариуг уншихдаа нарийвчлал чухал бол raw текстээр задал. JSON талбар: Required бол `z.custom` (z.unknown нь
дутуу түлхүүрийг шалгадаггүй), `meta: unknown`.

Мэдэгдэж буй хязгаар: Go (RE2) pattern-ийг JS RegExp болгоно (`(?i)` flag, `(?P<` хөрвүүлнэ; `\A \z \Q \C [[:class:]]` → алдаа);
`Ref.Parent` (cascade) нь `KvFieldDef`-д илэрхийлэгдэхгүй; child `id` багана `"id"` гэж үзнэ (`ChildMeta` нэр өгдөггүй).

**OpenAPI 3.1:** resource бүрийн route бүр (`GET|POST <ep>`, `POST /query`, `GET /export.xlsx` (Export), `POST /bulk` (Bulk),
`GET /options/{field}` (Ref байвал; `field` enum), `GET|PUT|DELETE /{id}`, `GET /meta` — `Info.MetaRoutes`), schema (`<P>`, `<P>Create`,
`<P>Update`, `<P>List`, child), `components.responses` (400/401/403/404/409/422/500 — §1-ийн envelope), `bearerAuth`. `crud.Meta` нь `ExposeMeta`-г
агуулдаггүй тул `/meta` нь `Info.MetaRoutes`-оор БҮХ resource-д нэг дор. Нэр давхцвал `Info.Names`.

## 7. crudtest — tenant leak test generator

```go
crudtest.Isolation(t, crudtest.Config{
    App: app, Meta: rooms.Meta(), Secret: testSecret,             // эсвэл Token func(org,user) string, эсвэл Auth (HeaderAuth + ClaimsMiddleware)
    Sample:      func(org string) map[string]any { ... },         // хүчинтэй create payload; unique талбар дуудлага бүрт ШИНЭ
    RefSample:   map[string]func(org string) string{"owner_id": seedPerson},          // child: "lines.item_id"
    ChildSample: map[string]func(org string) map[string]any{"lines": lineFor},
})
```

Хоёр org (A, B; анхдагч санамсаргүй UUID) үүсгэнэ. A-ийн token-оор мөр үүсгээд, B-ийн token-оор subtest бүрээр баталгаажуулна:
- `list_excludes_other_org` — `GET /` ба `POST /query`-д A-ийн мөр алга (B өөрийнхөө мөрийг харна — хоосон дамжилт биш);
- `get_other_org_404`, `update_other_org_404`, `delete_other_org_404` — 404 БА A-ийн мөр байт-байтаар өөрчлөгдөөгүй;
- `bulk_other_org_affects_nothing` (Meta.Bulk) — delete/update хоёулаа `affected: 0`;
- `create_ignores_body_org_id` — body `org_id`/`id` = A-ийнх ч B-д хадгалагдана, A харахгүй;
- `ref_cross_org_422` / `options_never_leak` (Ref байвал) — A-ийн id-тай create/update 422 (`errors`-д талбар), `/options/:field`-д A-ийн утга гарахгүй (`ids=` ч);
- `children_cross_org` (ChildSample) — B нь A-ийн child id-г өөрийн мөрөнд холбож/засаж чадахгүй (422), A-ийн мөр өөрчлөгдөхгүй;
- `unauthenticated_401` — token-гүй (мөн буруу Bearer) бүх route 401.

Generator-ийг өөрийг нь тестлэсэн: зөв in-memory endpoint-д алдаа 0, цоорхой бүр (list/get/update/delete/bulk/body org/ref/options/no-auth) өөрийн
subtest-ийг унагана (`crudtest.Check(T, cfg)` + recorder), мөн жинхэнэ `crud.Resource` (DB, strict Guard, JWT) дээр бүгд PASS.
Cleanup: үүсгэсэн мөрийг API-аар (DELETE) устгана (best effort).

## 8. Хэрэгжүүлэлтийн явцад гэрээнд оруулсан засвар (бүртгэл)

| # | Засвар | Шалтгаан |
|---|---|---|
| 1 | `Resource.Validate` (cross-field) талбартай нэр зөрчилдөх тул тохиргоо шалгах арга нь `Check()` | Go-д field ба method нэр давхцахгүй |
| 2 | `query.Resource.Mount`-ийг шууд ашиглахгүй, `query`-ийн экспортлогдсон функцийг crud-ийн gate/алдааны map-аар дуудна | `query.Fail` 500-д `err.Error()` бичдэг (DB-ийн дотоод мэдээлэл алдагдана), read gate болон bulk-ийн үйлдэл тус бүрийн эрх байхгүй |
| 3 | Cross-field `Validate` нь write транзакц дотор (мөр түгжигдсэний дараа, `x.Old`-той) ажиллана; `x.Tx` nil хэвээр | Partial update-д хуучин утгагүйгээр (ж: зөвхөн `end` ирэхэд `start`-тай харьцуулах) шалгах боломжгүй |
| 4 | Password талбартай resource-д `ListQuery` заавал; Bulk нь Ref/Unique/Password/Version баганыг засахыг хориглоно | Анхдагч `SELECT *` hash-ийг жагсаалтад гаргана; bulk нь ref/unique шалгалтыг тойрно |
| 5 | `lookup.Spec.Kind`, `Spec.Validate()`, `ValidateWhere`, `TenantOff()`; `Search` nil үед Label-ийн нэгдсэн мөрөөр хайна | uuid/int баганад буруу утга DB алдаа (500) болохоос сэргийлэх; "Овог Нэр" хайлт |
| 6 | `validate.Parse`-ийн present/хоосон утгын дүрэм тодорхой болсон (§4); `Min/Max` decimal-аар харьцуулна; `Bound`, `NewErrors/Merge/Len` | Спек "present"-ийг тодорхойлоогүй байсан; float харьцуулалт `0.1`-д алдаатай |
| 7 | Money/Number-ийг хариунд яг цифртэй JSON тоо болгож буцаана (supplemental `CAST(col AS text)` уншилт) | pgx numeric-г float64 болгодог — 9999999999999999.99 нь 1e16 болж алдагддаг байсан |
| 8 | Child алдааны түлхүүр `lines.0.qty`; child null → өөрчлөхгүй; child `Unique` дэмжигдэхгүй; child ID зөвхөн UUID | UForm нэршил; null-ээр санамсаргүй бүгдийг устгахаас сэргийлэх; unique-ийн хүрээ тодорхойгүй |
| 9 | `Resource.Check()`, `Meta` нь DB/Can-гүйгээр; `Meta.Ref.Options` URL; `crud.Off()/On()`; `NotFound(msg)` | codegen offline ажиллах ёстой |
| 10 | RLS: бүх зам `tenant.Tx` дотор (жагсаалт/export/options-ийг review-ийн дараа нэмсэн) | Strict policy-д `app.org_id`-гүй query хоосон гардаг |
| 11 | `gorm.ErrRecordNotFound` hook-оос 404 БИШ 500; hook 404 хүсвэл `crud.NotFound` | Hook доторх санамсаргүй `First()` алдааг "мөр олдсонгүй" гэж буруу ойлгуулахгүй |
| 12 | Bulk: түгжсэн id-д `updated_at/updated_by/version+1` + мөр бүрт event; жагсаалтын decimal overlay | Bulk нь optimistic lock болон outbox-ийг тойрдог байсан; жагсаалтад мөнгөн дүн float64 болдог байсан |
| 13 | Аюулгүй байдлын review (2026-11-06): `DefaultOrder`-ийг idCol тодорхой болсны дараа угсарна | Хоосон үед `t. ASC` болж жагсаалт 500 өгдөг байсан |
| 14 | `\u0000` (JSON), NUL (`search`/`parent`/текст шүүлт) → 4xx; IDInt-ийн id болон `lookup` Int утгыг `CAST(? AS bigint)`-ээр харьцуулна | Client-ийн өгөгдлөөр 500 (22P05/22021, int4 encode) үүсгэх боломжтой байсан |
| 15 | Before* hook-ийн дараа хамгаалагдсан баганыг (`org_id`, `deleted_at`, version, timestamps…) Values-аас хасна | Hook `org_id`/`deleted_at`-ийг дарж бичих боломжтой байсан |
| 16 | `GET /meta`-д `table` (DB-ийн жинхэнэ нэр) гарахгүй | Дотоод бүтэц ил гарахгүй |
| 17 | Hook/children-тэй resource-ийн bulk delete нь мөр бүрт `DELETE /:id`-ийн ижил дүрэм (`deleteOne`); hook/Validate-тэй resource-д bulk update-ийг Mount хориглоно | Bulk нь BeforeDelete («батлагдсан баримт устгахгүй»)/Validate-ийг тойрдог байсан |
| 18 | Child-д `Password` төрөл хориотой; Bulk.Fields-д хамгаалагдсан багана (`created_by` г.м.) хориотой | Нэг хүсэлтэд 500 bcrypt (DoS); audit баганыг bulk-аар засах |
| 19 | `Resource.Timeout` (анхдагч 30с, <0 хязгааргүй) — handler бүрийн context deadline | Удаан query DB-г хязгааргүй эзлэхгүй |
| 20 | `codegen`: `Meta` нь `ExposeMeta`, child id багана (`"id"` гэж үзнэ)-г агуулдаггүй; OpenAPI `/meta` нь `Info.MetaRoutes`-оор нэг дор | Meta-г өөрчлөхгүйгээр (crud-д хөндөхгүй) codegen/crudtest нэмсэн; хэрэгтэй бол `Meta.ExposeMeta`, `ChildMeta.IDColumn` нэмнэ |
| 21 | Decimal: Row `number`, Payload `number \| string` (zod union + scale/range шалгалт) | Хариу нь яг цифртэй JSON тоо, хүсэлт тоо эсвэл мөр (§6) |
| 22 | `Resource.Cache` (`CacheSpec`): унших 4 endpoint-ийн org-тусгаарлагдсан generation cache + ETag/304, бичилт commit-ийн дараа bump | GraphQL/apollo client cache-ийг орлоно; `can` cache-д ордоггүй, read эрх cache-ээс өмнө; 4xx/5xx cache-д ордоггүй; `ListQuery` нь хэрэглэгчээс хамаарсан бол Cache тавихгүй. [docs/CACHE.md](CACHE.md) |

**Unique-ийн шаардлага:** `Unique: true` талбар нь апп түвшний шалгалт (TOCTOU) тул DB-д заавал
`UNIQUE (org_id, col) [WHERE deleted_at IS NULL]` index байх ёстой — org_id-гүй global index бусад байгууллагад тухайн
утга байгааг 409-оор илчилнэ. **Options:** `/options/:field` нь resource-ийг унших эрхтэй хэрэглэгчид ref хүснэгтийн
label-ийг (тухайн org-ийнхыг л) харуулна — ref хүснэгтийн өөрийн эрхийг шалгахгүй.
