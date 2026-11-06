# gen — Lambda form/grid → crud.Resource generator

`gen` package ба `khan gen` команд нь **P4** шатанд (120 Lambda form + 101 grid → native `crud` resource)
гар ажлыг багасгана. Оролт: DB-ийн бүтэц (`schema.Catalog`) + (заавал биш) хуучин Lambda form/grid JSON.
Гаралт: gofmt хийгдсэн **ноорог** Go файл (`func <Name>Resource(db, can) *crud.Resource[map[string]any]`).
Generator нь үүсгэсэн бүтцээ `crud.Resource.Check()`-ээр өөрөө шалгадаг тул Mount-д унах тохиргоо гаргахгүй.
Гаралт тогтвортой (огноо/random байхгүй): ижил оролт → ижил текст.

## 1. Хэрэглээ

```bash
# 1) catalog snapshot (DB-гүйгээр ажиллахад)
khan schema dump -env .env -o catalog.json

# 2) нэг resource
khan gen resource -catalog catalog.json -table planning.tasks \
    -form main/lambda/schemas/form/1018.json [-grid .../grid/1019.json] \
    -label "Ажил" -name Task -endpoint /api/planning/tasks -pkg planning -o task_resource.go

# 3) олон формыг файл бичихгүй шалгах (coverage), эсвэл -o DIR-ээр бүгдийг нь үүсгэх
khan gen audit -catalog catalog.json -forms main/lambda/schemas/form \
    -models main/lambda/models/form/formModels -grids main/lambda/schemas/grid [-force] [-v] [-o DIR]
```

`gen resource` flag: `-table schema.name` (хоосон бол `-form`-ын `model`), `-form`, `-grid`, `-name`, `-label`,
`-endpoint`, `-pkg`, `-o`, `-catalog` (өгөөгүй бол `-env`-ээр DB-ээс), `-force`,
`-hidden createonly|keep|exclude`, `-ref-label field=col1+col2` (давтаж болно). TODO-ууд stderr-д ч хэвлэгдэнэ.

Go API: `gen.Generate(gen.Options{...}) (*Result, error)` (Source, Todos, Issues, Fields, Children),
`gen.Resource(opts) ([]byte, error)`, `gen.ParseForm/ParseGrid`, `gen.Audit`, `gen.ModelTables`.

**Татгалзах дүрэм**: `schema.Lint`-ийн `org-id` / `primary-key` зөрчилтэй хүснэгтээс resource үүсгэхгүй
(`*gen.RefusedError`, crud org_id шаарддаг). `-force` = үүсгээд `TODO [lint]` + файлын дээр зөрчлийг бичнэ.
Timestamps/money зөрчил нь мэдээллийн шинжтэй (комментоор гарна).

## 2. Баганын mapping

| DB | validate.Kind | Тайлбар |
|---|---|---|
| `numeric(18,2)` | Money | |
| `numeric(p,0)` | Int | |
| бусад `numeric(p,s)` | Number `Scale: s` | s>18 бол 18; scale үл мэдэгдэх бол анхдагч |
| `float4/float8` | Number | |
| `int2` / `int4` | Int + `Min/Max` (төрлийн хязгаар) | |
| `int8` | Int | |
| `bool` / `date` | Bool / Date | |
| `timestamp[tz]` / `time[tz]` | DateTime / Time | |
| `uuid` | UUID | |
| `text`, `citext` | Text | |
| `varchar(n)`, `bpchar(n)` | String `MaxLen: n` | n байхгүй бол String (анхдагч 255) |
| enum | Enum + утгууд | |
| `json[b]` | JSON | |
| массив, geometry, tsvector, bytea, interval … | — | талбар хасагдаж `TODO [column-type]` |

- `NOT NULL` + default байхгүй → `Required`; nullable → `Nullable`; default-тай NOT NULL → аль нь ч биш.
- Хамгаалагдсан багана (`id`, `org_id`, `created_at/updated_at/deleted_at`, `created_by/updated_by`, `version`) Fields-д орохгүй.
  PK `(org_id,id)` нийлмэл бол `id` нь ID. `int` PK → `IDInt`. Generated/identity (PK биш) багана алгасагдана.
- `deleted_at` → `SoftDelete`; `version` (int) → `Version`; `created_by`+`updated_by` → `TrackUser`;
  `created_at`/`updated_at` бүрэн биш бол `Timestamps: crud.Off()`.
