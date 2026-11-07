# P4 W0 - prod runbook (org_id / timestamps, G1-G5)

Төлөв: local scratch дээр батлагдсан, **prod-д ажиллуулаагүй**. Ажиллуулахаас өмнө owner/CTO зөвшөөрөл заавал. Prod DDL = энэ runbook-ийн дагуу л.
Баримт: `MIGRATION_VALIDATION.md`, `ORG_ID_GAPS.md` ("Prod баталгаажуулалт"), `verify_p4.sql`, `prod_counts.sql`.

## 0. Хамрах хүрээ (W0)
| Group | Файлууд | Юу хийнэ |
|---|---|---|
| 1 | 0101-0113 | employee_extra_{phones,emails,shifts}: org_id + CHECK + trigger + backfill + NOT NULL + idx. public.sub_employee_*: org_id (users.org_id) + trigger + backfill + idx (NOT NULL ҮГҮЙ). sub_employee_skill/job_experience PK(id). |
| 2 | 0201-0209 | sale_order_line, sale_order_surcharge, inventory_discrepancy_responsibility_employees: full. item_details: org_id + trigger + backfill + PK(id) + idx (NOT NULL ҮГҮЙ). |
| 3 | 0301,0302,0305 | attendance.sub_shift: org_id + trigger + backfill + idx (NOT NULL ҮГҮЙ). |
| 4 | 0401-0405,0407 | party.relationship: full + PK(id). party.individual ОРООГҮЙ. |
| 5 | 0501,0502 | created_at/updated_at (`DEFAULT now() NOT NULL`, metadata-only). deleted_at ОРООГҮЙ. |
ОРООГҮЙ: RLS (бүгд), public.users, party.individual, HYBRID хоёр, organizations generated org_id -> `migrations/_pending_owner/`.
**Дугаар**: 0101.. нь main-ийн дараагийн сул хувилбар руу дахин дугаарлана (харьцангуй дараалал хадгал; gap зөвшөөрөгдөнө). `khan.schema_migrations` prod-д ОДОО байхгүй - runner үүсгэнэ (checksum drift хамгаалалттай; хэрэглэсэн файлыг дараа нь БҮҮ засах).

## 1. Урьдчилсан шалгалт (зөвхөн SELECT, `SET default_transaction_read_only=on`)
1. `prod_counts.sql` бүгдийг ажиллуул. Хүлээгдэх (2026-11-07): orphan = item_details 3, sub_shift 5, бусад 0 (sub_employee_* нь users-тэй 26/26 таарна); `relationship_from_party_without_org` = 0.
2. Хэрэв clean хүснэгтэд (employee_extra_*, sale_order_line, sale_order_surcharge, discrepancy_*_employees, relationship) orphan >0 болсон бол ЗОГС - gate 0103/0203/0403-д унана (устгахгүй; доор §6).
3. Дараахийг шалга: (a) `select count(*) from pg_stat_activity where xact_start < now()-interval '1 minute' and state<>'idle'` = 0 (CIC бүх хуучин tx-ийг хүлээнэ, 0104/0105/0204/0404 + idx файлууд), (b) `idle in transaction` = 0, (c) runner role = хүснэгтүүдийн эзэн (`doadmin`), (d) `khan.schema_migrations` байхгүй/өмнөх хувилбар.
4. Pre-snapshot (rollback-д хэрэгтэй): 0501/0502-ийн хүснэгтэд created_at/updated_at ӨМНӨ нь байсан эсэх - `verify_p4.sql #9`-ийн гаралт + `select table_schema,table_name,column_name from information_schema.columns where column_name in ('created_at','updated_at') and (table_schema,table_name) in (...)`-ийг файлд хадгал. 2026-11-07: 7 жишиг хүснэгтэд байхгүй байсан. Урьд нь байсан багана rollback-д DROP хийхгүй.
5. Нам цонх (оройн цаг); DB backup/snapshot байгааг баталгаажуул (DO managed DB). Апп код ӨӨРЧЛӨХ шаардлагагүй (trigger org_id-г дүүргэнэ).

