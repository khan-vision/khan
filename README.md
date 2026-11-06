# khan

`github.com/khan-vision/khan` нь Khan Vision ERP-ийн Go backend library юм. Postgres-only, multi-tenant
бөгөөд KvForm/KvGrid frontend-д зориулагдсан. Хуучин
[`lambda-platform/lambda`](https://github.com/lambda-platform/lambda)-ийн шилдэг санааг (DB introspection,
schema-driven CRUD, relation-validated select, lifecycle hook, per-module CRUD эрх) авч, түүний асуудлуудыг
(`init()` side effect, global state, runtime codegen, console sync, raw SQL concat, олон dialect) засаж
шинээр бичсэн.

## Хувилбар

Fiber болон GORM үргэлж хамгийн сүүлийн хувилбар дээр байна. Одоо: Go 1.26, Fiber v3.6.1, GORM v1.31.2,
gorm postgres driver v1.6.3, golang-jwt v5.

## Зарчим

- `init()`-д side effect хийхгүй. Бүх зүйл `Load`/`Open`/`New`-ээр explicit үүсэж, error буцаана.
- org_id-ийн дүрэм fail-closed: org зөвхөн JWT-ээс авна. `X-Org-ID` header болон `?org_id`-д итгэхгүй.
- PostgreSQL-ээс өөр DB дэмжихгүй.
- Codegen зөвхөн offline CLI-аар хийгдэж, гаралт нь git-д commit хийгдэнэ.
- Package бүр DB-гүй unit тесттэй. DB тест (`testkit`) зөвхөн local `*test` DB дээр ажиллана.

## Package-ууд

