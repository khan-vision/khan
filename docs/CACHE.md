# cache — дизайны гэрээ

`cache` package болон `crud`-ийн cache интеграцийн **нэг эх сурвалж**. Зорилго нь GraphQL/apollo-ийн client cache-ийг
REST дээр орлох явдал юм: давтан жагсаалт/дэлгэрэнгүй уншилтыг DB-гүйгээр хариулах, browser-т `304 Not Modified`
өгөх, бичилтийн дараа **хуучирсан өгөгдөл хэзээ ч харуулахгүй** байх.

## 1. Аюулгүй байдал ба зөв байдлын инвариантууд (тестээр батлагдана)

1. **Org тусгаарлалт**: cache түлхүүр бүр org_id-г агуулна. Нэг org-ийн хариу нөгөөд хэзээ ч очихгүй.
2. **Эрхээс хамаарах хэсэг cache-д орохгүй**: `can` map-ийг cache-ээс авсны дараа хүсэлт бүрт тооцож нэмнэ.
   Read эрх (`Can(c, read)`) cache-ийг уншихаас ӨМНӨ шалгагдана.
3. **Generation-based invalidation**: `(org, resource)` бүр generation тоолуурын утгатай. Түлхүүрт тухайн resource
   болон `DependsOn` resource-уудын generation орно. Бичилт generation-ийг нэмэгдүүлнэ, тиймээс хуучин түлхүүр
   хэзээ ч дахин уншигдахгүй (устгах шаардлагагүй, O(1)).
4. **Commit-ийн дараа bump**: crud нь write транзакц амжилттай commit болсны ДАРАА generation-ийг нэмнэ.
   Commit-оос өмнө нэмбэл зэрэгцээ уншигч хуучин өгөгдлийг шинэ generation-д хадгалах эрсдэлтэй.
   Бичсэн хүсэлт өөрөө bump-ийн дараа хариу өгдөг (read-your-writes). Commit ба bump хоорондох хэдэн мс-д
   зэрэгцээ уншигч хуучин cache-ийг харж болно. Bump амжилтгүй болбол логлоно; TTL нь хуучирлын дээд хязгаар болно.
5. **Fail-open уншилт**: Cache store (Redis) алдаа гарвал хүсэлт DB-ээс шууд үйлчлэгдэнэ (алдаа биш, лог).
   Generation уншиж чадахгүй бол cache-ийг алгасна (`X-Khan-Cache: bypass`, ETag/304 байхгүй). Буруу
   generation-аар cache-ээс уншихгүй. `Remember`-ээс буцсан алдаа нь ЗӨВХӨН `fn`-ийн алдаа.
6. **Хэмжээний хязгаар**: L1 нь entry болон байтаар хязгаарлагдсан LRU. Нэг хариу `MaxEntryBytes`
   (анхдагч 1 MiB)-ээс том бол cache-д хийхгүй.
7. **Stampede**: ижил түлхүүрийг зэрэг асуувал `singleflight`-аар НЭГ л DB query явна.

## 2. Package `cache`

```go
type Store interface { // L2 (ж: Redis) эсвэл L1
    Get(ctx context.Context, key string) ([]byte, bool, error)
    Set(ctx context.Context, key string, val []byte, ttl time.Duration) error
}
type GenStore interface { // generation тоолуур
    Gen(ctx context.Context, keys []string) ([]uint64, error) // байхгүй бол 0
    Bump(ctx context.Context, keys []string) error             // түлхүүр бүрийг атомаар +1
}

type Config struct {
    L1 *Memory        // nil → NewMemory(MemoryConfig{}) (анхдагч 10k entry, 64 MiB)
    L2 Store          // optional (Redis); nil → зөвхөн L1
    Gens GenStore     // nil → L2 нь GenStore бол тэр (Redis), L2 байхгүй бол in-process MemoryGens
    DefaultTTL time.Duration // анхдагч 5 мин
    MaxEntryBytes int
    Prefix string     // анхдагч "khan"; `[a-z0-9_.-]+`
    Logf func(string, ...any)
    Now func() time.Time // тест; nil → time.Now
}
func New(cfg Config) (*Cache, error)

func (c *Cache) Bump(ctx context.Context, orgID string, resources ...string) error
func (c *Cache) Version(ctx context.Context, orgID string, resources ...string) (string, error) // "g12.g3" — ETag/түлхүүрт
func (c *Cache) Remember(ctx context.Context, k Key, ttl time.Duration, fn func(ctx context.Context) ([]byte, error)) (val []byte, hit bool, err error)
type Key struct{ OrgID, Resource string; DependsOn []string; Parts []string } // Parts = нормчилсон хүсэлт (route, query/body)

// crud-д зориулсан (generation-ийг ХОЁР удаа уншихгүйн тулд):
func (c *Cache) Resolve(ctx context.Context, k Key) (Resolved, error)          // Resolved.Version + бүрэн түлхүүр
func (c *Cache) RememberResolved(ctx context.Context, r Resolved, ttl time.Duration, fn ...) ([]byte, bool, error)
func (c *Cache) Epoch(ttl time.Duration) string                                // ETag-д: now/ttl
func (c *Cache) Stats() Stats                                                  // Hits, L2Hits, Misses, Errors, Sets, TooLarge, Coalesced, Bumps
func ETag(version string, parts ...string) string
func ETagMatch(ifNoneMatch, etag string) bool
func BumpOnEvents(c *Cache) events.Handler
```

