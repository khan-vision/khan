# access — модуль / role / capability эрхийн цөм

`github.com/khan-vision/khan/access` нь main-ийн `organization/permission/modulepermission`
(manifest → role → capability, `view_employee.permissions` JSON) загварыг цэвэр, тесттэй, global-гүй
болгон дахин бичсэн багц. Хуучин Lambda `krudMW` + `role_N.json` (request бүрт дискнээс уншдаг)-ийг
орлоно ([ARCHITECTURE.md](ARCHITECTURE.md) хүснэгт).

```go
reg := access.NewRegistry()
_ = reg.RegisterAll(finance.Manifest(), school.Manifest()) // модуль бүр өөрийн Manifest-ийг өгнө

src, _ := access.NewGormSource(gdb, access.GormConfig{
    Table:  "organization.employee",                       // эсвэл view_employee
    Filter: func(q *gorm.DB) *gorm.DB { return q.Where("status_id = 1") },
    Owner: &access.GormOwner{
        Table:  "organization.organization_users",
        Filter: func(q *gorm.DB) *gorm.DB { return q.Where("access = 'owner'") },
    },
})
pol, err := access.New(reg, src, access.WithTTL(5*time.Second))

api.Get("/invoices", pol.Require("finance", "invoices.view"), listInvoices)
Can: pol.CrudCan("finance", access.ActionCaps{Read: {"invoices.view"}, Create: {"invoices.create"}, ...})
api.Get("/permission/me", pol.MeHandler())
api.Get("/permission/modules", pol.ManifestHandler())
```

## 1. Загвар

| Ойлголт | Тайлбар |
|---|---|
| `Manifest{Key, Label, Roles[], Capabilities[]}` | Модулийн эрхийн тодорхойлолт. JSON хэлбэр нь main-ийн `ModuleManifest`-тэй ижил (`default_roles`) тул FE manifest UI өөрчлөлтгүй. |
| `Registry` | Manifest-ийн бүртгэл. **Global биш**; `New(reg, …)` дуудахад `Freeze` хийгдэнэ (compile хийсэн policy-тэй зөрөхгүй). |
| `Grants{Owner, Modules{key: ModuleGrant}}` | Нэг (org, user)-ийн эрх. |
| `ModuleGrant{Roles, Allow, Deny}` | Role-ууд (олон байж болно), role-оос гадна нэмэлт эрх, ХАСАХ эрх. |
| `Source` | `Load(ctx, org, user) (Grants, error)` — апп өөрийн хадгалалтаа залгана. Нэмэлт: `OrgConfigurer`. |
| `Policy` | Registry + Source-ийг compile хийж cache-тэй шийддэг. |

### Түлхүүрийн дүрэм (`Manifest.Validate`)
Модуль/role = `^[a-z][a-z0-9_]*$`, capability = `.`-ээр тусгаарласан ийм хэсгүүд (`enrollment.finance.edit`),
≤ 64 тэмдэгт. Давхардал, хоосон label, байхгүй `DefaultRoles` role, capability-гүй модуль → алдаа
(бүх зөрчил `errors.Join`). `*` wildcard байхгүй — нэг дор бүгдийг олгох нь ил (`Owner`) байна.

### Шийдвэрийн дараалал (`Policy.Check`)
1. org/user хоосон → `no_identity` (татгалзана).
2. Модуль manifest-д алга, эсвэл өгсөн capability-ийн **нэг ч** manifest-д алга → `unknown`.
   (Owner ч гэсэн: үсгийн алдаатай `Require` хэзээ ч нэвтрэхгүй.)
3. Source алдаа → `error`, **татгалзана** + лог (cache-д `ErrorTTL`, анхдагч 1с — унасан Source-ыг дэвслэхгүй).
4. `Owner` → manifest-ийн бүх capability (`OwnerPolicy`: `OwnerAll` анхдагч; `OwnerDeniable` — deny хэвээр хасна; `OwnerNone`).
5. Effective = (role-уудын `DefaultRoles` ∪ `Allow`) ∖ `Deny`. **Deny үргэлж allow-г дарна.** any-of: өгсөн cap-уудын нэг нь байхад хангалттай.
6. Шилжилтийн горим (асаасан модульд л) → §3.
7. Үгүй бол `no_grant`.

`Check` нь `Decision{Allowed, Reason, Capability}` буцаана (лог, тест, аудит-д шалтгаан).

## 2. Cache, invalidation

