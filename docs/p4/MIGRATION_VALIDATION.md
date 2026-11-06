# P4 migration validation (local scratch, 2026-11-06)

Орчин: LOCAL PG 18.3, scratch DB `khan_p4_scratch_*` (khan_vision_test-ийн schema-only хуулбар + синтетик өгөгдөл). Prod/remote DB-д ХАНДААГҮЙ. `khan migrate up` (lock_timeout 5s, statement_timeout 15m) ашигласан; 40 файл (0101..0502) нэг хавтсанд цуглуулж ажиллуулсан.

## Owner шийдвэр (2026-11-06): public.users
`public.users`-д org_id нэмэхгүй; tenant scope = `organization.organization_users` (org_id, user_id, access, owner_via_job_id) / `organization.employee` JOIN. Ямар ч migration public.users-д backfill/NOT NULL/RLS хийхгүй (`grep -rn 'public\.users' migrations` -> зөвхөн _pending_owner/README-д тайлбар). ORG_ID_GAPS.md-д тусгасан.

## Үр дүн (группээр)
| Group | Үр дүн |
|---|---|
| 1 organization-hr (0101-0119) | PASS (засвартай) |
| 2 item-inventory-sale (0201-0215) | PASS (засвартай) |
| 3 attendance (0301-0311) | PASS |
| 4 party (0401-0408) | PASS relationship; individual -> pending (0410) |
| 5 timestamps (0501, 0502) | PASS; 0502 шинэ (цоорхой) |
Дахин `migrate up` = "0 migration хэрэглэгдлээ" (idempotent). `khan schema lint` -> org-id/timestamps зөрчил арилсан, үлдсэн нь зөвхөн deleted_at (санаатай), party.individual + 2 HYBRID org_id NOT NULL (pending), finance.salary_increment money numeric(100,2) (P4-ээс гадуур).

## Олсон алдаа / засвар
1. **NOT VALID CHECK (org_id IS NOT NULL) нь expand-ийн дараа хуучин апп-ын INSERT-ийг шууд унагана** (апп org_id бичдэггүй). Засвар: expand файлд BEFORE INSERT trigger `p4_<table>_org_id_derive` (эцгээс org_id дүүргэнэ). Шалгасан: INSERT -> org_id автоматаар.
2. **Backfill `ctid IN (subquery LIMIT)`** нь индексгүй том хүснэгтэд O(n^2). Засвар: ctid блок-муж (TID Range Scan, 200 блок/batch, batch бүр COMMIT). Plan баталгаажсан.
3. **Orphan мөр VALIDATE-д ойлгомжгүй алдаагаар унадаг** -> validate файл бүрийн эхэнд GATE (DO): org_id NULL үлдсэн хүснэгт+тоо-г RAISE EXCEPTION-оор мэдээлнэ. Давхардсан/NULL id gate (UNIQUE INDEX-ээс өмнө). Cross-org relationship = WARNING.
4. **CREATE UNIQUE INDEX CONCURRENTLY lock_timeout/cancel-д INVALID index үлдээнэ; `IF NOT EXISTS` retry нь алгасаж "OK" гэнэ**, дараа нь PK нэмэхэд унана. Засвар: notnull файлд INVALID-index gate (гар засвар: DROP INDEX CONCURRENTLY + CREATE UNIQUE INDEX CONCURRENTLY, дараа нь migrate up).
5. **party.individual**: NOT VALID CHECK/RLS нь saveWithParty (individual-г party-аас өмнө, org_id-гүй бичдэг)-ийг эвддэг; олон org-д хуваалцсан/party-гүй individual NULL үлдэнэ. Засвар: 0401-д зөвхөн багана, 0402 backfill (зөвхөн ганц org-той), NOT NULL/RLS -> `_pending_owner/0410`. 0403/0405/0408-аас individual хасав.
6. **0501 нь org_id нэмсэн child хүснэгтийн created_at/updated_at-г оруулаагүй байсан** -> 0502 нэмэв (sub_shift, employee_extra_*, sub_employee_*, item_details, sale_order_surcharge, inventory_discrepancy_..._employees, relationship, lut_attendance_request, lut_inventory_tag).
7. Backfill дотор COMMIT-тай DO нь `-- khan:no-transaction`-д ажилласан (OK). SQL синтакс алдаа олдоогүй.

