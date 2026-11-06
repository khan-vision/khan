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
| `db` | ✅ | `Open`, pool (prod-д хэмжсэн анхдагч утгатай), libpq quote хийсэн DSN, `Tx` |
| `schema` | ✅ | Postgres introspection → `Catalog`, JSON snapshot, `Diff`, `Lint`, `GoType` |
| `query` | ✅ | KvGrid list/filter/sort/paging/totals/export/bulk (main `app/listquery`-ээс зөөсөн) |
| `tenant` | ✅ | org_id-г JWT-ээс авна, `Require`, `Middleware`, fail-closed GORM `Scope` |
| `testkit` | ✅ | Local test DB guard, `TxDB` (rollback), fake/signed JWT |
| `cmd/khan` | ✅ | `khan schema dump / lint / diff` |
| `auth`, `access`, `crud`, `lookup`, `validate`, `cache`, `upload`, `audit` | ⏳ | → [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) |

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

## Тест

```bash
go vet ./... && go test ./...                      # DB-гүй (DB тест skip болно)

KHAN_TEST_DB_HOST=127.0.0.1 KHAN_TEST_DB_DATABASE=khan_vision_test \
KHAN_TEST_DB_USERNAME=$USER KHAN_TEST_DB_EXTRA="sslmode=disable" go test ./...
```

`testkit` нь local биш host эсвэл нэрэндээ `test` агуулаагүй database руу холбогдохоос татгалзана.