* `loader[V]` — TTL (`WithTTL`, анхдагч 5с), дээд хэмжээ (`WithMaxEntries` 50k; хугацаа дууссаныг, дараа нь санамсаргүй 10%-ийг хасна), **singleflight** (50 зэрэгцээ → 1 `Load`).
* `Invalidate(ctx, org, user)` / `InvalidateOrg(ctx, org)` — шууд. Нарийн тал: ачаалж байх үед invalidate болбол үр дүн cache-д **орохгүй**, мөн invalidate-ийн дараах дуудагч хуучин flight-д нэгдэхгүй (singleflight түлхүүрт epoch). Дуудагчийн ctx цуцлагдсан ч ачаалалт бусдад зориулж дуустал (timeout `WithLoadTimeout` 5с). Source panic → алдаа.
* **Олон instance:** `WithGenerations(g Generations, prefix, interval)`. `Generations` нь `cache.GenStore`-той ижил (`Gen`/`Bump`) тул `cache.MemoryGens` эсвэл `rediscache` шууд тохирно; Redis заавал биш. Entry бүр ачаалах үедээ `[org, user]` generation-ийг хадгалж, `interval` (анхдагч 1с) тутамд л (хүсэлт бүр биш) шалгана; зөрсөн, эсвэл уншигдахгүй бол Source-оос дахин ачаална (**fail-safe** — хуучин эрх үлдэхгүй). `Invalidate` → bump хийж, bump алдаа бол *локал аль хэдийн цэвэр* боловч алдааг буцаана (бусад instance TTL-ээр хүрнэ).
* Эрх өөрчлөх кодын дараа ЗААВАЛ `Invalidate` дуудна (main-ийн `InvalidateUser`).

## 3. Шилжилтийн горим (explicit opt-in)

main-ийн `RequireCapabilityOrUnconfigured` нь route бүр дээр далд fail-open байсан. Энд **модуль тус бүрд, Policy үүсгэхэд**:

