# Behaviour-parity harness (`khan parity`)

Зорилго: refactor хийсэн backend (B) ажиллаж буй системээс (A) ЯЛГААГҮЙ хариу өгч байгааг
баталгаажуулах. Ижил хүсэлтийг A, B дээр ижил auth-аар тоглуулж, normalise хийсэн хариуг харьцуулна.

## Аюулгүй байдлын дүрэм

- Base URL нь **loopback** (`127.0.0.0/8`, `::1`, `localhost`) байх ёстой. Үгүй бол
  `-allow-remote` өгөхгүй л бол хүсэлт илгээхээс ӨМНӨ татгалзана.
- `-allow-remote` өгсөн ч **зөвхөн GET/HEAD**: POST/PUT/PATCH/DELETE remote руу хэзээ ч явахгүй
  (корпусд байсан ч, `-allow-mutations`-тэй ч). Шалтгаан: remote нь production байж болох тул
  өгөгдөл өөрчлөх, мэдэгдэл/имэйл/төлбөр өдөөх эрсдэлтэй; уншилт л аюулгүй.
- `-allow-mutations` нь хоёр URL хоёулаа loopback үед л хүчинтэй; тус бүр ӨӨРИЙН scratch DB-тэй.
- Нууц (token, нууц үг, Set-Cookie утга) тайланд орохгүй: бүртгэлтэй нууц, JWT, `Bearer ...`,
  мэдрэмтгий JSON түлхүүр (`token`, `password`, `authorization`...) болон query параметр `[REDACTED]` болно.
- Redirect дагахгүй (3xx-ийг өөрөө харьцуулна).

## Workflow: main HEAD vs `khan/pf-fiber-v3` (локал, хоёр scratch DB)

```bash
# 1. Тест DB-ээс хоёр хуулбар (тус бүр нэг сервер)
createdb -T khan_vision_test parity_a
createdb -T khan_vision_test parity_b

# 2. Хоёр worktree: A = main HEAD, B = branch
git worktree add ../parity-a main
git worktree add ../parity-b khan/pf-fiber-v3

# 3. Хоёр backend, өөр порт + өөр DB (env хувьсагчийн нэрийг main/docs/02-setup.md-ээс ав)
( cd ../parity-a/main && DB_NAME=parity_a PORT=18080 go run . ) &
( cd ../parity-b/main && DB_NAME=parity_b PORT=18081 go run . ) &

# 4. Корпус: front-end-ээс GET endpoint-ууд (+ гараар бичсэн)
khan parity corpus -frontend ../front_end/src -auth admin -params params.json \
    -merge handwritten.jsonl -o corpus.jsonl
#   -> corpus.jsonl + corpus.unresolved.jsonl (бөглөгдөөгүй {id} placeholder-ууд)

# 5. Харьцуулалт
khan parity run -a http://127.0.0.1:18080 -b http://127.0.0.1:18081 \
    -corpus corpus.jsonl -profiles profiles.json -ignore ignore.json -expect expected.json -o report/
echo $?   # 0 = parity; != 0 = гэнэтийн ялгаа/алдаа
```

Бичих (mutation) тест: `-allow-mutations` нэмнэ; хоёр DB эхлэхдээ ЯГ ижил (`createdb -T`) байх ба
`ignore.json`-д үүсэх id-г (`$..id`) нэмнэ. Тест тутам DB-г шинээр clone хийвэл давтагдахуйц.

### Тест байгууллагын эрх

Нэвтрэх эрхийг `/Users/munkh-altai/Workspace/khan-vision/khan_codes/test-orgs/` дотор үз
(`khankhulgun.md` — ERP/HR/planning, `khan-admin.md`, `octolondon.md` ...). Тэмдэглэлийн утгыг
profiles/docs/лог руу ХУУЛЖ БОЛОХГҮЙ. Нэвтрэх бодит урсгал (login -> select-role) нэг алхмаар
дуусахгүй бол өөрийн гаргаж авсан token-ыг `token_file`-д (chmod 600) хийнэ. Сервер бүр өөр
JWT_SECRET-тэй бол профайл бүр A, B дээр тусад нь login хийгдэнэ (`login_file`).

