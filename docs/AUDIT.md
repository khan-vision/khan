# audit — өөрчлөлтийн түүх (audit log)

`github.com/khan-vision/khan/audit` — хэн, хэзээ, хаанаас, юуг, хуучин → шинээр ямар утгаар өөрчилсөн
(санхүү/цалингийн найдвартай түүх) + restore-д хэрэгтэй snapshot. `crud.Resource.Audit`-аар автоматаар,
эсвэл `audit.Record`-оор гараар.

## 1. Яагаад (хуучин Lambda `crudlogger`)

| Хуучин | Одоо |
|---|---|
| Хүсэлтийн түүхий input-ийг үйлдлийн ДАРАА, транзакцийн ГАДНА бичдэг | `Record` нь бизнес бичилттэй ИЖИЛ транзакцад — хамт commit/rollback |
| Бичилт амжилтгүй болсон ч лог үлддэг / лог унасан ч өгөгдөл өөрчлөгдсөн | Атомар: audit бичилт унавал бизнес бичилт ч буцна, hook унавал audit мөр үлдэхгүй |
| Хуучин утга байхгүй (input л) | `changes = {field: [old, new]}` — зөвхөн өөрчлөгдсөн талбар, DB-ийн бодит төлвөөс |
| Нууц үг талбар логт орно | `Password` төрөл (hash ч) хэзээ ч бичигдэхгүй — `["***","***"]` |
| Хэн/хаанаас хоосон | actor, role, IP, User-Agent, request id |

## 2. Хүснэгт `khan.audit_log`

`audit.SchemaSQL`-ийг app-ийн migration-д оруулна (идемпотент; `events.SchemaSQL`-ийн адил).

| Багана | Тайлбар |
|---|---|
| `id bigserial` | PK |
| `org_id uuid NOT NULL` | байгууллага (JWT-ээс) |
| `resource text` | `crud.Resource.Name` (`finance.invoice`) |
| `record_id text` | мөрийн id (uuid эсвэл int-ийн текст) |
| `action text` | `create` `update` `delete` `restore` `bulk_update` `bulk_delete` эсвэл custom |
| `actor_id`, `actor_role`, `ip`, `user_agent` (≤255), `request_id` | `audit.MetaFrom(c)`; текст (id нь uuid/int аль ч байж болно) |
| `changes jsonb` | `{"amount":[100,150]}` — зөвхөн өөрчлөгдсөн талбар |
| `snapshot jsonb NULL` | `AuditSpec.Snapshot` үед мөрийн бүтэн төлөв |
| `created_at timestamptz` | `clock_timestamp()` — бодит бичилтийн агшин (`now()` нь транзакцийн эхлэл тул зэрэг бичилтийн түүх урвуу харагддаг байсан). Харуулах/шүүхэд; ЭРЭМБЭ = `id` |

Индекс: `(org_id, resource, record_id, id DESC)`, `(org_id, resource, id DESC)`, `(org_id, actor_id, created_at DESC)`,
`(created_at)` (Purge). Нэг мөрийн бичилтүүд `FOR UPDATE` түгжээгээр цувдаг тул `id` өсөх дараалал = бодит дараалал.
`SchemaSQL` нь хуучин `now()` default-ыг `clock_timestamp()` болгож, хуучин индексийг сольно (идемпотент).

**Append-only:** `BEFORE UPDATE OR DELETE` (row) trigger `UPDATE`/`DELETE`-ийг (`insufficient_privilege`) хориглоно,
`BEFORE TRUNCATE` (statement) trigger `TRUNCATE`-ийг хориглоно. Устгах цорын ганц зам = `audit.Purge`: багц бүрийг
өөрийн транзакцад `set_config('khan.audit_purge','on', true)` (local — транзакцаас гадуур үлдэхгүй) тавьж DELETE-ийг
нэвтрүүлнэ. `SchemaSQL` нь trigger-ийг drop хийхгүй: байхгүй/хуучин (зөвхөн UPDATE) үед л `CREATE OR REPLACE TRIGGER`
(PG14+), үгүй бол алгасдаг тул дахин ажиллуулахад хүснэгт түгжигдэхгүй. Апп-ийн DB хэрэглэгчид `UPDATE`/`DELETE`/`TRUNCATE`
эрх огт өгөхгүй байх нь илүү найдвартай. (Тест/cleanup-д `DELETE FROM khan.audit_log` ажиллахгүй — `Purge` ашигла.)

