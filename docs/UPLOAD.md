# upload: нийтийн ба хувийн файл

`/uploaded` нь нийтэд нээлттэй (`<img src>` cookie-гүй ажиллах ёстой). Нэр нь таашгүй ч
URL-ийг олсон хүн файлыг үүрд уншина. Гэрээ, цалингийн хуудас, ХН баримт, иргэний үнэмлэх зэрэг
мэдрэмтгий файл **Private** харагдах байдлаар хадгалагдана.

## Public ба Private

| | Public | Private |
|---|---|---|
| Диск | `<Root>/uploaded/<cat>/<yyyy>/<mm>/<name>` | `<PrivateRoot>/<org_id>/<cat>/<yyyy>/<mm>/<name>` (анхдагч `<Root>/private`, файл 0600, хавтас 0700) |
| Унших | `Serve()` (auth-гүй) | `ServePrivate()` (гарын үсэг) эсвэл `ServePrivateAuth()` (нэвтэрсэн org-гишүүн) |
| `Result.Path` | `/uploaded/images/2026/11/a.jpg` | `private/<org>/documents/2026/11/geree-3f9a….pdf` (opaque, шууд татагдахгүй) |
| `Result.URL` | нийтийн URL | богино настай гарын үсэгтэй URL (Signer байвал) |
| Оруулах эрх | `AllowAnonymous` дагана | нэвтэрсэн principal + JWT-ийн `org_id` ЗААВАЛ |
| Нэрлэлт | LambdaCompat дагана | үргэлж санамсаргүй нэр, `yyyy/mm` (LambdaCompat нөлөөлөхгүй) |

`Serve()` зөвхөн `<Root>/uploaded`-ийг харна; private хавтас түүний гадна тул traversal, encoded, double-encoded
зам хэзээ ч private файлд хүрэхгүй (тестээр баталгаажсан). `New` нь нийтийн ба хувийн хавтас, URL угтвар давхцах/давхарлахыг
алдаа болгоно (хавтасны зам `EvalSymlinks`-ээр задлагдсаны дараа харьцуулагдана, тиймээс symlink-ээр нэг хавтас руу
заасан alias мөн алдаа; одоогоор байхгүй сегментийг байгаа эцэг хавтсаар нь задлана). **Root-ийг статик файл серверээр (Caddy `file_server`, `app.Static`) нийтэлж байвал `PrivateRoot`-ийг Root-оос
ГАДНА тавь**, эс бөгөөс `<Root>/private` нийтлэгдэнэ.

## Тохиргоо

```go
signer, _ := upload.NewSigner(
    upload.SignKey{ID: "2026-11", Secret: []byte(os.Getenv("UPLOAD_SIGN_SECRET"))}, // ≥32 байт, JWT_SECRET-ээс ӨӨР
    []upload.SignKey{{ID: "2026-08", Secret: []byte(os.Getenv("UPLOAD_SIGN_SECRET_PREV"))}},
    upload.WithSignMaxTTL(time.Hour), upload.WithSignSkew(30*time.Second),
)
up, _ := upload.New(upload.Config{
    Root: "public", PrivateRoot: "/var/lib/app/private",
    Signer: signer,
    DefaultVisibility:   upload.VisibilityPublic,                         // эсвэл Private
    AllowedVisibilities: nil,                                             // nil → public+private; []{Private} → клиент public хүсч чадахгүй
    PrivateURLTTL:       5 * time.Minute,
})
api := app.Group("/api", auth.Required(kr))
api.Post("/upload", up.Handler())                 // `visibility=public|private` multipart талбар
api.Post("/upload/hr", up.PrivateHandler())       // visibility үргэлж private (клиентийн талбарыг үл тооно)
api.Get("/files/*", up.ServePrivateAuth(authz))   // cookie/JWT-ээр, гарын үсэггүй
api.All("/files-url", up.SignHandler(authz))      // шинэ signed URL авах
up.MountServe(app)                                // /uploaded/*  (public)
up.MountPrivateServe(app, auth.Optional(kr))      // /files/private/*  (auth.Optional: токен байвал уншина, үгүй бол үргэлжилнэ)
```