## 2. Ажиллуулах
```
# RTX/jump host: git-ээс хуулсан migrations/*.sql (НЭГ хавтас, _pending_owner/ ҮГҮЙ эсвэл үл тоомсорлогдоно)
khan migrate status -dir DIR -env prod.env           # зөвхөн унших
khan migrate up -dir DIR -env prod.env -lock-timeout 5s -statement-timeout 15m
```
Runner: файл бүр өөрийн tx + `SET LOCAL lock_timeout=5s`; `-- khan:no-transaction` файл (backfill DO, CIC) tx-гүй. Эхний алдаанд зогсоно; дахин ажиллуулахад дууссан хувилбараас үргэлжилнэ (aюулгүй retry).
Зөвлөмж: group бүрийн дараа зогсоод `verify_p4.sql` ажиллуул (§4). Заавал 0101->0102 (expand->backfill), 0201->0202, 0301->0302, 0401->0402-ийг завсаргүй (нэг `migrate up`-д) - expand-ийн NOT VALID CHECK нь backfill хийгдээгүй мөрийн UPDATE-ийг тэр цонхонд татгалзана (мөр цөөн тул секунд).

### Файл бүрийн хүлээгдэх хугацаа (prod тоогоор; scratch дээр бүгд < 15 ms, 1.5M мөрийн sale_order_line backfill = 4m52s -> prod 475 мөр = < 1 с)
| Файл | Lock | Хугацаа | Анхаар |
|---|---|---|---|
| 0101,0201,0301,0401 expand | ACCESS EXCLUSIVE богино (ADD COLUMN/CHECK/TRIGGER), нэг tx-д 7/4/1/1 хүснэгт | < 1 с | lock_timeout 5s-д унавал бүгд rollback, дахин ажиллуул |
| 0102,0202,0302,0402 backfill | ROW EXCLUSIVE (UPDATE) батч бүр COMMIT | < 1 с (sale_order_line 475, sub_shift 1286, relationship 4406) | дараа нь `VACUUM (ANALYZE)` шаардлагагүй (жижиг) |
| 0103,0203,0403 gate+VALIDATE | SHARE UPDATE EXCLUSIVE | < 1 с | gate унавал §6 |
| 0104,0105,0204,0404 `CREATE UNIQUE INDEX CONCURRENTLY` | SHARE UPDATE EXCLUSIVE; хуучин tx-ийг хүлээнэ | секунд (урт tx байвал удна) | унавал INVALID index - 0106/0205/0405 gate барина (§6) |
| 0106,0205,0405 SET NOT NULL + PK USING INDEX | ACCESS EXCLUSIVE, table scan | < 1 с (хамгийн их 4406 мөр) | |
| 0107-0113,0206-0209,0305,0407 `CREATE INDEX CONCURRENTLY` | SHARE UPDATE EXCLUSIVE | секунд | |
| 0501 (≈35 хүснэгт), 0502 (≈13 хүснэгт) | ACCESS EXCLUSIVE x N нэг tx-д (tx дуустал хадгалагдана) | < 2 с, lock хүлээлтээс хамаарна | pooler/ажлын цагт бүү ажиллуул; унавал бүгд rollback, retry |
Нийт хүлээлт: ~1 минут дотор (CIC-ийн хуучин tx хүлээлтээс бусад).

## 3. Хүлээгдэх төлөв (migrate up дараа)
- 7 хүснэгт (employee_extra_x3, sale_order_line, sale_order_surcharge, discrepancy_*_employees, relationship) = `org_id NOT NULL`, CHECK устсан.
- sub_employee_*: org_id nullable, users.org_id-ээр бөглөгдсөн (prod 26/26 ≥ нэг утга). item_details 12/15, sub_shift 1281/1286 бөглөгдөнө; үлдсэн 3 + 5 = NULL (orphan, УСТГААГҮЙ).
- Бүх 13 хүснэгтэд `p4_<t>_org_id_derive` BEFORE INSERT trigger.
- RLS ХЭЗЭЭ Ч идэвхжээгүй.

## 4. Verify
`psql -f docs/p4/verify_p4.sql` (SELECT л). Хүлээгдэх: #1 OK ба PENDING (item_details=3, sub_shift=5); #2 бүгд OK; #3 INFO; #3b OK; #5 OK; #6, #7 хоосон; #8 CHECK-тэй 7 хүснэгт org_id_notnull=t, derive_trg=t, rls=f; #9 хоосон. FAIL мөр байвал §6/§7.
Апп smoke (read-only + 1 зөвшөөрөлтэй бичилт): ажилтан нэмэлт утас хадгалах, sale order sub-line хадгалах, харилцагч нэмэх (relationship) - org_id автоматаар дүүргэгдэж байгааг `select org_id` -ээр харах (тест org: `test-orgs/khankhulgun.md`).