**Partition-д бэлэн.** Бүх уншилт `org_id` (+resource/record_id) ба `created_at`-аар шүүгддэг. Сая мөрөөс
дээш болбол `created_at`-аар RANGE partition хийж (сар бүр), хуучин partition-ийг `DETACH/DROP` хийх нь Purge-ээс хямд.
Partition хүснэгтэд PK нь `(id, created_at)` байх ёстой тул `SchemaSQL`-ийг шууд ашиглахгүй — шинэ хүснэгт
`PARTITION BY RANGE (created_at)`-аар үүсгэж, өгөгдлийг шилжүүлээд нэрийг солино; `audit` кодонд өөрчлөлт хэрэггүй.

## 3. crud интеграци

```go
r := &crud.Resource[Row]{
    Name: "finance.invoice", /* … */
    Audit: &crud.AuditSpec{
        Enabled:  true,
        Snapshot: true,                       // сонголт: мөрийн бүтэн төлөв
        Exclude:  []string{"cache_total", "lines.price"}, // сонголт
    },
}
```

`Audit == nil` (эсвэл `Enabled: false`) үед ямар ч өөрчлөлт/нэмэлт query/route байхгүй.

| Үйлдэл | Бичигдэх зүйл |
|---|---|
| `POST /` | `create`: `[null, new]` — DB-д бодитоор орсон төлөв (default-уудтай). children байвал `lines: [null, [...]]` |
| `PUT /:id` | `update`: түгжсэн хуучин мөр ↔ commit-ийн өмнөх шинэ мөр (hook/trigger-ийн өөрчлөлт ч орно). **Өөрчлөлтгүй бол мөр бичигдэхгүй.** Children илгээсэн бол `lines: [old[], new[]]` |
| `DELETE /:id` | `delete`: `[old, null]`; hard delete үед children-ийн мөрүүд ч |
| `POST /bulk` | мөр бүрт `bulk_update` / `bulk_delete` (түгжсэн мөрүүд; өөр org-ийн id алгасагдана; өөрчлөгдөөгүй мөр алгасна). Нэг INSERT-ээр |

- **Ямар талбар:** зөвхөн `Fields`-д зарласан (`id`, `org_id`, timestamps, version орохгүй). Child мөр = `id` + зарласан талбарууд.
- **Атомар:** audit бичилтийг After* hook-ийн ДАРАА, `events.Publish`-ийн өмнө ижил транзакцад хийнэ. Audit алдвал (хүснэгт
  байхгүй, DB алдаа…) бүхэл хүсэлт rollback + 500 (дотоод алдаа body-д ил биш, лог руу). Энэ нь санаатай: «өөрчлөлт хийгдсэн
  боловч түүх байхгүй» байдал үүсэхгүй. Audit хүснэгтийг заавал migration-д үүсгэ — үгүй бол бүх бичилт 500.
- **Password:** төлөв дотор өөрчлөгдсөн эсэхийг мэдэхийн тулд санах ойд л байна; `changes`-д `["***","***"]`, snapshot-д `"***"`.
  bcrypt salt-тай тул ижил нууц үгийг дахин илгээх нь «өөрчлөгдсөн» гэж бүртгэгдэнэ.
- **Exclude:** `Field.Name`, child нэр (`lines`), child талбар (`lines.price`). Зөвхөн Exclude хийсэн талбар өөрчлөгдвөл мөр бичигдэхгүй. Буруу нэр Mount-д алдаа.
- **Нэмэлт ачаалал:** write бүрд өмнөх/дараах төлөвийн 1–2 SELECT (+children байвал мөр бүрд 1). Audit-гүй resource-д 0.
- **Route-ууд** (Mount, `ActionRead` эрхээр, `t.withTimeout`-тай; 403 = `e.forbidden` → `OnForbidden`):
  - `GET /:id/history` — эхлээд мөр resource-ийн **DetailQuery**-ээр (org, soft-delete, custom нөхцөл) харагдаж байгаа эсэхийг
    шалгана; харагдахгүй бол `GET /:id`-тэй ижил **404**. Устгагдсан (soft/hard) мөрийн түүх анхдагчаар **404**;
    `AuditSpec.DeletedHistory: true` үед мөр байхгүй/soft-delete хийгдсэн бол түүх гарна (амьд боловч DetailQuery-ээр нуугдсан
    мөр үргэлж 404, өөр org-ийнх хоосон жагсаалт).
  - `GET /history` (resource-ийн бүх түүх) — **зөвхөн `AuditSpec.ListHistory: true`** үед. Мөр бүрийн харагдацыг шалгахгүй
    тул `ListQuery`/`DetailQuery` тохируулсан resource-д `ListHistory` = Mount/Check алдаа (fail-closed). Унтраалттай үед
    route байхгүй (`/history` → `/:id` руу өнгөрч 400). Мөрийн нууцлалтай resource зөвхөн `/:id/history` ашиглана.
  `/history` нь `/:id`-ээс өмнө бүртгэгдэнэ. Түүх нь хуучин утгуудыг агуулдаг тул «унших» эрхтэй хэн ч харна —
  илүү нууцлах талбарыг `Exclude`-д ор.