- **Unique**: зөвхөн `(org_id, col)` composite unique → `Unique: true`. Глобал single-column unique нь Unique болохгүй, комментоор тэмдэглэгдэнэ.
- **FK → Ref** (`lookup.Spec`): `org_id` хос нь орсон нийлмэл FK-г нэг баганынх гэж үзнэ. `Tenant: lookup.TenantOff()` (ref-д org_id
  байхгүй бол), `SoftDelete` (deleted_at байвал). Label таамаглал: `lastname+firstname` → `name` → `title` → `label` → `*_name` → `code` →
  эхний текст багана; `-ref-label`-ээр дарна.
- **Шошго**: legacy form label → багана comment → баганын нэр (aggregated `TODO [label]`).
- **List**: `Search` = текст баганууд (≤8, нэр-төстэй нь түрүүнд; uuid/enum-д ILIKE байхгүй), `Sort` бүх баганаар (tenant/аудитын баганагүй),
  `Filters` төрлөөр (Text/Number/Date/Enum/Bool/UUID), `DefaultOrder` = grid sort эсвэл `created_at DESC, id DESC`.
- Password төрөл → `ListQuery` (hash-ийг SELECT-ээс хасна) автоматаар.

## 3. Legacy feature coverage

| Legacy | Хөрвүүлэлт |
|---|---|
| form талбарын дараалал (`ui` tree) | Fields-ийн дараалал; формд ороогүй багана allowlist-д орохгүй (комментоор жагсаалт) |
| `rules`: required, email, unique, mongolianMobileNumber, number, register, englishAlphabet, mongolianCyrillic, min, max, custom regex | Required / Email / Unique / Phone / Pattern / MinLen,MaxLen,Min,Max. Үл мэдэгдэх/RE2-д хөрвөхгүй → `TODO [rule]` |
| `formType` Email, Password, Register, Textarea, Radio, ColorPicker, CK, Image/File, employeeSelector | Kind / `Field.Input` |
| статик `options` (Select/Radio) | Enum (утга≠шошго бол комментоор) |
| `relation` | Ref (table/key/fields/filter→`Where`/sortField/cascade `Parent`); catalog-д олдохгүй, `multiple`, `filterWithUser`≠org_id → `TODO [relation]` |
| `default` | `Field.Default` (`validate.Parse`-аар шалгасан) |
| `hidden` | `CreateOnly` (анхдагч) / keep / exclude |
| `disabled` | `CreateOnly` |
| `fillByUserField` | талбар хасагдаж `TODO [fill-by-user]` (Hooks.BeforeCreate-д JWT-ээс) |
| SubForm | `Children` (`model`=Name, `parent`=FK); child хүснэгт org_id/uuid PK/timestamps шаарддаг, үгүй бол `TODO [subform]` |
| `triggers` insert/update before/after | `TODO [trigger]` → Hooks.* |
| `formula`, `equations` | UI-only (hidden/disabled/required) → `TODO [show-hide]`; бусад → `TODO [formula]` |
| талбарын `trigger`, `ifshowhide`, custom formType, `extraButtons`, `step`, `email` | `TODO` (field-trigger / show-hide / custom-element / form-feature) |
| grid sort/paging/filterable/sortable/searchable/isGlobalSearch | `List` |
| grid `isExcel` | `Export` (харагдах баганууд) |
| grid `columnAggregations` | `Totals` |
| grid `condition`, formula, filter[], triggers, view/virtual багана, excel upload, pivot | `TODO [grid]/[trigger]/[formula]` |

TODO кодууд: `lint label column-type column-missing fill-by-user hidden rule default relation options custom-element trigger
field-trigger formula show-hide form-feature subform grid id version`. `label`, `show-hide` нь cosmetic (логикт нөлөөгүй).

## 4. P4 workflow

1. `khan schema dump` → catalog snapshot; `khan gen audit -v` → хүснэгт бүрийн TODO-г харж эрэмбэл.
2. `khan gen resource ...` → ноорог. `TODO(khan-gen)`-ийг шийд: Монгол Label, hook (trigger), custom `Input`, relation Label, child.
3. `Can`-ийг холбо (`access` package), `Validate`/`Hooks` нэм, `crud.Check()` + `Mount`.
4. `crudtest.Isolation`-аар org тусгаарлалт, payload parity-г тест.
5. `codegen.TS(...)` → front_end KvForm/useKvCrud төрөл; legacy form-той payload ижил эсэхийг шалга.

Анхаар: файлыг дахин generate хийвэл гар засварыг дарна — нэг удаа үүсгээд гараар үргэлжлүүл.