```go
access.WithTransition("hr", access.TransitionOrg)   // org-д хэнд ч hr эрх тохируулаагүй бол нээлттэй
access.WithTransition("sale", access.TransitionUser) // тухайн хэрэглэгчид sale блок байхгүй бол нээлттэй
```
* Анхдагч = strict (`TransitionOff`). Тохируулмагц (хэн нэгэнд role/allow/**deny**) автоматаар хаагдана. Хоосон блок (`{"role":"","capabilities":[]}`) тохируулсанд тооцогдохгүй.
* **Сайжруулалт:** org-configured уншилт алдаатай бол main *fail-open* (нээсэн) байсан; энд **татгалзана**. `portal_only` token (сурагч/эцэг эх) нь transition-аар нэвтрэхгүй. Deny-only блок мөн "тохируулсан".
* `TransitionOrg` нь `Source`-оос `OrgConfigurer` шаардана (`New` шалгана). `Invalidate(org, user)` org-ийн "тохируулсан" cache-ийг бас цэвэрлэнэ.
* `Me` / `Snapshot` transition-ийг тусгахгүй (main-ийн `/me`-тэй адил — зөвхөн сервер талын gate).

## 4. HTTP

| API | Тайлбар |
|---|---|
| `Require(module, caps...)` | any-of middleware. **401** `{"error":"Зөвшөөрөлгүй"}` (JWT-д org/user алга) · **403** `{"error":"Хандах эрхгүй байна","module":"finance","capability":"invoices.post"}` (any-of бол `capability` = эхний cap + `"capabilities":[...]` бүгд) · **503** Source алдаа (түр зуурын — FE дахин оролдоно, «эрхгүй» гэж харуулахгүй) · **500** manifest-д байхгүй нэр (үсгийн алдаа; хэзээ ч нэвтрүүлэхгүй). |
| `Can(c, module, cap)`, `CanAny` | handler доторх шалгалт. |
| `CrudCan(module, ActionCaps{Read,Create,Update,Delete})` | `crud.Resource.Can`-д шууд. Хоосон/буруу үйлдэл = **татгалзана**. |
| `Me(c)`, `MeHandler()` | `{"is_owner":bool,"modules":{key:{role,roles,capabilities}}}` |
| `ManifestHandler()` | `[]Manifest` (нэвтэрсэн хэрэглэгчид) |
| `Check/Allowed/Snapshot(ctx, org, user…)` | HTTP-гүй (job, мэдэгдэл; main-ийн `UserHasCapability`). |
| `Verify(module, caps...)` | эхлэхэд route-ын нэрсийг шалгах. |
| `UsersWithCapability(ctx, org, module, cap)` | cap-тай хэрэглэгчдийн id (эрэмбэлсэн). §4.2. |
| `WithDenyHook(fn)`, `AuditDenials(db)`, `ReportDenied`, `LastDenial` | татгалзлын hook / audit. §4.3. |

Identity: org = `tenant.OrgID(c)` (JWT), user = `auth.User(c).ID`. `X-Org-ID`, `?org_id`-д **итгэхгүй** (тестэлсэн).
Нэг хүсэлтэд шийдвэр `c.Locals`-д (Policy тус бүрийн түлхүүр) нэг л удаа ачаалагдана — `crud`-ийн `canMap` дөрвөн удаа дуудахад Source нэг л удаа.

### 4.1 403 / 503 хариу (FE-д)
* 403 body-д `module`, `capability` (+ any-of бол `capabilities`) буцна: FE (`finance/services/api.ts`) мессеж зохиоход ашигладаг. Эдгээр нь нууц биш (manifest `ManifestHandler`-ээр нээлттэй). Бусдын өгөгдөл, олгосон эрхийн дэлгэрэнгүй ХЭЗЭЭ Ч орохгүй (тестэлсэн).
* `CrudCan`-ийн 403-ыг `crud` багц өөрөө бичдэг (зөвхөн `error`) — `module/capability` нэмэх бол crud талд өөрчлөлт хэрэгтэй (access-аас хийх боломжгүй).
* **503** = Source (DB) түр унасан: татгалзсан боловч *эрхгүй гэсэн үг биш*. FE `503`-д «эрхгүй» toast харуулалгүй түр зуурын алдаа гэж үзэж дахин оролдоно (`WithErrorTTL` 1с-ийн дараа Source дахин уншигдана).
* **Owner анхдагчаар deny-г үл тооно** (`OwnerAll`): эзэн өөрийгөө санамсаргүй түгжихгүй. Дотоод хяналт хэрэгтэй байгууллагад `WithOwnerPolicy(OwnerDeniable)` (deny owner-д ч үйлчилнэ) эсвэл `OwnerNone`.

### 4.2 UsersWithCapability
`UsersWithCapability(ctx, org, module, cap) ([]string, error)` — job/мэдэгдэлд (ж: «нэхэмжлэх батлах эрхтэй хүмүүст мэдэгдэх»). `Source` нь `Lister` (`ListUsers(ctx, org) ([]UserGrants, error)`) байх ёстой, үгүй бол `ErrNoLister`; `MemorySource`, `GormSource` хэрэгжүүлсэн (`GormSource`: Filter-тэй НЭГ query + Owner хүснэгт, user бүр нэг, олон мөр нэгдсэн). Owner нь `OwnerPolicy`-оор, deny хүндлэгдэнэ. **Шилжилтийн горим ҮЛЧЛЭХГҮЙ** — «хэнд ч тохируулаагүй тул бүгдэд нээлттэй» нь бодит эрх биш, мэдэгдэл бүх org-д очихоос сэргийлнэ (зөвхөн тодорхой олгосон эрх). Cache-гүй; эвдэрхий JSON-той нэг мөр бүх жагсаалтыг алдаатай болгоно (fail-closed). Үр дүн эрэмбэлэгдсэн, хоосон бол `[]`.

### 4.3 Татгалзлын hook ба audit
* `WithDenyHook(func(c fiber.Ctx, d Denial))` — `Require`-ийн 401/403/503 болон `ReportDenied(c, module, caps...)` бүрт дуудагдана. `Denial{Org, User, Module, Caps, Reason (no-grant|deny|source-error|unauthenticated), RequestID, Path, Method}`. Hook panic → recover + лог, хариу/статус хэзээ ч өөрчлөгдөхгүй. Мөн `LastDenial(c)`-ээр хүсэлт дотроос авч болно.
* `Can/CanAny/CrudCan/Me`-д hook **байхгүй**: `crud` нь `can` map-д 4 үйлдлийг бүгдийг шалгадаг тул false нь татгалзал биш (давхар/хуурамч бүртгэл). Crud-ийн 403-ыг бүртгэх бол өөрийн кодоос `ReportDenied` дууд.
* `AuditDenials(db)` — `audit.Entry{Action:"access_denied", Resource: module, Meta: audit.MetaFrom(c)}`-ийг тусдаа богино транзакцад (2с timeout) бичнэ; алдаа = зөвхөн лог. `audit.Entry` `RecordID` хоосныг хүлээж авдаггүй тул `RecordID = "<reason>:<cap1,cap2>"` (шалтгаан + cap хайхад ашигтай); `Changes` nil. **Rate-limit**: (org, user, module, caps) тус бүр минутад 1 мөр, санах ойн map ≤10k түлхүүр (дүүрвэл хугацаа дууссаныг, дараа нь бүгдийг цэвэрлэнэ). Org тодорхойгүй (unauthenticated) бол бичихгүй. Hook синхрон тул 403 хариу нэг DB бичилтээр (минутад 1) удаашрах боломжтой. Import cycle байхгүй (`audit` нь `access`-ийг импортлодоггүй).

```go
pol, _ := access.New(reg, src, access.WithDenyHook(access.AuditDenials(gdb)))
```

### `/me` хэлбэр ба main-тэй зөрүү
FE `usePermission.ts` нь `modules[key].role` (string) ба `modules[key].capabilities` (string[]) уншдаг; main `/me` нь
`{is_owner, modules}`. Ижил нэр хадгалсан (товч бичлэг `owner`/`caps` **биш**). Зорилготой өөрчлөлтүүд:
`roles[]` нэмэгдсэн (олон role); `capabilities` үргэлж эрэмбэлсэн, `null` биш; `modules` үргэлж `{}`; манифестэд байхгүй
role/cap, deny-д орсон cap-ууд гарахгүй; owner-д `role:"owner"` + бүх cap (main-тэй адил). Role-гүй, зөвхөн
`Allow`-той модуль `role:""` — FE-ийн `hasModule()` (role шаарддаг) үүнийг цэсэнд харуулахгүй (main-тэй адил).

## 5. Source-ууд

* `GormSource` — нэг хүснэгтийн JSON/JSONB/text баганаас. Table/багана нэр config-оос (regexp шалгаад GORM `clause.Column`-оор quote), утга үргэлж параметр, `org_id` нөхцөл үргэлж (хоосон org → `ErrNoOrg`, org-гүй query үүсгэхгүй). `Filter` scope-оор идэвхтэй/устаагүй мөр. Нэг user-т олон мөр (олон ажлын байр) → эрхүүд **нэгдэнэ**. Эвдэрхий JSON → алдаа (fail-closed). `Owner` → тусдаа хүснэгт. **JSON доторх `"owner":true`-д анхдагчаар итгэхгүй** (HR-ийн засдаг JSON-оор өөртөө owner олгох боломжийг хаана); `TrustJSONOwner` opt-in. `OrgConfigurer` хэрэгжсэн.
* `MemorySource` — тест/жижиг апп (`Set/Delete/SetError/Loads`).
* `Merge(sources...)` — нэгдэл (main-ийн *implicit grant*: callbot-ийн багийн гишүүн → `calls.dial`). Нэг нь алдаа өгвөл бүхэлдээ алдаа.
* `SourceFunc`.

Wire JSON (main-тэй нийцтэй, нэмэлттэй): `{"modules":{"finance":{"role":"accountant","roles":["viewer"],"capabilities":["x.y"],"allow":["z.w"],"deny":["invoices.delete"]}}}`.
Үл мэдэгдэх түлхүүр (хуучин `permissions` цэс) алгасагдана — ижил баганад зэрэгцэн оршиж болно.

## 6. main-ээс шилжих

| main | access |
|---|---|
| `modulepermission.Register` (init) | `Registry.Register` (explicit, шалгагддаг, global биш) |
| `HasCapability(c,m,cap)` / `RequireCapability` | `Policy.Can` / `Require` |
| `RequireAnyCapability(m)` (`HasAnyCapability`) | `Require(m, <модулийн бүх нээх cap>)` — хэрэгтэй бол `Snapshot.Modules[m]` хоосон эсэх |
| `RequireCapabilityOrUnconfigured` (route тус бүр, `PerUserTransition`) | `WithTransition(module, TransitionOrg/TransitionUser)` (Policy түвшинд, explicit) |
| `IsOwner` (JWT role 5 fallback) | `Grants.Owner` (Source). **JWT `role==5` fallback байхгүй** — owner зөвхөн Source-оос. Шилжилтэд `Source`-оо `Merge`-ээр JWT-ээс хөтөлсөн Source-той нэгтгэж болно (хэрэв хэрэгтэй бол). |
| 5с map cache + `InvalidateUser/Org` | `WithTTL(5s)` + `Invalidate/InvalidateOrg` (+ generations) |
| `UsersWithCapability(org, m, cap)` | `Policy.UsersWithCapability` (`Lister` Source; §4.2) |
| 403 `{status,error,message,module,capability}` | 403 `{"error","module","capability"[,"capabilities"]}` (§4.1; `status`/`message` байхгүй). |
| `RegisterImplicitGrant` | `Merge(jobSource, implicitSource)` |

### Хуучин өгөгдлийг ДАТА болгон оруулах (код биш)
Хуучин системийн хоёр эх сурвалжийг библиотект код болгон хуулахгүй; нэг удаагийн migration/скриптээр `modules` JSON болгон хөрвүүлнэ:

1. **`modules` блок** (`view_employee.permissions`, ажлын байрны `job.permissions`) — формат өөрчлөгдөөгүй. `GormSource` шууд уншина; хуурамч `owner` үл тооно.
2. **Хуучин цэсний эрх** (`permissions.<menu uuid> = {show,c,r,u,d}`, мөн `lambda/role_N.json`-ийн `permissions.permissions`):
   * main-ийн `menucaps.go` дахь `MenuCap{Module, R, C, U, D []string}` хүснэгтийг **data файл** (JSON/CSV: `menu_id → module + r/c/u/d cap-ууд`) болгож экспортлоно (`legacymenus.go`-ийн бүлгүүд хамт).
   * Скрипт хэрэглэгч бүрийн цэс бүрт `c/r/u/d=true` бол харгалзах cap-уудыг тухайн модулийн `capabilities` (Allow)-д нэмнэ; хуучин `gridCondition/formCondition` (мөрийн хязгаарлалт: `emp_id/user_id/struct_id`) нь модулийн org-түвшний API-д хамаарахгүй тул main-ийн `rowScoped()` дүрмээр **алгасна** (тусад нь тайлагна).
   * `role_N.json`: `role=5` (owner) → `organization_users.access='owner'` (`GormOwner`); `role=6` → ажилтны JSON-оос; бусад role-ийн `permissions` нь ижил `{menu→c,r,u,d}` хэлбэр тул дээрх хөрвүүлэлтээр. Үр дүнг `Grants` JSON болгож ажилтны/ажлын байрны permissions баганад нэмнэ (`ParseGrants` + `json.Marshal(Grants)` round-trip тесттэй).
   * Хөрвүүлсний дараа `Deny`-аар хуучин «хаалттай» тэмдэглэгээг мөн илэрхийлэх боломжтой.
3. Cut-over: хуучин модульд `WithTransition(..., TransitionOrg)` асаагаад, org бүр тохируулагдах тусам автоматаар strict болно.

## 7. Тест

`go test -race ./access/` (DB env-гүй бол GormSource тестүүд SKIP): registry/manifest шалгалт, шийдвэрийн матриц (owner, role,
multi-role, allow/deny, unknown, any-of, transition ×2), fake clock-той TTL, invalidation уралдаан, 50 goroutine → 1 Load,
ctx цуцлал, eviction, generations (2 instance + алдаа), HTTP 401/403/503/500/200, header-д итгэхгүй, crud adapter, `/me` хэлбэр,
түр schema-тай бодит Postgres (org тусгаарлалт, олон мөр нэгдэл, JSON owner escalation, SQL injection, config injection).

## 8. Шийдвэрлэсэн асуултууд (owner, 2026-11-06)
* `UsersWithCapability` — **хийсэн** (`Lister`, §4.2).
* 403 `module`/`capability` — **буцаана** (§4.1).
* Source алдаа = **503** хэвээр; FE түр зуурын алдаа гэж үзнэ (§4.1).
* `OwnerPolicy` анхдагч `OwnerAll` (deny-г дарна) — **хэвээр**; дотоод хяналт хэрэгтэй org `OwnerDeniable`.
* Audit — **`WithDenyHook` + `AuditDenials`** (§4.3).

Нээлттэй үлдсэн:
* `crud`-ийн 403 body-д `module/capability`, мөн crud-ийн татгалзлыг hook-д дамжуулах (`ReportDenied` дуудах) — crud багцын өөрчлөлт.
* `Deny`-ийг эрхийн UI-д гаргах эсэх.


## crud-тэй холбох (шийдэгдсэн, 2026-11-06)

```go
caps := access.ActionCaps{Read: {"invoices.view"}, Create: {"invoices.create"}, Update: {"invoices.edit"}, Delete: {"invoices.delete"}}
res := &crud.Resource[...]{
    Can:         pol.CrudCan("finance", caps),       // `can` map + gate (hook-гүй)
    OnForbidden: pol.CrudForbidden("finance", caps), // бодит татгалзалт: 403 {error,module,capability}, 503, deny hook
}
```

Ингэснээр §8-ийн «CrudCan-ийн 403 body» болон «crud-ийн 403-ыг бүртгэх» асуулт шийдэгдэнэ. Татгалзалт бүр hook-д
НЭГ удаа ордог бөгөөд `can` map-ийн тооцоолол hook дууддаггүй.
