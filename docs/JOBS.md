# jobs — background job queue + cron scheduler

`jobs` package нь Postgres дээр суурилсан background job queue ба cron scheduler юм. `main`-ий модуль бүрт
тарсан cron-уудыг (`bootstrap.go` дахь `if !noCron { xxx.StartCron() }`, `modules/*/cron`, гараар бичсэн
`pg_try_advisory_lock`) нэг хэлбэрт оруулах зорилготой.

Одоогийн асуудал:

| Асуудал | `jobs`-ийн шийдэл |
|---|---|
| instance бүр дээр cron ажиллана, модуль бүр өөрийн advisory lock-ийг гараар бичнэ | tick бүр `khan.job_schedule_runs` PRIMARY KEY-ээр нэг instance-д л очно; lock бичих шаардлагагүй |
| алдаа лог дотор алга болно, дахин оролдлого байхгүй | бүх оролдлого `khan.jobs`-д (`last_error`, `attempts`, `dead`); backoff-тэй retry; `List/Retry/Cancel/QueueStats` |
| `KV_NO_CRON=1` бүгдийг зэрэг унтраана | Worker/Scheduler-ийг тусдаа процесс/instance-д ажиллуулна (доороос) |
| хүсэлт дундуур үүссэн ажил (имэйл, журнал) commit-оос өмнө/хойно алдагдана | `Enqueue` нь бизнес өгөгдлийн ТРАНЗАКЦДАА бичигдэнэ |

`events` (transactional outbox) -тэй ялгаа: `events` = "юу болсон" (domain event, олон handler сонсоно);
`jobs` = "энийг хий" (нэг эзэн, цаг товлох, retry, харах). Хоёулаа `FOR UPDATE SKIP LOCKED` дээр суурилсан.

## 1. Хүснэгт

`jobs.SchemaSQL` (идемпотент) -ийг app-ийн migration-д оруулна. Хоёр хүснэгт:

- `khan.jobs` — `id, queue, kind, org_id (NULL=систем), payload jsonb, priority, run_at, attempts, max_attempts,
  last_error, locked_by, locked_until (lease), status, unique_key, schedule, created_at, updated_at, finished_at,
  cancel_requested`.
  - `status`: `pending → running → succeeded | dead | canceled`. Алдсан job `pending` руу backoff-оор буцна.
  - `jobs_claim_idx (queue, priority DESC, run_at, id) WHERE status IN ('pending','running')`.
  - `jobs_unique_key_org_idx (queue, kind, COALESCE(org_id, '000…0'::uuid), unique_key) WHERE unique_key IS NOT NULL
    AND status IN ('pending','running')` — partial unique: идэвхтэй хооронд л, **байгууллага тус бүрд** (NULL=систем
    тусдаа) давхардлаас хамгаална; дууссаны дараа түлхүүр чөлөөлөгдөнө. `SchemaSQL` хуучин `jobs_unique_key_idx`
    (org-гүй)-ийг шинийг үүсгээд устгана (идемпотент шилжилт).
- `khan.job_schedule_runs (name, scheduled_for) PRIMARY KEY, job_id, note` — scheduler-ийн tick бүрийн "эзэмшил".

Хүснэгтэд RLS БАЙХГҮЙ (системийн хүснэгт). Org тусгаарлалт нь admin helper-ийн `Scope`-оор хийгдэнэ (§6).
`org_id` нь зөвхөн metadata/шүүлт; handler өөрөө `tenant.Tx(ctx, db, rec.OrgID, ...)`-ээр org-ийн контекстэд орно.

## 2. Enqueue

```go
err := gdb.Transaction(func(tx *gorm.DB) error {
    if err := tx.Create(&inv).Error; err != nil { return err }
    _, err := jobs.Enqueue(ctx, tx, jobs.Job{
        Kind: "billing.send_invoice", OrgID: orgID,
        Payload: map[string]string{"InvoiceID": inv.ID},
        // Queue: "default", Priority: 0, Delay: time.Minute / RunAt: t, MaxAttempts: 5, UniqueKey: "..."
    })
    return err
})
```