- **`New`**: `L2` өгсөн боловч хуваалцсан `GenStore` байхгүй (`Gens` nil ба L2 нь `GenStore` биш) бол **алдаа** —
  процесс бүрийн тусдаа generation нь хуваалцсан L2-той хамт хуучирсан өгөгдөл (мөн restart-ийн дараа generation
  0-ээс эхлэх) өгнө.
- **`Remember`**: store/generation/буруу Key зэрэг cache-ийн ЯМАР Ч алдаа `fn`-ийг дуудаж хариу өгөхөд саад болохгүй
  (лог + `Stats().Errors`); буцсан `err` нь ЗӨВХӨН `fn`-ийн алдаа. `fn`-ийн алдаа хэзээ ч cache-д орохгүй. Singleflight
  нь `Do`-оор (удирдагчийн goroutine-д синхрон) — `fn` нь дуудагч handler-ийн хугацаанд л ажиллана (fiber.Ctx-ийг
  goroutine-оос гадуур ашиглахгүй). Удирдагчийн ctx цуцлагдсанаас дагагчид `context.Canceled/DeadlineExceeded` авбал
  өөрийн ctx хэвийн бол нэг удаа (дахин нэгтгэгдэн) өөрөө оролдоно. Буцсан `[]byte`-ийг ЗАСАЖ БОЛОХГҮЙ (хуваалцсан).
- **`Memory`** (`MemoryConfig{MaxEntries, MaxBytes, Shards, Now}`): dependency-гүй, sharded (анхдагч 16) LRU + TTL,
  entry/байтын хязгаартай (shard бүрд `ceil(max/shards)`), `-race` аюулгүй. `Set` нь өгөгдлийг хуулна; shard-ийн байтын
  хязгаараас том entry хадгалагдахгүй (өмнөх утга байсан бол устгана). In-process generation: `MemoryGens`.
- **`cache/rediscache`** (дэд package): `github.com/redis/go-redis/v9` (`redis.UniversalClient`). Store (`SET key val PX ms`,
  `GET`) болон GenStore (`INCR`, `GET` — нэг pipeline; `MGET` биш: cluster-ийн CROSSSLOT-оос сэргийлнэ).
  **Олон instance**: generation нь Redis-д байх тул instance бүр шууд шинэ generation-ийг харна, процесс дотор memo
  хийхгүй. Generation түлхүүр TTL-гүй тул `maxmemory-policy`-г `volatile-*`/`noeviction` болго (`allkeys-*` generation-ийг
  гаргавал тоолуур 0 болж хуучин entry дахин «хүчинтэй» болно). Тест нь `miniredis` ашиглана.
- Түлхүүрийн хэлбэр: `<prefix>:v1:<org>:<resource>:<gens>:<sha256(deps+parts)>`. `<gens>` = `Version` ("g12.g3", эхлээд
  `Resource`, дараа нь эрэмбэлсэн `DependsOn`). Хэш нь `DependsOn` нэрсийг мөн агуулна (deploy-ийн дундуур dependency
  өөрчлөгдөхөд gens ижил тоо байсан ч хуучин entry-тэй мөргөлдөхгүй); хэсэг бүр урт-угтвартай (`["a","bc"]` ≠
  `["ab","c"]`). Generation-ий түлхүүр: `<prefix>:gen:<org>:<resource>`. Org/resource нь `[a-z0-9_.-]` (uuid,
  `finance.invoice`); бусад байтыг (`:`, `%`, том үсэг, UTF-8...) `%XX` болгож escape хийнэ. Хоосон org/resource →
  `ErrInvalidKey` (cache алгасна).

