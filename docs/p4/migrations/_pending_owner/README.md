# Owner шийдвэр хүлээж буй migration-ууд

Эдгээрийг apply хийхгүй. Дугаарлах дараалалд ОРООГҮЙ (`_` угтвартай хавтас). Тус бүрийн толгойд урьдчилсан нөхцөл бичигдсэн.

| Файл | Яагаад хойшилсон |
|---|---|
| 0119/0215/0311/0408 `*_rls_soft` | RLS эффектгүй: prod апп role `doadmin` = `rolbypassrls=t` (FORCE RLS ч BYPASSRLS-ийг давдаггүй). Апп role NOBYPASSRLS болгож, session бүрд `app.org_id` тавидаг болсны дараа. |
| 0120 sub_employee_* | `employee_id` = `public.users.id` (profile data, хэрэглэгч олон org-д). Tenant загвар owner-ийн шийдвэр. |
| 0216 item_details | prod 3/15 мөр dangling item_id (orphan). |
| 0303 sub_shift | prod 5/1286 мөр dangling shift_id (orphan). |
| 0406/0410 party.individual | `party.party.org_id` зөвхөн organization party-д; individual нь relationship-ээр олон org-д хуваалцагдана (20). |
| 0601 organizations org_id | owner шийдвэр. |

HYBRID (lut_attendance_request, item.lut_inventory_tag), public.users (org_id нэмэхгүй), obsolete хүснэгтийн DROP-д зориудаар migration бичээгүй (ORG_ID_GAPS.md §6).

## Шийдвэр (2026-11-07, owner: «хамгийн ухаалгаар шийд, ажиллаж байгаа систем хэвээр»)
W0 prod-д 10:22-д хэрэглэгдсэн (33 файл). Энэ хавтасны файлуудыг **одоохондоо ажиллуулахгүй** — шалтгаан:
1. **item_details 3 / sub_shift 5 orphan** — хэвээр (NULL org_id, NOT NULL-гүй). Эцэг нь устсан, апп уншдаггүй мөрүүд; устгах ашиггүй, эрсдэлтэй.
2. **sub_employee_* (26)** — хэрэглэгчийн профайлын өгөгдөл (parent = public.users), tenant өгөгдөл БИШ. org_id = home org (best-effort, nullable) хэвээр; NOT NULL/RLS хийхгүй (`0120` хойшлогдоно).
3. **party.individual** — tenant нь relationship-ээр тодорхойлогддог (3927 нэг org, 20 олон org, 61 холбоогүй); хуулбарлах/join хүснэгт = апп логик өөрчлөх ⇒ хийхгүй (`0410` хойшлогдоно).
4. **RLS** — апп role `doadmin` BYPASSRLS ⇒ асаасан ч нөлөөгүй; role салгах = DB дэд бүтцийн өөрчлөлт (пул, migration, cron) ⇒ хийхгүй. Tenant хамгаалалт = апп түвшин (`khan/tenant`, JWT org, `crudtest.Isolation`).
5. **`p4_*_org_id_derive` trigger** — хэвээр (апп org_id бичдэг болох хүртэл аюулгүй тор). 11/07 шалгалт: шинэ мөр org_id-тай, зөрчил 0.
6. **HYBRID** (lut_attendance_request, lut_inventory_tag) — nullable хэвээр.
Дахин авч үзэх нөхцөл: апп бүх INSERT-д org_id бичдэг болсон (P4 native resource-ууд бүрэн) эсвэл DB role-ийг салгах шийдвэр гарсан үед.