- `Enqueue` ЗААВАЛ транзакц дотор (`ErrNotInTx`) — rollback болбол job мөн алга болно.
- `UniqueKey`: `(queue, kind, org, unique_key)` pending/running хооронд давхцахгүй (өөр org ижил түлхүүр ашиглаж болно). `Enqueue` давхардалд **байгаа id-г**
  буцаана (алдаа биш); `EnqueueUnique` нь `created bool`-ийг мөн буцаана. `ON CONFLICT DO NOTHING` ашигладаг тул
  давхардал нь caller-ийн транзакцыг abort ХИЙХГҮЙ.
- Priority: ИХ тоо түрүүлнэ; дараа нь `run_at`, `id`.
- Kind: `billing.send_invoice` хэлбэр (events-ийн topic-той ижил дүрэм); queue: `[a-z0-9][a-z0-9_.-]{0,62}`.

## 3. Worker

```go
w := &jobs.Worker{DB: gdb, Queues: []string{"default", "mail"}, Concurrency: 8}
w.Register("billing.send_invoice", handler, jobs.WithTimeout(2*time.Minute))
go w.Run(ctx)       // ctx дуустал; RunOnce(ctx) = нэг багц, дуустал (тест/гараар)
```

`Handler func(ctx, Record) error`. `Record.Attempts` = энэ оролдлогын дугаар (1 = анхны), `Record.Decode(&v)`.

| Тохиргоо | Анхдагч | Утга |
|---|---|---|
| `Concurrency` | 4 | зэрэг ажиллах job |
| `PollInterval` | 1с | job байхгүй үед (job дуусахад шууд дахин авна) |
| `Lease` | 60с | авсан job-ийн түгжээ; heartbeat `Lease/3` тутам сунгана; Lease хугацаанд сунгаж чадаагүй бол handler цуцлагдана |
| `JobTimeout` | 10мин | нэг оролдлого (`WithTimeout` kind-аар дарна) |
| `ShutdownTimeout` | 30с | зогсоход in-flight job-ийг дуусгах хүлээлт |
| `Backoff` | `DefaultBackoff` | 5с·2^(n-1), дээд 1ц, ±20% jitter |

Зан төлөв:

- **Claim**: нэг SQL — `SELECT … FOR UPDATE SKIP LOCKED` CTE + `UPDATE … RETURNING`. Зөвхөн өөрийн `Queues` ба
  `Register` хийсэн kind-уудыг авна (бүртгэлгүй kind-ыг өөр сервис боловсруулна).
- **Lease/reclaim**: `status='running' AND locked_until < now()` job-ийг өөр worker шууд авна (унасан worker).
  `attempts` claim бүрд нэмэгддэг тул унаж байгаад л дахин ажиллуулдаг "poison" job `max_attempts`-д хүрээд
  `dead` болно (reap). Claim-ийн нөхцөл өөрөө `attempts < max_attempts AND NOT cancel_requested`-ийг шаарддаг тул
  reap болон claim хоёрын хооронд lease нь дууссан, оролдлого дууссан job дахин ажиллахгүй (`max+1` болохгүй).
- **Heartbeat**: handler ажиллах хооронд lease-ийг сунгана. Мөр fence-ээр олдохгүй болбол (өөр worker авсан)
  handler-ийн `ctx` `ErrLeaseLost` cause-оор цуцлагдана. Амжилттай сунгалт бүрт локал хугацаа = хүсэлт илгээсэн цаг +
  `Lease` (серверийн `locked_until`-аас үргэлж эрт). DB хүрэхгүй / pool дүүрсэнээс болж энэ хугацаа дуусвал
  өөр worker reclaim хийсэн байж болзошгүй тул handler-ийн `ctx` `ErrLeaseLost`-оор цуцлагдаж, үр дүн
  **хадгалагдахгүй** (`Stats().LeaseLost`); job lease дуусмагц reclaim болно. **Handler заавал `ctx`-ийг
  хүндэтгэх ёстой.** Heartbeat нь `Worker.DB`-ийн ижил холболтын pool-ыг ашигладаг: pool-ийн хэмжээг
  `Concurrency` + handler-уудын DB холболтоос том тохируул (heartbeat-д тусгай холболт нөөцлөгдөөгүй).