`authz func(c fiber.Ctx, orgID, opaquePath string) bool` — org-ийн гишүүнээс гадна нэмэлт эрх (ж. «зөвхөн ХН» эсвэл
`hr.document` мөрөөр эзэмшигчийг шалгах). `nil` бол org-ын гишүүн хангалттай. `ServePrivate`-д uid-гүй холбоос auth шаарддаггүй тул
`auth.Optional` зөвхөн `uid`-тэй холбоост хэрэгтэй.

## Signed URL формат

```
/files/private/private/<org>/<cat>/<yyyy>/<mm>/<name>?exp=<unix>&kid=<key id>[&uid=<user id>][&disp=inline|attachment]&sig=<base64url>
```

`sig = HMAC-SHA256(secret[kid], frame(domain="khan-upload-sign-v1", kid, path, org, exp, uid, disp))`; талбар бүр
урт-угтвартай (давхцал/холилт байхгүй). `uid`, `disp` нь MAC-д орсон тул query-ээс хасах/солих боломжгүй.

Шалгалтын дараалал (`ServePrivate`): зам бүтэц (6 сегмент, сегмент бүр regexp) → kid → гарын үсэг (`hmac.Equal`) → хугацаа
(`now ≤ exp + skew`) → `exp ≤ now + MaxTTL + skew` → `uid` байвал нэвтэрсэн principal (байхгүй 401, таарахгүй 403) → файл нээх.
Бүх гарын үсгийн алдаа (буруу зам, kid, sig, хугацаа) ИЖИЛ `403 {"error":"Хандах эрхгүй"}`; **404 зөвхөн зөв гарын үсгийн дараа**,
тиймээс файл байгаа эсэхийг гарын үсэггүйгээр мэдэх боломжгүй.

Хариуны header: `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer`, `X-Robots-Tag: noindex`,
`Cache-Control: private, max-age=<үлдсэн ttl>` (skew дотор 0), идэвхтэй төрөл (svg/html/xml) → CSP `sandbox`,
медиа биш (pdf/зураг/видео/аудио-оос бусад) → `attachment`. `disp=inline` нь зөвхөн аюулгүй төрөлд (pdf, зураг, медиа) үйлчилнэ; `html` хэзээ ч inline болохгүй.
Range (206/416), HEAD, ETag/304 дэмжинэ (гарын үсэггүй хүсэлтэд 304 ч өгөхгүй).

## Түлхүүр эргүүлэлт

`auth.Keyring`-тэй ижил: идэвхтэй түлхүүр (`kid`) зурна, өмнөх түлхүүрүүд зөвхөн шалгана. Эргүүлэх:

1. Шинэ түлхүүр үүсгэ (`openssl rand -base64 48`), шинэ `ID`-тай.
2. `NewSigner(new, []SignKey{old})` болго → шинэ URL шинэ kid-тэй; хуучин URL (дээд тал нь MaxTTL) ажилласаар.
3. `MaxTTL` + `skew` өнгөрсний дараа `old`-ыг хас. Түлхүүр алдагдсан бол шууд хас (тэр түлхүүрээр гарсан бүх URL үгүй болно).

Үл мэдэгдэх kid → 403. Түлхүүрийн алдаанд secret орохгүй.

## Frontend хэрэглээ

- Оруулах: `FormData{file, visibility:"private"}` → `{path, url, expires_at, thumb_url?}`. **`path`-ийг DB-д хадгал** (opaque, `private/<org>/…`);
  `url`-ийг хадгалахгүй (5 минутад дуусна).
- Харуулах/татах: `GET /api/files-url?path=<path>[&ttl_seconds=120][&disposition=attachment][&bind_user=true]`
  (эсвэл POST JSON ижил талбартай) → `{path, url, expires_at}`. Дараа нь `<img src=url>` / `window.open(url)`.
  Дуусах дөхөхөд (`expires_at`) шинээр аваад солино.
- Нэвтэрсэн апп дотор холбоос шаардлагагүй бол `fetch('/api/files/'+path, {credentials:'include'})` (blob болгож харуулна).
- `bind_user=true` нь холбоосыг тухайн хэрэглэгчид хүлээлгэнэ (хуваалцсан ч өөр хүнд ажиллахгүй); `<img src>`-д cookie очиж
  `auth.Optional` principal уншиж чадах үед л хэрэглэ.