## Файлын формат

**Корпус (JSONL)**, `#`-ээр эхэлсэн мөр тайлбар:
`{"method":"GET","path":"/api/hr/employees","query":"limit=10","headers":{},"body":{...},"auth":"admin","module":"hr"}`

**profiles.json**:
```json
{"profiles": {
  "admin": {"login_file": "/secure/path/admin-cred.json", "login_path": "/auth/login"},
  "svc":   {"token_file": "/secure/path/token.txt", "headers": {"X-Org-ID": "..."}}
}}
```
`token` / `token_env` / `token_file` (статик bearer) эсвэл `login_file` (`{"login":..,"password":..}` —
runtime-д уншиж `POST login_path {login,password}`; хариуны `token|access_token|accessToken` (дээд түвшин эсвэл `data`) авна).
Login нь сервер бүр дээр тусад нь хийгдэнэ.

**ignore.json**:
```json
{"ignore_paths": ["$..id", "$.data[*].code"], "unordered_paths": ["$.rows", "$..tags"],
 "ignore_headers": ["cache-control"], "compare_headers": ["x-total-count"], "no_defaults": false}
```

**expected.json** — санаатай ялгааны allowlist. `reason` ЗААВАЛ (үгүй бол ачаалахгүй):
```json
{"expected": [{"method":"GET","path":"/api/hr/*","kind":"body","body_path":"$..grade","reason":"KHA-123: талбарын нэр өөрчилсөн"}]}
```
`path` glob (`*` = дурын тэмдэгт), `kind` ∈ status|content-type|header|body|error, `body_path` (JSON pattern),
`header` (нэр). Ашиглагдаагүй мөр тайланд WARN болно (хуучирсан allowlist).

## Normalise / харьцуулалтын дүрэм

- Status код; content-type **бүлэг** (json/html/xml/text/binary:mime/none; charset үл тооно).
- JSON: түлхүүр эрэмбэлэх, тоо каноник (`12.5 == 12.50 == 1.25e1`, `3 == 3.0`; `"1"` != `1`, `null` != `0`).
- Үндсэн ignore: `$..created_at`, `updated_at`, `deleted_at`, `request_id`, `trace_id`, `etag`, `token`,
  `access_token`, `refresh_token` (түлхүүр үл ялгана case-insensitive). `$..id` ЗОРИУД ороогүй — нэмнэ.
- Pattern: `$..key` (дурын гүн), `$.a.b`, `$.a[*].b`, `$.a.*`. Массив default эрэмбэтэй; `unordered_paths` дахь массивыг эрэмбэлэн харьцуулна.
- Header: Cache-Control, Content-Security-Policy, X-Content-Type-Options, Content-Disposition;
  Set-Cookie зөвхөн нэр + атрибут (Path/Domain/SameSite-ийн утга, Expires/Max-Age байгаа эсэх), утгагүй.
- JSON бус бие: sha256 + урт.
- Тайлан: `report/report.txt` (уншихад), `report/report.json`. Төлөв: identical | differing (гэнэтийн) |
  expected | error | skipped (mutation, бөглөгдөөгүй placeholder). Exit != 0: differing эсвэл error байвал.

## Корпус builder

`khan parity corpus` нь `.get(` `.head(` `$fetch(` `useFetch(` дуудлагын `'/api/..'`, `"/lambda/.."`, `/mapserver/..`
литерал болон template literal-ыг статикаар олно (`node_modules`, `.nuxt` алгасна; `$fetch` дахь `method: 'POST'` GET биш).
`${a.b}` -> `-params` JSON-ийн `"a.b"` эсвэл `"b"` түлхүүрээр бөглөнө, үгүй бол `{b}` placeholder болж
`*.unresolved.jsonl`-д орно (run үед skipped). Query дахь `${}`-тэй хос хаягдана. Module = `/api/<m>/` (public-г алгасна).
Статик шинжилгээ тул динамик (`'/api/x/' + id` -> `{dynamic}`) болон тусгай wrapper-аар дуудсан endpoint-ыг
`-merge`-ээр гараар нөхнө.