- **Fencing**: үр дүн бичих бүх UPDATE нь `WHERE id=? AND status='running' AND locked_by=? AND attempts=?` —
  lease-ээ алдсан (stale) worker шинэ эзний төлвийг дарж бичиж чадахгүй (`Stats().LeaseLost`).
- **Retry**: алдвал (эсвэл panic) `attempts < max_attempts` бол `pending` + `run_at = now()+backoff`; үгүй бол `dead`.
  `jobs.Permanent(err)` бол шууд `dead` (буруу payload гэх мэт).
- **Panic**: handler-ийн panic алдаа болж (stack-тай лог) retry-д орно; worker үргэлжилнэ.
- **Timeout**: нэг оролдлого `ErrJobTimeout` cause-оор цуцлагдаж алдаа гэж тооцогдоно.
- **Graceful shutdown**: `Run`-ийн `ctx` дуусахад шинэ job авахаа болино, in-flight-ийг `ShutdownTimeout` хүртэл
  хүлээнэ. Хэтэрвэл handler-ийн ctx `ErrShutdown`-оор цуцлагдаж, job **оролдлогод тооцогдохгүйгээр** (`attempts-1`,
  `run_at=now()`) буцаан чөлөөлөгдөнө. Handler ctx-ийг огт хүндэтгэхгүй гацвал `Run` +5с-ийн дараа алдаа
  буцаана; тэр job lease дуусмагц reclaim болно (хаалттай хамгаалалт нь lease).

Delivery нь **at-least-once**: lease алдагдсан/унасан тохиолдолд handler дахин ажиллана → **IDEMPOTENT** бич
(`Record.ID` нь dedupe түлхүүр болно).

## 4. Scheduler (cron)

```go
s := &jobs.Scheduler{DB: gdb, Schedules: []jobs.Schedule{
    {Name: "billing.nightly", Spec: "0 2 * * *", Kind: "billing.nightly", Queue: "default"},
    {Name: "qpay.poll", Spec: "@every 1m", Kind: "qpay.poll", SkipIfRunning: true},
}}
go s.Run(ctx)   // instance бүр дээр ажиллуулна — leader сонгох шаардлагагүй
```

**Spec**: 5 талбар (тоо нь зөвхөн ASCII цифр: `+5` гэх мэт татгалзагдана) `минут цаг өдөр сар гараг` (`*`, `?`, `1,5`, `1-5`, `*/10`, `10-30/5`, `5/15`, `JAN-DEC`,
`SUN-SAT`, гараг 0 ба 7 = Ням); `@yearly|@annually|@monthly|@weekly|@daily|@midnight|@hourly`;
`@every 10m` (>= 1с, epoch-д тулгуурласан → бүх instance-д ижил tick). Өдөр ба гараг хоёулаа хязгаарласан бол
Vixie cron-той адил **аль нэг** нь таарахад (OR). Хэзээ ч таарахгүй spec (`0 0 30 2 *`) эхэндээ татгалзагдана.

**TZ**: анхдагч `Asia/Ulaanbaatar` (UTC+8, DST-гүй; tzdata-гүй image-д тогтмол UTC+8-аар орлогдоно). DST-тэй
бүсэд spring-forward-д оршдоггүй цаг алгасагдана, fall-back-д давхардсан цаг нэг л удаа ажиллана.

**Яагаад яг нэг job**: tick-ийн цаг нь spec-ээс тооцогдох (instance-ийн эхэлсэн цаг/clock skew-ээс үл хамаарах)
тул бүх instance ижил `(name, scheduled_for)`-г олно. Нэг транзакцад `INSERT INTO job_schedule_runs … ON CONFLICT DO
NOTHING`; ялсан instance л (rows=1) job-ийг (мөн `unique_key = schedule:<name>:<unix>`) үүсгэнэ, бусад нь чимээгүй
алгасна. Job үүссэн эсэх нь tick-ийн эзэмшилтэй атомар. Дууссан job-ийг устгасан ч (`Purge`) `job_schedule_runs`
мөр үлдэх тул tick дахин үүсэхгүй.