## 4. Унших API

`GET …/:id/history` ба `GET …/history`:

```
?page=1&limit=20            limit ≤ 100, page ≤ 1000 (давбал 400)
?before_id=123              keyset: зөвхөн id < 123 (гүн унших зам; offset-оос хурдан)
?from=2026-11-01&to=2026-11-30   RFC3339 эсвэл YYYY-MM-DD (to нь өдрийг оролцуулна)
?actor_id=…&action=update&record_id=…&request_id=…   (зөвхөн /history; /:id/history-д record_id = path)
?snapshot=1                 snapshot-ийг оруулах
```

```json
{"data":[{"id":12,"resource":"finance.invoice","record_id":"…","action":"update","action_label":"Засварласан",
  "actor_id":"…","actor_role":"1","ip":"…","user_agent":"…","request_id":"…",
  "changes":{"amount":[100,150.50]},"created_at":"2026-11-06T12:00:00Z"}],
 "total":25,"total_capped":false,"has_more":true,"next_before_id":12,"page":1,"limit":20}
```

ШИНЭ нь эхэнд (`id DESC`).
**Хуудаслалтын хязгаар:** `page ≤ 1000`, `limit ≤ 100` (offset ≤ 100 мянга мөр). `total` нь **10000-аар хязгаарлагдсан**
(тоолуур хамгийн ихдээ 10001 мөр уншина; хэтэрвэл `total=10000, total_capped=true`). `has_more` = дараагийн хуудас байна
(limit+1 мөр уншиж тодорхойлно); `next_before_id` = хуудасны хамгийн жижиг id → дараагийн хуудсыг `?before_id=` -оор
авна (гүн түүхийн зөв зам). Offset нь UI-ийн нийцтэйн тулд хэвээр. Action шошго: Үүсгэсэн, Засварласан, Устгасан, Сэргээсэн, Бөөнөөр засварласан, Бөөнөөр устгасан.
Алдаа: 401 (JWT), 403 (`ActionRead` үгүй), 404 (`/:id/history`: мөр харагдахгүй — §3), 400 (буруу id/page/before_id/limit/огноо/NUL).
Org нь ЗӨВХӨН JWT-ээс — өөр org-ийн түүх хэзээ ч гарахгүй (`/history`: зөвхөн өөрийн org; `/:id/history`: 404).

crud-гүй хэрэглээ: `audit.Handler(audit.HandlerConfig{Resource, DB, Can, Org, RLS, ParseID})` → `Handlers{Record, List}`;
`audit.List(ctx, db, audit.Filter{OrgID: …})`. `HandlerConfig.Visible` (Record-ийн мөрийн харагдац шалгалт; false → 404) нь
crud-ийн DetailQuery шалгалтыг дамжуулдаг; nil бол зөвхөн `Can` + org.

## 5. Гараар (crud-гүй)

```go
err := db.Tx(ctx, gdb, func(tx *gorm.DB) error {
    // … бизнес бичилт …
    return audit.Record(tx, audit.Entry{
        OrgID: orgID, Resource: "finance.journal", RecordID: id, Action: "post", // custom action
        Meta:    audit.MetaFrom(c),
        Changes: audit.Diff(oldMap, newMap, audit.DiffOptions{Redact: []string{"pin"}}),
    })
})
```

`Record` транзакцгүй `*gorm.DB`-д `ErrNotInTx` буцаана. `RecordMany` олон entry-г нэг INSERT-ээр. Entry-ийн `OrgID`
(uuid), `Resource` (`a.b_c`), `RecordID` (≤200), `Action` (`[a-z][a-z0-9_]*`) заавал. Текстүүд цэвэрлэгдэж (NUL, буруу UTF-8)
таслагдана. `Record` нь `Changes`/`Snapshot`-ийн нууц түлхүүрийг дахин «***» болгоно (гараар бүтээсэн ч).

## 6. `Diff` ба нормальчлал

`audit.Diff(old, new map[string]any, DiffOptions{Partial, Exclude, Redact, Reveal}) Changes`

- **Өөрчлөгдөөгүй гэж тооцох:** `12.5` = `"12.50"` = `json.Number("12.500")`; `int 12` = `12.0`; өөр бүсийн ижил `time.Time`;
  `time.Time` = RFC3339 мөр (ижил агшин); `"2026-01-02"` = 00:00 UTC; JSON-ийн түлхүүрийн дараалал, `json.RawMessage` ↔ map; `[]byte` = string.
