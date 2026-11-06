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
| `testkit` | ✅ | Local test DB guard, `TxDB` (rollback), fake/signed JWT |
| `cmd/khan` | ✅ | `khan schema dump / lint / diff / rls`, `khan migrate status / up` |
| `access`, `crud` (+ Go↔TS codegen), `lookup`, `validate`, `cache`, `upload`, `audit` | ⏳ | → [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) |

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