- `org_id`-г клиент өгөхгүй: JWT-ээс; `X-Org-ID`, `?org_id=`, форм талбар үл тооно. Өөр org-ын path-д sign хүсвэл 404.

## Одоо байгаа мэдрэмтгий файлуудыг шилжүүлэх

Хуучин public файлууд (`/uploaded/documents/...`) URL-тэй хэвээр нийтэд нээлттэй. Шилжүүлэх:

1. Шинэ оруулалтыг `PrivateHandler`/`visibility=private` болго.
2. Хуучин файл бүрийн org-ыг өгөгдлийн сангаас мэдэж (`org_id` мөр) скриптээр `StoreFile(..., Visibility: Private, OrgID: org)`-ээр
   дахин хадгал (эх файлыг `Storage.Open`-оор уншина), DB-ийн `file_path` баганыг шинэ opaque `Result.Path`-аар солих.
3. Бүх мөр шилжсэнийг шалгаад хуучин файлыг `<Root>/uploaded/documents/...`-оос устга (Storage-д Delete интерфэйс байхгүй, диск дээр гараар),
   мөн CDN/кэшийг цэвэрлэ. **Хуучин URL-ийг олсон хүн шилжүүлэх хүртэл унших боломжтой хэвээр** — алдагдсан гэж үзвэл заавал устга.
4. Тестийн шалгалт: `GET /uploaded/documents/...` хуучин нэрээр 404 өгч байгаа эсэх.

## Хадгалалтын атомар байдал, алдааны горим

- `LocalStorage.Save` эцсийн нэр дээр хагас/хоосон файл ХЭЗЭЭ Ч харуулахгүй: агуулгыг ижил хавтсанд `.khan-tmp-<hex>` түр файлд
  бичиж fsync хийгээд `os.Link`-ээр (no-replace, атомар) эцсийн нэр рүү гаргана, дараа нь түр нэрийг устгана. Нэр байвал `fs.ErrExist`
  (давхцлын шийдэл хэвээр). Алдаа/ctx цуцлалтад түр файл устна; crash хийвэл зөвхөн нуугдмал `.khan-tmp-*` үлдэнэ (Serve/ServePrivate
  цэгээр эхэлсэн нэрийг хэзээ ч үйлчлэхгүй; цэвэрлэх нь ops-ийн ажил). Hard link дэмжээгүй файлын системд `Lstat`+`Rename` fallback —
  зэрэгцээ хоёр Save яг нэг мөчид нэг нэр сонговол сүүлийнх нь дарах нарийн уралдаан боломжтой (Uploader нь санамсаргүй нэр ашиглах тул практикт бараг үгүй).
- Public `Serve` нь 0 байт файлыг үйлчилнэ ч `immutable` биш (`max-age=0, must-revalidate`) кэштэй — гэмтсэн хоосон файл жилээр кэшлэгдэхгүй.
- Signer алдаа (private URL гарын үсэг зурах) нуугдахгүй: `StoreFile` нь `Result` (Path бөглөгдсөн, URL хоосон) + алдаа буцаана,
  `Handler`/`PrivateHandler` нь 500 өгч лог бичнэ. **Файл диск дээр хадгалагдсан хэвээр** — клиент дахин оруулахын оронд
  `Result.Path`-аар `SignHandler`-ээс холбоос авч болно.

## Хязгаарлалт

- Холбоосыг хугацаа дуусахаас өмнө цуцлах боломжгүй (capability URL); богино TTL + `bind_user` ашигла. Хөтөч `max-age` хугацаанд кэшлэж болно.
- Storage интерфэйсэд Delete алга: private файл устгах/GDPR-ийг аппын тал (диск/S3) шийднэ.
- `Result.Path`-ийн дараа `Storage` нь public/private-д ТУСДАА байх ёстой; ижил backend-ийг хоёуланд нь өгвөл public Serve-д private файл гарч болно.
- Fiber/fasthttp `/../` ба хвостын `/`-ийг router-ээс өмнө цэвэрлэдэг тул ижил файл руу хүргэх хэлбэрүүд (зөв гарын үсэгтэй зам) ажиллана — гарын үсэг тухайн цэвэрлэсэн замд л хүчинтэй.
- Rate limit байхгүй (HMAC-ийг таах боломжгүй ч edge дээр тавихыг зөвлөнө).