**Missed tick** (`Schedule.Missed`): `Grace`-ээс (анхдагч 1мин) хуучирсан tick "өнгөрсөн".
- `MissedSkip` (анхдагч): өнгөрсөн tick-ийг алгасна; зөвхөн цагтаа ирсэн tick үүснэ.
- `MissedCatchUp` + `MaxCatchUp` (анхдагч 5): өнгөрсөнөөс хамгийн сүүлийн N-ийг нөхнө (`run_at` = tick-ийн цаг).
- Эхний удаа (түүх байхгүй) ажиллахад зөвхөн ирээдүйн tick. Restart-д cursor нь DB-ийн сүүлийн tick-ээс сэргэнэ.
- `Lookback` (24ц) -ээс өмнөх tick хэзээ ч нөхөгдөхгүй.

`SkipIfRunning`: өмнөх tick-ийн job pending/running бол энэ tick-ийг алгасна (`note` бичигдэнэ) — давхцахаас сэргийлнэ.
`Tick(ctx)` нэг удаа шалгана (тест/гараар); `Now` инжекцлэж болно. Нэг хуваарийн алдаа бусдыг зогсоохгүй,
алдсан tick-ийн cursor ахихгүй тул дараагийн Tick дахин оролдоно.

## 5. Observability

- Лог: `slog` (`Worker.Logger`/`Scheduler.Logger`, үгүй бол `obs.From(ctx)`); job бүрт `job_id, kind, queue,
  attempt, org_id`.
- `Worker.Stats()`: процесс доторх тоолуур (`Claimed, Succeeded, Retried, Dead, Panics, Timeouts, Released,
  LeaseLost, Canceled, InFlight`).
- `QueueStats(ctx, db, scope)`: queue тус бүрийн `Pending, Ready, Running, Succeeded, Dead, Canceled,
  OldestReadyAge, ExpiredLeases` — `OldestReadyAge` өсөх/`ExpiredLeases > 0` = worker дутуу/унасан дохио;
  `Dead > 0` = анхаарах job. `/healthz` эсвэл metrics-д холбоход тохиромжтой.

## 6. Admin helper ба org тусгаарлалт

`List/Get/Retry/Cancel/QueueStats` нь `Scope` ЗААВАЛ авна: `OrgScope(orgID)` (зөвхөн `org_id = orgID`; системийн
болон бусад org-ийн job харагдахгүй, гаднын id → `ErrNotFound`), `SystemScope()` (оператор). Тэг утга `ErrScope` —
JWT-ээс хоосон org_id гарвал бүх org-ийн job гарахаас сэргийлнэ. `org_id`-г JWT-ээс (`tenant`) ав, client-ээс биш.

- `Retry`: `dead|canceled → pending` (attempts=0); идэвхтэй ижил unique_key байвал `ErrDuplicate`.
- `Cancel`: `pending` (мөн lease нь дууссан `running`) → шууд `canceled`. Идэвхтэй `running` бол төлөв `running`
  хэвээр, `cancel_requested=true` тавигдаж **unique_key эзлэгдсэн хэвээр** үлдэнэ (хуучин handler ажиллаж байхад ижил
  түлхүүрээр шинэ job үүсэхгүй). Worker дараагийн heartbeat-д (≤ Lease/3) handler-ийн `ctx`-ийг `ErrCancelRequested`-оор
  цуцалж, handler зогсмогц job-ийг `canceled` болгоно (`Stats().Canceled`); handler ctx-ийг үл хайхран амжилттай
  дуусвал `succeeded`. Дууссан job-д `ErrState`; давтан Cancel аюулгүй. `Retry` нь `cancel_requested`-ийг арилгана.
- `Purge(ctx, db, olderThan, statuses...)`: анхдагчаар `succeeded|canceled`-ийг устгана (`dead`-ийг ЗӨВХӨН
  тодорхой заавал), мөн schedule-run-ийн сүүлийн мөрөөс бусдыг цэвэрлэнэ. Өдөр бүрийн schedule-ээр ажиллуул.

## 7. `main`-ийн cron-уудыг шилжүүлэх

