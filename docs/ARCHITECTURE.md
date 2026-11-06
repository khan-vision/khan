# khan — архитектур ба замын зураг

Бүрэн төлөвлөгөө (owner баталсан, 2026-11-06): `~/.claude/plans/main-main-bootstrap-init-init-go-go-run-modular-breeze.md`.

## Owner-ийн шийдвэр

1. Console/microservice горимыг бүрэн хасна. Зөвхөн standalone горим үлдэх тул `init.go`, console gRPC болон
   `main/lambda/`-ийн codegen байхгүй болно.
2. Lambda-ийн 120 form болон 101 grid-ийг шууд native REST руу шилжүүлнэ (`crud.Resource`). Compat давхарга
   хийхгүй.
3. Зөвхөн PostgreSQL дэмжинэ.
4. GraphQL-ийг REST-ээр солино. Apollo-ийн client cache-ийг орлохын тулд `cache` package заавал хийнэ.

## Lambda → khan

| Lambda | khan | Сайжруулалт |
|---|---|---|
| `config` (init, `godotenv.Overload`, global) | `config.Load()` | Explicit дуудалт. Env нь файлын утгыг дарна. Алдааг error-оор буцаана |
| `DB.DB` (init-д холбогдож, log.Fatal) | `db.Open()` | Pool үргэлж тохируулагдана. DSN quote хийгдэнэ. Global байхгүй |
| `DBSchema` (нэрээр, PK-г таамагладаг) | `schema.Introspect()` | Schema-аар qualify хийнэ. PK/FK/unique-ийг pg_constraint-оос уншина. Enum-ийн утгуудыг өгнө. Partition child болон extension-ийн хүснэгтийг хасна |
| `generator` (runtime, сервер дээр) | `cmd/khan gen` | Offline ажиллаж, гаралт нь commit хийгдэнэ |
| `krud`/`dataform`/`datagrid` + `GetMODEL` switch | `crud.Resource[T]` | Typed. org_id built-in. Hook нь typed функц (string identifier биш) |
| `krudMW` + `role_N.json` (request бүрт дискнээс уншдаг) | `access` | Санах ойд compile хийж, invalidation хийнэ |
| `agent` (бүх user мөр JWT-д, lock-гүй lockout map) | `auth` | Claim нэр ижил үлдэх ч цөөн claim хадгална. Mutex-тэй lockout + rate limit, refresh session |
| `orgctx` / `jwtorg` (main) | `tenant` | `Scope` нь fail-closed |
| `app/listquery` (main) | `query` | Main-ээс шууд зөөсөн |
| GraphQL + gqlguard | `crud` + `cache` | ETag/304, generation-based invalidation |
| `crudlogger` | `audit` | — |

## Хасагдсан

- console/gRPC sync
- puzzle builder
- `vb_schemas`
- GraphQL
- chart `/ve/*`
- moqup
- exportImport
- public `/db-dic`
- datasource view sync
- seo
- MySQL/MSSQL/Oracle

## Дараагийн package-ууд (P1+)

- **auth**: HS256 issue/verify. Claim нэр (`id`, `login`, `role`, `org_id`, `extra_role`, `portal_only`, `sid`)
  одоогийнхтой ижил үлдэх тул идэвхтэй session тасрахгүй. `Required()`, `User(c)`, bcrypt, lockout, refresh.
- **access**: role/module capability policy (`access.Require("finance.invoice", access.Create)`) ба row condition.
- **crud**: `Resource[T]{Table, Fields, Query, Hooks, Validate, Relations, Children, Export, Bulk, Can, Cache}`.
  Дараах route-уудыг mount хийнэ: `GET /`, `POST /query`, `GET /:id`, `POST /`, `PUT /:id`, `DELETE /:id`,
  `GET /export.xlsx`, `POST /bulk`, `GET /options`. Wire нь front_end `useKvCrud`-тэй ижил.
- **lookup**, **validate**: `{error, errors:{field:[msg]}}` хэлбэрийн envelope.
- **cache**: L1 in-process болон L2 Redis. Түлхүүр нь `org:{org}:{resource}:g{gen}:{hash}`. Write бүрд generation
  нэмэгдэнэ. Redis pub/sub, singleflight ашиглана. `ETag` + `Cache-Control: private, no-cache` толгойтой.
- **upload**: SVG/content-type шалгалт, nosniff+CSP, thumbnail.
- **audit**: write audit.
- **cmd/khan gen**: `gen model`, `gen resource` (Go + FE `KvCrudConfig` draft), `gen from-lambda --form --grid`.

## CI

GitHub Actions workflow нь [docs/ci-workflow.yml](ci-workflow.yml)-д байна. Идэвхжүүлэхдээ `.github/workflows/ci.yml` руу хуулна. Push хийх token-д `workflow` scope хэрэгтэй.