| Package | Төлөв | Үүрэг |
|---|---|---|
| `config` | ✅ | Typed тохиргоо. Env нэрс lambda-тай ижил (`DB_*`, `JWT_*`, `MAIL_*`, `FILE_MAX_SIZE`) |
| `db` | ✅ | `Open`, pool (prod-д хэмжсэн анхдагч утгатай), libpq quote хийсэн DSN, `Tx`, `UpdateVersioned` (optimistic lock, `ErrConflict`) |
| `schema` | ✅ | Postgres introspection → `Catalog`, JSON snapshot, `Diff`, `Lint`, `GoType` |
| `query` | ✅ | KvGrid list/filter/sort/paging/totals/export/bulk (main `app/listquery`-ээс зөөсөн) |
| `tenant` | ✅ | org_id-г JWT-ээс авна, `Require`, `Middleware`, fail-closed GORM `Scope`. Хоёр дахь хамгаалалт: Postgres RLS (`Tx`/`WithOrg` → `app.org_id`, `PolicySQL` soft/strict, `SystemTx`), org нөхцөлгүй query-г илрүүлэх GORM `Guard` plugin (Warn/Strict) |
| `migrate` | ✅ | Хувилбартай forward-only SQL migration: lock_timeout/statement_timeout, advisory lock, checksum drift, `-- khan:no-transaction` |
| `auth` | ✅ | HS256 JWT + түлхүүр эргүүлэлт (`kid`, өмнөх түлхүүр, kid-гүй хуучин token), `Required`/`Optional` middleware, `User(c)`, bcrypt, mutex-тэй `Limiter` (rate limit + өсөн нэмэгдэх lockout) |
| `obs` | ✅ | `RequestID`, request-scoped slog, `AccessLog`, нууц далдлалт (`RedactString`, `ReplaceAttr`), `/healthz` + `/readyz`, `GormLogger` |
| `events` | ✅ | Transactional outbox: `Publish(tx, Event)`, `Dispatcher` (at-least-once, SKIP LOCKED, retry/backoff/dead, `prefix.*`), `Purge`, `SchemaSQL` |
| `idempotency` | ✅ | `Idempotency-Key` Fiber v3 middleware (JWT org/user scope, replay, 409/422, TTL), `Purge`, `SchemaSQL` |
| `upload` | ✅ | Public + **нууц файл** (org-оор тусгаарласан хадгалалт, HMAC signed URL, kid эргүүлэлт, атомаар хадгалах) → [docs/UPLOAD.md](docs/UPLOAD.md).  Аюулгүй файл оруулах/үйлчлэх (lambda `/lambda/krud/upload` + `/uploaded` thumb-ийг орлоно). Анхдагч: JSON хариу, `<ангилал>/2026/11/`, таашгүй нэр (`geree-<16hex>.pdf`), өргөтгөл буруу raster зургийг жинхэнэ өргөтгөлөөр хадгална; `LambdaCompat` = шилжилтийн хуучин route-д text/plain + `2026/November` + дараалсан нэр. Хуучин файлууд хэвээр үйлчлэгдэнэ. өргөтгөл allowlist + агуулгын шалгалт (magic byte, decode, zip бүтэц), SVG XML-tokenizer sanitizer, pixel-bomb хамгаалалт, `thumb_` thumbnail, `os.Root` суурьтай `LocalStorage` (symlink/`..` гарахгүй, түр файл+rename, O_EXCL), `Serve` (nosniff, CSP sandbox, Range, ETag) |
| `testkit` | ✅ | Local test DB guard, `TxDB` (rollback), fake/signed JWT |
| `access` | ✅ | Модулийн эрхийн policy: manifest registry, role/allow/deny (deny давамгайлна), санах ойд compile + TTL/invalidate + олон instance-д generation, fail-closed (Source алдаа → 503), `Require`, `CrudCan` + `CrudForbidden` (403 `{error,module,capability}`), `Me` (front_end usePermission-тэй ижил), `UsersWithCapability`, deny hook → `AuditDenials` → [docs/ACCESS.md](docs/ACCESS.md) |
| `money` | ✅ | Яг мөнгөний төрөл (int64 бага нэгж, numeric(18,2)), хэтрэлт = алдаа, бөөрөнхийлөлтийн горим заавал, `Allocate`/`Split` нийлбэр яг, валютын scale (JPY/KRW), SQL/JSON → [docs/MONEY.md](docs/MONEY.md) |
| `jobs` | ✅ | Postgres job queue + cron scheduler: транзакц доторх `Enqueue`, SKIP LOCKED + lease + fencing, heartbeat алдвал цуцлана, retry/dead, tick бүр олон instance-д яг нэг удаа, org-scoped admin → [docs/JOBS.md](docs/JOBS.md) |
| `audit` | ✅ | Өөрчлөлтийн түүх (хэн/юуг/хэзээ/хуучин→шинэ) бизнес транзакцад, нууц үг далдлагдана, append-only (UPDATE/DELETE/TRUNCATE хориотой), crud `Resource.Audit` + `/:id/history` (DetailQuery-ийн харагдах эрх) → [docs/AUDIT.md](docs/AUDIT.md) |
| `gen` | ✅ | DB catalog + хуучин lambda form/grid JSON → `crud.Resource` Go код (`khan gen resource`, `khan gen audit`) → [docs/GEN.md](docs/GEN.md) |
| `cmd/khan` | ✅ | `khan schema dump / lint / diff / rls`, `khan migrate status / up` |
| `validate` | ✅ | JSON → typed утга (`Parse`: Money/Number decimal мөр, Int, Date, UUID, Enum, JSON, Password→bcrypt, Phone, Email), `Rules`, `Errors` → 422 envelope |
| `lookup` | ✅ | `Spec` → `GET /options` (`search/parent/ids/limit`) + `Exists` (relation/FK шалгалт, org-scoped), `Where` grammar шалгагч |
| `crud` | ✅ | Declarative CRUD: `Resource[T]` → list/query/export/bulk (`query`-ээр) + GET/POST/PUT/DELETE, options, meta, children (subform), hook, events (outbox), optimistic `Version`, soft delete, RLS. Гэрээ: [docs/CRUD.md](docs/CRUD.md) |
| `codegen` | ✅ | `crud.Meta` → TypeScript (`Payload`/`Row` interface, zod v4 schema, `KvFieldDef[]`, Ref `load`, endpoint) ба OpenAPI 3.1 JSON; deterministic, `WriteFile` (өөрчлөлтгүй бол хөндөхгүй). Апп өөрийн `cmd/kvgen`-ээс дуудна. [docs/CRUD.md §6](docs/CRUD.md) |
| `crudtest` | ✅ | `Isolation(t, Config)` — tenant leak тест generator (list/GET/PUT/DELETE/bulk/body org_id/ref/options/children/401), JWT эсвэл header claims. [docs/CRUD.md §7](docs/CRUD.md) |
| `cache` | ✅ | Generation-д суурилсан, org-тусгаарлагдсан хариуны cache: `Memory` (sharded LRU+TTL), `Cache.Remember` (singleflight, fail-open), `Bump`/`Version`, `ETag`/304, `BumpOnEvents`; `cache/rediscache` (go-redis v9, олон instance bump-ийг шууд харна); `crud.Resource.Cache`. [docs/CACHE.md](docs/CACHE.md) |

## Хэрэглээ

```go
cfg, err := config.Load()            // эсвэл config.Load(config.WithEnvFile(".env"))
if err != nil { log.Fatal(err) }
gdb, err := db.Open(cfg.DB)
if err != nil { log.Fatal(err) }

cat, err := schema.Introspect(ctx, gdb, schema.Filter{Schemas: []string{"finance"}})
inv, _ := cat.Table("finance.invoice")
for _, issue := range schema.Lint(*inv) { fmt.Println(issue) }
```

CLI (DB-ээс зөвхөн уншина):

```bash
go run ./cmd/khan schema dump -env ../main/.env.test -schema finance,hr -o catalog.json
go run ./cmd/khan schema lint -schema finance
go run ./cmd/khan schema diff old.json new.json
```

Migration (`NNNN_description.sql` файлууд, `embed.FS` эсвэл `os.DirFS`):

```go
ms, err := migrate.Load(migrationsFS)
applied, err := migrate.Up(ctx, gdb, ms, migrate.Options{}) // lock_timeout 5s, statement_timeout 15m
```