1. **Migration**: `jobs.SchemaSQL`-ийг нэмнэ (`khan` schema; `events.SchemaSQL`-тэй зэрэгцээ).
2. **Нэг binary-д** `bootstrap.go`-д нэг Worker + нэг Scheduler эхлүүлнэ:
   ```go
   w := &jobs.Worker{DB: DB.DB, Queues: []string{"default"}, Concurrency: 8}
   registerJobHandlers(w)           // modules/*/jobs.go: w.Register("finance.xxx", ...)
   s := &jobs.Scheduler{DB: DB.DB, Schedules: allSchedules()}
   if !noCron { go w.Run(ctx); go s.Run(ctx) }
   ```
   `KV_NO_CRON=1` нь Worker+Scheduler-ийг л унтраана (одоогийн утга хэвээр). Ирээдүйд `KV_JOBS=worker|scheduler|off`
   гэх мэтээр тусад нь салгаж, web instance-ыг зөвхөн enqueue хийхээр, тусдаа процессыг worker болгож болно.
3. **Цикл тус бүр** (`StartCron()` доторх `time.Ticker` / `cron.AddFunc`):
   - Тухайн тик дээр хийдэг ажлын биеийг `func(ctx, jobs.Record) error` handler болго (алдаа БУЦААЖ, `log.Println`-оор
     залгихгүй; тусдаа `recover` хэрэггүй).
   - `pg_try_advisory_lock` + `defer unlock` блокыг УСТГА — scheduler tick-ийг нэг instance-д, worker job-ийг нэг
     worker-т өгнө. Хэрэв ажил олон org-оор давтдаг бол: (a) системийн нэг job дотор org бүрийг `tenant.SystemTx`-ээр
     давт (энгийн), эсвэл (b) tick-ийн job нь org бүрд `Enqueue(OrgID: …, UniqueKey: "kind:"+orgID+":"+tick)` fan-out хийж
     org бүрийн алдаа тусдаа харагдана (зөвлөмж).
   - Урт ажил (импорт, тайлан) `ctx.Done()`-г шалга; `WithTimeout` өг.
   - Хуваарь: `time.NewTicker(5*time.Minute)` → `{Name:"pkg.job", Spec:"@every 5m"}`; "өдөр бүр 02:00" →
     `"0 2 * * *"` (UB цаг). Давхцаж болохгүй (polling, reconcile) бол `SkipIfRunning: true`.
     Унтарсан үед нөхөх хэрэгтэй (сарын хаалт) бол `MissedCatchUp`.
4. **Шилжих дараалал** (аюулгүй): эхлээд шинэ хуваарийг `Queue` тусдаа, хуучин cron-тэй зэрэг ажиллуулахгүйн тулд
   хуучин `StartCron`-ыг feature flag-аар унтрааж шинийг асаа. Хоёулаа идемпотент бол богино хугацаанд зэрэг
   ажиллахад аюулгүй. Модуль нэг нэгээр (marketing, mentor, appointment, billing, school, food …).
5. **Дараа нь**: Dead/ExpiredLeases-ийг QueueStats-аар `/healthz` эсвэл admin хуудсанд гаргаж, `Retry/Cancel`-ийг
   admin API-д `OrgScope`-оор холбоно.

## 8. Хязгаарлалт

- Poll дээр суурилсан (LISTEN/NOTIFY байхгүй): enqueue-аас авах хүртэл ≤ `PollInterval` саатал.
- Scheduler-ийн нарийвчлал = `CheckInterval` (анхдагч 5с); cron-ийн хамгийн бага нэгж 1 минут.
- DST бүсэд оршдоггүй орон нутгийн цаг алгасагдана.
- Нэг job-ийн дэс дараалал/хамаарал (DAG, per-key serial) байхгүй; `UniqueKey` нь зөвхөн идэвхтэй хооронд dedupe.
- Handler-ийн зогсолт зөвхөн `ctx` хүндэтгэснээр хурдан; үгүй бол lease дуусах хүртэл reclaim хийхгүй.
- `khan.jobs` хүснэгт өсдөг — `Purge`-ийг тогтмол ажиллуул.
