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