## 3. HTTP — ETag / 304

`cache.ETag(version, parts...) string` → `W/"<version>-<hash16>"` (hash16 = parts-ийн sha256-ийн эхний 8 байт). crud
GET handler-ууд:

- `ETag`, `Cache-Control: private, no-cache` (browser хадгална, гэхдээ ҮРГЭЛЖ revalidate хийнэ),
  `Vary: Authorization, Cookie` тавина. Generation уншиж чадаагүй (bypass) бол ETag тавихгүй.
- ETag-ийн parts: `org, resource, can-ийн хэв (read,create,update,delete → "1011"), TTL epoch, route/нормчилсон хүсэлт`.
  Org-г заавал оруулна (өөр org-ийн ETag таарахгүй). `can` нь эрхийн хэвийг агуулдаг тул эрх өөрчлөгдвөл ETag өөрчлөгдөнө.
  **TTL epoch** (`Cache.Epoch(ttl)` = `now/ttl`) нь `bump`-гүй өөрчлөлт (crud-ийн гадуурх шууд бичилт) 304-өөр хязгааргүй
  нуугдахаас сэргийлнэ: ETag ~TTL тутамд шинэчлэгдэх тул хуучирлын дээд хязгаар (~2×TTL) 304-д ч хүчинтэй.
- `If-None-Match` нь одоогийн ETag-тай таарвал (weak харьцуулалт, таслалаар жагсаасан) DB-д хүрэхгүйгээр **304**
  (body-гүй). **Зөвхөн GET/HEAD**: `POST /query` нь ETag-тай боловч RFC 9110-ын дагуу 304 болохгүй (200). `If-None-Match: *`
  үл тооно. Read эрх (`Can`) 304-ээс ӨМНӨ шалгагдана.
- Browser нь XHR GET-д `If-None-Match`-ийг өөрөө илгээдэг тул **front_end-д код өөрчлөх шаардлагагүй**.

## 4. crud интеграци

```go
Resource.Cache *crud.CacheSpec
type CacheSpec struct {
    Cache     *cache.Cache
    TTL       time.Duration // 0 → Cache.DefaultTTL
    DependsOn []string      // жагсаалт/detail/options-д JOIN/ref хийгддэг өөр resource-ийн Name (тэдгээрийн бичилт энэ cache-ийг хүчингүй болгоно)
    Bumps     []string      // энэ resource-ийн бичилт өөр resource-ийн cache-ийг мөн хүчингүй болгоно
    NoETag    bool
}
```

| Route | Cache |
|---|---|
| `GET /`, `POST /query` | Body cache + ETag. Түлхүүрт нормчилсон хүсэлт (page/limit/search/sort/dir + filters-ийн тогтвортой JSON; GET ба POST ижил хүсэлтэд ижил түлхүүр). `can` cache-д ОРОХГҮЙ: body-д хүсэлт бүр дээр нэмэгдэнэ |
| `GET /:id` | Body cache + ETag (404 cache-д орохгүй) |
| `GET /options/:field` | Body cache + ETag. Түлхүүрт `field, search, parent, ids, limit`. 200 биш (400/500) хариу cache-д орохгүй |
| `GET /export.xlsx`, `/meta` | Cache хийхгүй |
| POST/PUT/DELETE/bulk | Commit-ийн дараа `Bump(org, Name, Bumps...)`; амжилтгүй/rollback бичилт bump хийхгүй |

- Дараалал: org (401) → `Can(read)` (403) → хүсэлт parse (400) → cache. Cache-д орсон хариу нь cache-гүй хариутай
  байт-байтаар ижил (decimal overlay, children, `filterable` ... `can` нь цагаан толгойн эхэнд тул encoding/json-ий
  эрэмбэтэй ижил).
- 4xx/5xx хэзээ ч cache-д орохгүй (`fn` алдаа буцаана → хадгалагдахгүй), мөн ETag/`X-Khan-Cache` тавигдахгүй.
- `RLS: true` үед ч мөн адил (түлхүүрт org байгаа; унших нь tenant.Tx дотор).
- **Options**: spec-д «`Ref.Resource` заасан бол» гэсэн боловч `lookup.Spec`-д тийм талбар байхгүй. Иймд options нь энэ
  resource-ийн `Name` + `DependsOn`-ийн дор cache-лэгдэнэ: ref хүснэгтийн resource-ийг `DependsOn`-д заавал бич.