## 5. Апп-д нөлөө (INSERT-ийн шинжилгээ, `main/` кодоос)
Бүх INSERT нь багана жагсаалттай эсвэл GORM struct/map (org_id талбаргүй) -> trigger дүүргэнэ; positional INSERT олдсонгүй.
- relationship: `modules/organization/controllers/CrmRegisterController.go:174,278`, `ContactImportController.go:294,356`, `modules/appointment/services/party_link.go:90,115`, `modules/organization/registry/registry.go:234`, `PartyController.go:54,121` (GORM Create). **Анхаар**: from_party нь organization төрөлтэй (org_id≠NULL) байх ёстой - individual party-аас гарсан relationship INSERT NOT NULL-д унана (prod-д 0 тохиолдол; 4406/4406 org-аас).
- sale_order_line/surcharge: `modules/sale/controllers/SOController.go:~440-456` (эцэг tx-д өмнө нь орсон -> trigger харна), `QuickSaleController.go:~602`, Lambda form 1854 SubForm. sale_order.org_id NOT NULL тул дүүрнэ.
- sub_shift: `modules/attendance/controllers/ApiController.go:25` CreateSubShift (shift өмнө нь үүссэн), `modules/hr/shift/*`.
- item_details: `modules/item` ItemCrudController (PackagingRow), Lambda form 1538. employee_extra_*: Lambda form 1513/1746. sub_employee_*: Lambda 1587-1590 (parent=users.id). discrepancy_*_employees: Lambda 1778.
- Lambda `SELECT *`/GraphQL нэмэлт багана (org_id, created_at, updated_at) нь struct-д байхгүй бол үл тоомсорлогдоно.
- Эрсдэл: (1) relationship дээрх дээрх нөхцөл; (2) chk хүснэгтийн эцэг org_id NULL бол (бүгд NOT NULL тул байхгүй) INSERT унана; (3) sub_employee_* trigger нь NULL (users.org_id NULL, role=4) орхиж БЛОКЛОХГҮЙ.
Trigger-ийг апп org_id бичдэг болсны дараа `DROP TRIGGER`.

## 6. Алдааны үед
- `P4 gate: org_id NULL мөр үлдсэн ... <table>=N`: orphan. УСТГАХГҮЙ; `select` -ээр шалгаад owner шийд (§8). Шийдсэний дараа `migrate up` дахин.
- `P4 gate: давхардсан/NULL id`: PK-гүй хүснэгтийн id давхардал (sub_employee_skill, sub_employee_job_experience, item_details, relationship) - owner-ээр id-г шийдүүл.
- `INVALID index`: `DROP INDEX CONCURRENTLY <schema>.<idx>; CREATE UNIQUE INDEX CONCURRENTLY <idx> ON <t>(id);` (psql, tx-гүй) -> `migrate up`.
- `lock timeout`: тэр файл бүхэлдээ rollback; идэвхгүй болгох шаардлагагүй, хэсэг хугацааны дараа retry.