- **Өөр гэж тооцох:** `nil` ≠ `""` ≠ `0` ≠ `false`; `"007"` ≠ `"7"` (мөр↔тоо хөрвүүлэлт нь НЭГ тал нь тоо үед л); массивын дараалал.
- **nil vs missing:** байхгүй түлхүүр = `nil`. `Partial: true` үед `new`-д байхгүй түлхүүр «өөрчлөгдөөгүй» (хэсэгчилсэн PUT);
  `new`-д `nil` гэж ИРСЭН бол өөрчлөлт. Create = `Diff(nil, row)`, delete = `Diff(row, nil)`.
- **Тоо:** `json.Number` — scale хадгалагдана (`12.50`), 64-bit-аас том decimal алдагдахгүй. crud нь numeric-г `CAST(… AS text)`-ээр уншина.
- **Нууц:** `Redact` жагсаалт + түлхүүрийн нэрэнд `password`, `passwd`, `secret`, `token`, `authorization`, `cookie`, `api_key`,
  `credential`, `private_key` агуулсан бол автоматаар «***» (`obs.SensitiveKey`; nested JSON-д ч). Мөр утгын доторх Bearer/JWT/`token=…`
  нь `obs.RedactString`-ээр далдлагдана. Нэрээр нь буруу далдлагдсаныг `Reveal`-ээр чөлөөлнө (ж: `token_count`).
  `Changes.Old()/New()` нь «***» утгыг оруулахгүй (restore-д аюулгүй).
- **Тогтвортой:** `Changes`-ийн JSON түлхүүр цагаан толгойн дарааллаар; ижил өөрчлөлт ижил байт.

## 7. Restore

`snapshot` (эсвэл `changes`-ийн `Old()`) нь мөрийг өмнөх төлөвт нь буцаахад хангалттай; `ActionRestore` нь тэр үйлдлийг
бичихэд зориулагдсан. Restore-ийг апп өөрөө (өөрийн эрхийн шалгалттай) `PUT`-аар хийж, `audit.Record(…, ActionRestore, …)` дуудна.
Автомат restore endpoint энд ОРООГҮЙ (бизнес дүрэм, үлдэгдэл/журналын нөлөө resource бүрт өөр).

## 8. Retention — `audit.Purge`

```go
n, err := audit.Purge(ctx, gdb, time.Now().AddDate(-2, 0, 0), audit.ForOrg(orgID), audit.ForResource("finance.invoice"), audit.Batch(5000))
// бүх org (зөвхөн системийн cron): audit.AllOrgs() — ЗААВАЛ ил зааж өгнө
```

Хуучин бичлэгүүдийг багцлан (`Batch`, анхдагч 5000) устгаж нийт тоог буцаана. `olderThan` хоосон/ирээдүйд бол `ErrPurgeCutoff`; `ForOrg`/`AllOrgs()` аль нь ч байхгүй бол `ErrPurgeScope` (fail-closed).
Багц бүр өөрийн транзакцад `khan.audit_purge=on` тавина (§2).
Cron-оос дуудна. Санхүүгийн хуулийн хадгалалтын хугацааг (жишээ нь 5+ жил) мөрдөнө; хэт олон мөр болбол §2-ын partition.

## 9. Хязгаарлалт

- Бүх `Field`-ийн өмнөх/дараах утга (`Exclude`-оос бусад) түүхэнд орно: том `Text`/`JSON` талбар хадгалалтыг ихэсгэнэ.
  Утгыг таслахгүй (өөрчлөлтийг найдвартай илрүүлэхийн тулд).
- Child мөрийн өөрчлөлт нь талбар түвшний diff БИШ — child-ийн бүтэн жагсаалт `[old[], new[]]` (мөр бүрд `id`).
- Hook доторх (`x.Tx`) өөр хүснэгтийн бичилт автоматаар бүртгэгдэхгүй — `audit.Record`-оор өөрөө дууд.
- Resource-ийн `DetailQuery`/`ListQuery`-ийн тооцоолсон багана түүхэнд орохгүй (зөвхөн хүснэгтийн зарласан талбар).
- `Version` багана, timestamps, `created_by/updated_by` түүхэнд ордоггүй (actor нь entry-д).
- `actor_id`-г client-ээс авахгүй: зөвхөн JWT (`auth.User`). `ip` нь Fiber-ийн `c.IP()` — proxy-ийн ард бол app-д `TrustProxy`/`ProxyHeader` зөв тохируулах ёстой.
- Мэдээллийн сангийн эзэмшигч (superuser) trigger-ийг устгаж чадна; бүрэн өөрчлөх боломжгүй байдал хэрэгтэй бол тусдаа эрх, WORM хадгалалт (экспорт) хэрэгтэй.
- Audit-гүй resource-оос audit-тэй болгоход өмнөх түүх байхгүй (эхний `update` нь хуучин төлвийг харуулна, `create` entry байхгүй).