- Cache-д ОРОХ хариу нь зөвхөн org + хүсэлт (+ `can`)-аас хамаарах ёстой. `ListQuery`/`DetailQuery` нь хэрэглэгчээс
  хамаарсан мөр буцаадаг resource-д Cache тавьж болохгүй.
- Hook дотор өөр хүснэгт бичдэг бол тэр resource-ийг `Bumps`-д заа. Bulk нь commit амжилттай бол үргэлж bump хийнэ.
- Bump нь handler-ийн timeout-оос салгасан (`context.WithoutCancel` + 5с) ctx-ээр ажилладаг: commit болсон бол bump
  timeout-оос болж алдагдахгүй. Bump алдвал бичилт амжилттай хэвээр, алдаа логлогдож TTL хязгаарлана.
- Crud-аас ГАДУУРХ бичилт (service код, cron) нь `cache.Bump(ctx, org, "finance.invoice")`-ийг өөрөө дуудна.
  Эсвэл `events` dispatcher-т `d.Handle("finance.*", cache.BumpOnEvents(c))` бүртгэж, `<resource>.<action>` event бүрд
  (`finance.invoice.posted` → `finance.invoice`) bump хийнэ. Event бичилттэй нэг транзакцад орох тул найдвартай
  (at-least-once, bump idempotent). org-гүй event-ийг алгасна.
- Хариуны header: `X-Khan-Cache: hit|miss|bypass` (оношлогоонд). `bypass` = generation уншигдсангүй (Redis унасан).

## 5. Хязгаар

Cache нь eventual биш, «generation-ий дараа шууд» тогтвортой байна. `DependsOn`-д заагаагүй хүснэгтийг JOIN
хийсэн бол тухайн өөрчлөлт TTL дуустал харагдахгүй. Тиймээс TTL-ийг богино (анхдагч 5 мин) байлгаж, `DependsOn`-ийг
нарийн тодорхойлно. Bump амжилтгүй болсон (Redis унасан) тохиолдолд Redis сэргэсний дараа хуучин generation-тай entry-үүд
TTL дуустал харагдаж болно. 1 MiB-аас том хариу cache-д орохгүй (ETag/304 харин ажиллана).

## Аюулгүй байдлын review-ийн засвар (2026-11-06)

| Олдвор | Засвар |
|---|---|
| Cache-гүй dependency resource-ийн бичилт `DependsOn`-той cache-ийг хүчингүй болгодоггүй | `CacheSpec.BumpOnly`: уншилтыг cache хийхгүй, бичилтийн дараа зөвхөн bump хийнэ |
| COMMIT алдаа (холболт тасарсан, timeout) үед сервер commit хийсэн байж болох ч bump хийгддэггүй | Транзакц дотор (`fn`) гарсан алдаа rollback-ийг баталгаатай болгоно. `fn` амжилттай болсон хойно гарсан транзакцын алдааг `commitError` болгож, ийм үед bump хийнэ (илүү bump хор хөнөөлгүй) |
| Restart/flush-ийн дараа generation 0-ээс эхлэхэд хуучин ETag-аар 304 авч болно | `Config.Salt` (build id). Process доторх `MemoryGens` үед boot nonce автоматаар нэмэгдэнэ. Version нь `s<8hex>.g…` хэлбэртэй |
| Хэвшүүлээгүй түлхүүр (`limit=99999`, `page`, урт `search`) LRU-г шахна | `query.Spec.Normalized`: query-ийн өөрийн дүрмээр хэвшүүлнэ. 200 тэмдэгтээс урт хайлт эсвэл 1000-аас хол хуудсыг cache хийхгүй |
| Хуваалцсан L2 + process доторх `MemoryGens` хослолыг хүлээн авдаг | `New` алдаа өгнө (`AllowLocalGens`-ээр л зөвшөөрнө) |

**Мэдэгдэж буй хязгаар:** Ижил `Name`-тэй хоёр өөр resource нэг Cache-ийг хуваалцвал түлхүүрийн орон зай нь давхцана, тиймээс Name нь давтагдашгүй байх ёстой. Leader-ийн context цуцлагдвал follower-ууд нэг удаа өөрсдөө дахин оролдоно.