## Lock-ийн зан төлөв (хэмжсэн)
- `ROW SHARE` түгжээ (SELECT FOR SHARE) барьсан session байхад 0201 (ALTER TABLE ADD COLUMN): **5s-д "canceling statement due to lock timeout"**, бүхэлдээ rollback (org_id багана үүсээгүй), түгжээ суларсны дараа дахин ажиллуулахад ✓. Аюулгүй retry.
- Нээлттэй write tx (өөр хүснэгт дээр) байхад CIC (0204): lock timeout-оор унаж INVALID index үлдсэн (дээрх #4) -> CIC нь DB-ийн бүх хуучин tx-ийг хүлээдэг. Prod-д урт tx/идэвхгүй session-гүй цонхонд ажиллуул.
- Нэг tx-д олон ALTER (0101, 0501, 0502): lock-ууд tx дуустал барьгдана (ms); том хэмжээнд ч богино. Унавал бүгд rollback.
- RLS: scratch role superuser тул policy-ийн ЭФФЕКТ шалгаагүй; зөвхөн policy/FORCE байгааг шалгав (verify #8). Prod апп role NOBYPASSRLS эсэхийг эхлээд шалга.

## Backfill хэмжээний туршилт
sale.sale_order_line 1.51M мөр (312MB, локал laptop): 0202 backfill = **4m52s** (~5k мөр/с, шугаман; batch бүр COMMIT тул бичилтийг хаахгүй, гэхдээ ~2x bloat -> дараа нь VACUUM). Prod-ийн мөрийн тоогоор экстраполяц хий.

## Verification SQL (prod-д ашиглах)
`docs/p4/verify_p4.sql` (зөвхөн SELECT): #1 NULL org_id, #2 эцэгтэй org зөрүү, #3 cross-org relationship, #4 individual (shared/no_party/null), #5 давхардсан id, #6 INVALID index, #7 validate хийгдээгүй *_org_id_nn CHECK, #8 NOT NULL/PK/RLS/policy/trigger төлөв, #9 timestamps дутуу. Scratch дээр бүх migration дараа: #1,#2,#5,#6,#7 = OK/хоосон; #3 REVIEW (1, синтетик); #4 individual: shared=1,no_party=1,null=2 (синтетик; pending 0410-ийн нөхцөл).

## Prod-д хэрэглэх урсгал
prod_counts.sql -> orphan-ыг owner шийдэх -> `migrate up` (gate унавал тоог харж цэвэрлэ, дахин ажиллуул - retry аюулгүй) -> verify_p4.sql. Backfill NULL-ыг АВТОМАТААР устгадаггүй (scratch дээр туршихдаа гараар DELETE хийсэн; prod-д owner шийднэ).

## Нээлттэй асуудал
- Дугаарлалт: 0101.. нь main-ийн сул хувилбар руу дахин дугаарлагдана (файл бүрийн толгойд).
- party.individual (0410), HYBRID хоёр хүснэгт, organizations generated org_id (0601): owner шийдвэр.
- trigger-уудыг апп org_id бичдэг болсны дараа хасах эсэх.
- public.users: ШИЙДСЭН (дээр).
- finance.salary_increment numeric(100,2) -> numeric(18,2) (P4-ээс гадуур).
- Pre-existing NOT VALID FK: sale.sale_order.sale_order_sales_team_fk (P4-д хамаагүй).
- Эрсдэл/хүснэгт (prod_counts.sql-ийн мөрийн тоог бөглөх; test DB бараг хоосон):
| Хүснэгт | Backfill | Lock (expand/NOT NULL/RLS) | Эрсдэл |
|---|---|---|---|
| sale.sale_order_line | ХАМГИЙН ИХ (~5k мөр/с; мөр=?) | богино ACCESS EXCLUSIVE; VALIDATE бүтэн scan (бичилтийг хаахгүй) | Өндөр: хугацаа, WAL, bloat (хүснэгт ~2x) |
| sale.sale_order_surcharge, item.item_details | жижиг/дунд (мөр=?) | богино | Бага; item_details PK-гүй -> dup id |
| organization.employee_extra_*, public.sub_employee_* | жижиг (мөр=?) | богино | Бага; sub_employee employee_id NULL=orphan; int8 id |
| attendance.sub_shift, inventory..._employees | жижиг (мөр=?) | богино | Бага |
| party.relationship | жижиг (мөр=?) | богино | Дунд: from_party_id NULL, cross-org |
| party.individual | дунд (мөр=?) | - | Дунд: shared org, NOT NULL pending |
| 0501/0502 timestamps | metadata-only (PG11+) | богино ACCESS EXCLUSIVE x ~35 хүснэгт нэг tx | Бага; хуучин мөрийн created_at=migration цаг |