```bash
go run ./cmd/khan migrate status -dir ./migrations -env .env
go run ./cmd/khan migrate up -dir ./migrations -dry-run
go run ./cmd/khan migrate up -dir ./migrations -lock-timeout 3s -lint-schema finance,hr
```

Migration бүр өөрийн транзакцид `SET LOCAL lock_timeout`-той ажиллана (hot хүснэгтийг удаан түгжихгүй).
Файлын эхний мөр `-- khan:no-transaction` бол транзакцгүй (`CREATE INDEX CONCURRENTLY`; нэг statement).
Хэрэглэгдсэн файл өөрчлөгдвөл (checksum) эсвэл DB-д байгаа хувилбар хавтсанд алга бол юу ч ажиллуулахгүй
алдаа өгнө (`-allow-missing` / `Options.AllowMissing`). Хяналт: `khan.schema_migrations`. Зөвхөн урагшаа.

## CRUD (declarative)

```go
rooms := &crud.Resource[map[string]any]{
    Name: "organization.conference_room", Label: "Хурлын өрөө", Endpoint: "/api/organization/conference/rooms",
    Table: "organization.conference_room",
    Fields: []crud.Field{
        {Name: "room_name", Label: "Нэр", Kind: validate.String, Rules: validate.Rules{Required: true, MaxLen: 500}},
        {Name: "capacity", Label: "Багтаамж", Kind: validate.Int, Rules: validate.Rules{Nullable: true, Min: validate.Bound(0)}},
        {Name: "owner_id", Label: "Эзэмшигч", Kind: validate.UUID, Rules: validate.Rules{Nullable: true},
            Ref: &lookup.Spec{Table: "organization.employee", Label: []string{"last_name", "first_name"}, SoftDelete: true}},
    },
    List: query.Spec{Search: []string{"t.room_name"}, DefaultOrder: "t.room_name"},
    Can:  func(c fiber.Ctx, a crud.Action) bool { return a == crud.ActionRead || isAdmin(c) }, // ЗААВАЛ (fail-closed)
    DB:   func() *gorm.DB { return gdb },
    SoftDelete: true, Version: "version", Events: true,
}
if err := rooms.Mount(app.Group("/api/organization/conference/rooms", authMW)); err != nil { // тохиргоо буруу бол error
    log.Fatal(err)
}
```

`org_id` зөвхөн JWT-ээс; body-ийн `org_id`/`id` үл тоогдоно; өөр org-ийн мөр 404; ref нь тухайн org-д олдох ёстой (422);
алдаа `{"error":"…","errors":{"field":["…"]}}` (KvForm `serverErrors.ts`).

## Cache (ETag / 304)

```go
c, err := cache.New(cache.Config{})                                   // L1 + in-process gens
// олон instance: rc := rediscache.New(redisClient); c, err = cache.New(cache.Config{L2: rc})
invoices.Cache = &crud.CacheSpec{Cache: c, DependsOn: []string{"finance.customer"}} // GET /, /query, /:id, /options/:field
// crud-ийн гадуурх бичилт: c.Bump(ctx, orgID, "finance.invoice") эсвэл
disp.Handle("finance.*", cache.BumpOnEvents(c))
```

Бичилт commit болсны дараа `(org, resource)` generation нэмэгдэж хуучин түлхүүр хэзээ ч уншигдахгүй; хариу нь
`ETag` + `Cache-Control: private, no-cache`-тэй, `If-None-Match` таарвал DB-гүй `304`. `can` cache-д ордоггүй.
Redis унавал DB-ээс шууд үйлчилнэ (`X-Khan-Cache: bypass`).

## Tenant тусгаарлалт (RLS)

```bash
# org_id-тэй хүснэгт бүрт policy үүсгэж migration болгоно (эхлээд soft горимоор)
go run ./cmd/khan schema rls -schema finance -mode soft -o migrations/0007_rls_finance.sql
go run ./cmd/khan migrate up -dir migrations
```

```go
gdb.Use(tenant.NewGuard(tenant.TenantTables(cat), tenant.GuardWarn, log.Printf)) // dev/test-д GuardStrict
err := tenant.Tx(ctx, gdb, orgID, func(tx *gorm.DB) error { /* app.org_id тавигдсан */ return nil })
```

Superuser болон BYPASSRLS эрхтэй role-д RLS үйлчлэхгүй. Апп нь энгийн role-оор холбогдох ёстой.

## Тест

```bash
go vet ./... && go test ./...                      # DB-гүй (DB тест skip болно)

KHAN_TEST_DB_HOST=127.0.0.1 KHAN_TEST_DB_DATABASE=khan_vision_test \
KHAN_TEST_DB_USERNAME=$USER KHAN_TEST_DB_EXTRA="sslmode=disable" go test ./...
```

`testkit` нь local биш host эсвэл нэрэндээ `test` агуулаагүй database руу холбогдохоос татгалзана.