## 7. Rollback (зөвхөн ШИНЭЭР НЭМСЭН объект; өгөгдөл устахгүй гэсэн үг БИШ - org_id/created_at багана утгатай нь устна)
Apply-ийн дараа л. Эсрэг дарааллаар (idx -> NOT NULL/PK -> CHECK/trigger -> багана). Нэг tx-д (`BEGIN; SET LOCAL lock_timeout='5s'; ...; COMMIT;`), idx-ийг тусад нь `DROP INDEX CONCURRENTLY`.
```sql
-- idx (tx-гүй, тус бүр; нэр нь migrations/*.sql-тэй ижил)
DROP INDEX CONCURRENTLY IF EXISTS organization.idx_employee_extra_phones_org_emp_id;   -- idx_employee_extra_emails_org_emp_id, idx_employee_extra_shifts_org_emp_id ижил
DROP INDEX CONCURRENTLY IF EXISTS public.idx_sub_employee_skill_org_employee_id;       -- _course_, _education_, _job_experience_ ижил
DROP INDEX CONCURRENTLY IF EXISTS item.idx_item_details_org_item_id;
DROP INDEX CONCURRENTLY IF EXISTS sale.idx_sale_order_line_org_order_id;
DROP INDEX CONCURRENTLY IF EXISTS sale.idx_sale_order_surcharge_org_orders_id;
DROP INDEX CONCURRENTLY IF EXISTS inventory.idx_inventory_discrepancy_responsibility_employees_org_inventory_discrepancy_responsibility_id;
DROP INDEX CONCURRENTLY IF EXISTS attendance.idx_sub_shift_org_shift_id;
DROP INDEX CONCURRENTLY IF EXISTS party.idx_relationship_org_from;

-- tenant хүснэгт T (employee_extra_phones/emails/shifts, sale_order_line, sale_order_surcharge, inventory_discrepancy_responsibility_employees, party.relationship):
ALTER TABLE <T> ALTER COLUMN org_id DROP NOT NULL;
-- PK-ууд нэмэгдсэн бол (sub_employee_skill, sub_employee_job_experience, item_details, relationship): ALTER TABLE <T> DROP CONSTRAINT <t>_pkey;  DROP INDEX <t>_id_uidx -- PK drop хийхэд index бас устна.
-- trigger + function (13 хүснэгт бүр):
DROP TRIGGER IF EXISTS p4_<t>_org_id_derive ON <schema>.<t>;
DROP FUNCTION IF EXISTS <schema>.p4_<t>_org_id_derive();
-- багана (хамгийн сүүлд; backfill-ийн утгууд ба хэрэв лавлаж байгаа бол дээд давхрага унана):
ALTER TABLE <T> DROP COLUMN IF EXISTS org_id;      -- 13 хүснэгт
-- timestamps (0501/0502) - ЗӨВХӨН §1.4 snapshot-д "өмнө нь байгаагүй" гэж батлагдсаныг:
ALTER TABLE <T> DROP COLUMN IF EXISTS created_at, DROP COLUMN IF EXISTS updated_at;
-- tracking:
DELETE FROM khan.schema_migrations WHERE version IN (<apply хийсэн хувилбарууд>);   -- owner зөвшөөрлөөр
```
Rollback-ийн оронд ихэвчлэн ЗӨВ: trigger-ийг үлдээгээд (аюулгүй) асуудалтай CHECK-ийг л `DROP CONSTRAINT`-ээр хас. Бүрэн DB restore = backup/snapshot.

## 8. Owner-ийн шийдвэр (orphan / загвар)
1. **item.item_details 3/15 мөр** item_id нь устсан item-ийг заадаг: (a) архивлаж (CTAS archive.*) устга, (b) org-ийг гараар тогтоо, (c) орхи (org_id NULL, NOT NULL байхгүй). -> `_pending_owner/0216`.
2. **attendance.sub_shift 5/1286** shift_id устсан shift: ижил (a/b/c). -> `_pending_owner/0303`.
3. **public.sub_employee_* (26 мөр)**: employee_id = users.id; хэрэглэгч олон org-д. (A) tenant өгөгдөл биш (org_id хаях), (B) users.org_id (home org) - одоогийн W0 утга - tenant болгож NOT NULL, (C) org бүрт хуулбар. Мөн: эдгээр форм (1587-1590) users-ийн профайл дээр л хэрэглэгддэг үү? -> `_pending_owner/0120`.
4. **party.individual** (4008 party): 3927 нэг org, 20 олон org, 61 холбоогүй -> олон org-д хуваалцсаныг хуулбарлах/join хүснэгт, холбоогүй 61-ийг юу хийх. Мөн saveWithParty org_id бичдэг болох. -> `_pending_owner/0410`.
5. **RLS**: апп role `doadmin` BYPASSRLS=t. NOBYPASSRLS role руу шилжүүлэх + session бүрд `app.org_id` тавих (khan tenant) шийдвэр гартал RLS идэвхжүүлэхгүй. -> `_pending_owner/*_rls_soft`.
6. **HYBRID** lut_attendance_request (sys 26/org 0), lut_inventory_tag (sys 3/org 64): org_id nullable хэвээр (системийн мөр), NOT NULL ХИЙХГҮЙ.
7. sub_employee_* (int8 id) crud child IDInt, trigger-ийг хэзээ хасах, finance.salary_increment numeric(100,2) (P4-ээс гадуур).
