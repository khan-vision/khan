# P4 долгионууд (P4_WAVES)

Үүсгэсэн: 2026-11-06. `khan gen audit -forms main/lambda/schemas/form -models main/lambda/models/form/formModels -grids main/lambda/schemas/grid -catalog <snapshot of khan_vision_test> -v`-ийн гаралт дээр суурилав. Нэг форм = нэг мөр; grid-ийг формын хүснэгттэй таарсан эсэхээр холбов (heuristic: grid `model`/`mainTable` -> хүснэгт).

Тэмдэглэл: `main/lambda/schemas/grid` дотор **96** grid JSON байна (төлөвлөгөөнд 101 гэсэн - 5 зөрүүг шалга). 57 / 113 формын id main/modules-ийн Go коммент/кодод аль хэдийн дурдагдсан (`native?`=Y) - ихэнх нь legacy krud-ийг native болгосон модуль (kvadmin/*, organization/registry, attendance/attcore, hr/payroll, ...). Эдгээрийг ДАХИН genерацлахгүй, legacy form/grid-ийг retire/харьцуул.

## 1. Audit хураангуй

```
формууд: 113 | clean: 0 | cosmetic TODO: 17 | логик TODO: 49 | refused (org_id): 43 | error: 4
```
Үүсээгүй шалтгаан: 43 refused (org_id алга/буруу; 2 нь PK алга), 3 хүснэгт catalog-д алга (1604, 1605, 1615), 1 «бичигдэх талбар үлдсэнгүй» (1603 document.category). SubForm: 55-аас 47 алгассан (34 child org_id-гүй, 13 created_at/updated_at-гүй).

| TODO код | формын тоо / нийт |
|---|---|
| label | 66 / 88 |
| grid | 40 / 76 |
| custom-element | 24 / 43 |
| relation | 13 / 16 |
| show-hide | 13 / 31 |
| subform | 12 / 23 |
| fill-by-user | 9 / 9 |
| trigger | 7 / 8 |
| column-missing | 4 / 5 |
| formula | 4 / 4 |
| column-type | 2 / 2 |
| field-trigger | 2 / 4 |
| form-feature | 1 / 1 |

`label`, `show-hide` = cosmetic. Бусад = логик (hook/relation/custom input/grid view).

## 2. Долгионы дараалал, тоо

Хамаарал: **W0 (migration) -> бусад.** W1-W4 дотор формын `blocked-by-org_id` статус W0-ийн дараа `todo`/`minor` болж шинэчлэгдэнэ (дахин `gen audit`). Дараалал нь P4 төлөвлөгөөний (school -> organization/hr -> finance -> inventory/item -> crm/sale -> бусад -> built-in)-ийг дагана; `school` schema энэ 113 формд байхгүй (аль хэдийн native/өөр механизм).

| Долгион | Хамрах хүрээ | Формууд | clean | cosmetic | logic | blocked-org_id | failed | Grid |
|---|---|---|---|---|---|---|---|---|
| W0 | Урьдчилсан: org_id/timestamps migration (G1-G5) | 0 | - | - | - | - | - | 0 |
| W1 | organization / hr / attendance / planning | 30 | 0 | 5 | 21 | 4 | 0 | 28 |
| W2 | finance / treasurer / payment | 16 | 0 | 6 | 9 | 1 | 0 | 16 |
| W3 | inventory / item / loyalty / delivery / purchase | 15 | 0 | 6 | 8 | 1 | 0 | 17 |
| W4 | crm: sale / service / workflow / party / news | 14 | 0 | 0 | 10 | 4 | 0 | 15 |
| W5 | platform + global (kvadmin native, lookup) | 17 | 0 | 0 | 1 | 16 | 0 | 13 |
| W6 | built-in: users / organizations (auth, profile) | 12 | 0 | 0 | 0 | 12 | 0 | 3 |
| W7 | retire: obsolete / алга болсон | 9 | 0 | 0 | 0 | 5 | 4 | 4 |
| **Нийт** | | **113** | 0 | 17 | 49 | 43 | 4 | 96 |

### Долгион бүрийн тайлбар

- **W0**: `migrations/` G1 organization-hr (7 хүснэгт), G2 item-inventory-sale (4), G3 attendance (1), G4 party (2), G5 timestamps (42 багана / 23 хүснэгт). Урьдчилсан шалгалт: `prod_counts.sql`. Блок: owner шийдвэр (ORG_ID_GAPS.md §6). Дараа нь `khan schema dump` -> `gen audit` дахин ажиллуулж W1-W4-ийн blocked формуудыг шинэчил.
- **W1**: Эхний цуваа: организаци/ХН/ирц/төлөвлөгөө. Ихэнх нь logic TODO (trigger, custom orgStructSelector, fill-by-user). attendance/attcore аль хэдийн native - эхлээд parity.
- **W2**: finance/treasurer. payroll-тай холбоотой 1651/1656/1657/1660/1661/1662 нь `/payroll` НЭГ хөдөлгүүрт (hr/payroll, payrollcore) орсон тул генерацлахгүй; зөвхөн лавлах (lut_*, salary_type) үлдэнэ.
- **W3**: inventory/item/loyalty/delivery. 1538 (item) subform-ууд W0 G2-оос хамаарна.
- **W4**: sale/service/workflow/party/news. 1854 (sale_order) subform-ууд W0 G2; party 1574/1581 W0 G4; process_status_history = useTableType (TODO).
- **W5**: Платформын контент: kvadmin/* native (role 4) - genерац ХЭРЭГГҮЙ, retire. public.* лавлах, aibot.chat_bot_package, web.*, map_server.*, classification.sector.
- **W6**: public.users (1536, 1585-1591, 1822) + organizations (1495, 1647, 1762): auth/profile - khan/auth (P2) + KvForm; tenant-scope-гүй, owner шийдвэр шаардана.
- **W5/W7 төлөв (2026-11-07): ДУУССАН (retire).** `main/legacy/retired` guard (main `4943d1ca`) нь 44 form/grid id-ийн `/lambda/krud|puzzle` хүсэлт бүрд 404 буцаана (DB хүснэгт хөндөөгүй). Үндэслэл: role 4 цэсний хуучин Lambda uuid-ууд FE `legacyMenuRedirects.ts`-ээр `/kv-dashboard/*` руу шилжсэн, kv-dashboard хуудсууд `/api/kv-admin/*`-ыг л дууддаг; Caddy log (10/31–11/03 + 11/07 09:42-оос) хандалт = зөвхөн 11/02-ийн нэг удаагийн schema sweep (id бүр ≤12). Үлдсэн FE цэвэрлэгээ: `nativeForms.ts`/`nativeGrids.ts` registry + `core|map|aibot/forms/*` (хуучин krud shell-ийн dead код).
- **W7**: archive.sales_purchase_order(+items) 1557/1618/1620/1832, sale.sub_order_surcharge 1619, document.category 1603, category_sub 1604, category_sub_sub 1605/1615: устгах/obsolete.

## 3. Форм бүрийн төлөв

`state`: clean / cosmetic TODO / logic TODO / blocked-by-org_id / failed. `org_id fix` = ORG_ID_GAPS.md-ийн шийдвэр. `todo` = кодын тоо.

| Форм | Хүснэгт | Долгион | state | todo | Grid | native? | org_id fix |
|---|---|---|---|---|---|---|---|
| 204 | `classification.sector` | W5 | blocked-by-org_id | - | 205 | Y | (b) global reference; өөрийн native модуль бий **RETIRED 2026-11-07** (main `4943d1ca`, sidecar 404 guard `legacy/retired`) |
| 206 | `organization.sub_sector` | W1 | blocked-by-org_id | - | 208 | Y | (b) |
| 638 | `planning.work_plan_tags` | W1 | cosmetic TODO | labelx1 | 639 | Y |  |
| 678 | `attendance.shift` | W1 | logic TODO | formulax1, gridx1, labelx1, subformx1 (subform алгассан: 1) | 680 | Y |  |
| 682 | `attendance.attendance` | W1 | logic TODO | gridx2, labelx2, relationx1 | 1744, 684, 912 | Y |  |
| 701 | `attendance.annual_leave` | W1 | logic TODO | fill-by-userx1, gridx1, labelx1, relationx1 | 703, 705 | - |  |
| 704 | `attendance.annual_leave` | W1 | logic TODO | gridx1, labelx1, relationx2 | 703, 705 | - |  |
| 790 | `attendance.leave` | W1 | logic TODO | fill-by-userx1, gridx2, labelx2, show-hidex2 | 1460, 792, 798 | - |  |
| 793 | `attendance.lut_leave` | W1 | blocked-by-org_id | - | 794 | - | (b)/(c) prod-д мөр/хэрэглээ шалга; хэрэглээ байхгүй бол retire |
| 795 | `attendance.lut_period` | W1 | blocked-by-org_id | - | 796 | - | (b)/(c) дээрхтэй адил |
| 797 | `attendance.leave` | W1 | logic TODO | gridx2, labelx2, relationx1, show-hidex2 | 1460, 792, 798 | - |  |
| 800 | `attendance.leave` | W1 | logic TODO | gridx2, labelx2, show-hidex2 | 1460, 792, 798 | - |  |
| 801 | `attendance.leave` | W1 | logic TODO | gridx2, labelx2, relationx1, show-hidex2 | 1460, 792, 798 | - |  |
| 892 | `attendance.beacon_request` | W1 | logic TODO | custom-elementx1, labelx1, relationx1 | 894 | Y |  |
| 1011 | `planning.projects` | W1 | logic TODO | fill-by-userx1, labelx1 | - | - |  |
| 1018 | `planning.tasks` | W1 | logic TODO | column-missingx1, custom-elementx2, fill-by-userx1, labelx1, subformx1 (subform алгассан: 1) | - | - |  |
| 1021 | `planning.project_priorities` | W1 | cosmetic TODO | labelx2 | - | - |  |
| 1022 | `planning.task_sub_task_group` | W1 | cosmetic TODO | labelx2 | - | - |  |
| 1023 | `planning.projects` | W1 | logic TODO | labelx1, triggerx1 | - | - |  |
| 1414 | `attendance.attendance_request` | W1 | logic TODO | gridx2, labelx1, relationx1, show-hidex2, triggerx1 | 1415 | Y |  |
| 1419 | `attendance.lut_attendance_request` | W1 | blocked-by-org_id | - | 1420 | Y | (a-variant) NOT NULL БОЛГОХГҮЙ; crud-д `OrgOrSystem` (NULL=read-only system) дэмжлэг хэрэгтэй, эсвэл org бүрт системийн  |
| 1430 | `attendance.attendance_request` | W1 | logic TODO | custom-elementx1, gridx2, labelx1, show-hidex2, triggerx1 | 1415 | Y |  |
| 1436 | `attendance.location` | W1 | logic TODO | custom-elementx2, fill-by-userx1, labelx1 | 1437 | Y |  |
| 1495 | `organization.organizations` | W6 | blocked-by-org_id | - | 1497, 1676 | - | (a-variant) generated org_id=id; sysadmin жагсаалт = tenant-гүй kv-admin API (owner баталгаажуулна) |
| 1507 | `organization.struct` | W1 | logic TODO | custom-elementx2, gridx2, labelx1 | 1509, 1677 | - |  |
| 1510 | `organization.job` | W1 | logic TODO | custom-elementx2, gridx2, labelx1 | 1512 | Y |  |
| 1513 | `organization.employee` | W1 | logic TODO | custom-elementx2, gridx2, labelx2, show-hidex1, subformx3 (subform алгассан: 3) | 1515, 1743, 1748 | - |  |
| 1536 | `public.users` | W6 | blocked-by-org_id | - | 1537 | - | (b) org_id НЭМЭХГҮЙ. Built-in долгион: auth/profile нь khan/auth-д; org-ийн хэрэглэгчийн жагсаалт = employee-ээр scope-т |
| 1538 | `item.item` | W3 | logic TODO | column-typex1, custom-elementx4, formulax1, gridx3, labelx2, show-hidex5, subformx3 (subform алгассан: 3) | 1551, 1835, 1860 | Y |  |
| 1539 | `inventory.inventory` | W3 | logic TODO | custom-elementx1, formulax1, gridx3, labelx1, relationx1 | 1552 | - |  |
| 1540 | `loyalty.card_type` | W3 | cosmetic TODO | labelx1 | 1541 | - |  |
| 1542 | `loyalty.loyalty_card` | W3 | logic TODO | custom-elementx1, form-featurex1, gridx2, labelx1 | 1544 | - |  |
| 1545 | `loyalty.card_batch_print` | W3 | cosmetic TODO | labelx1 | 1546 | - |  |
| 1557 | `archive.sales_purchase_order` | W7 | blocked-by-org_id | - | 1560 | Y | (c) obsolete: форм 1557/1618/1832, grid 1560-г устга; хүснэгтийг owner зөвшөөрлөөр архивла **RETIRED 2026-11-07** (main `4943d1ca`, sidecar 404 guard `legacy/retired`) |
| 1561 | `map_server.map_layers` | W5 | blocked-by-org_id | - | 1567 | Y | (b) native kvadmin/gis бий -> legacy retire **RETIRED 2026-11-07** (main `4943d1ca`, sidecar 404 guard `legacy/retired`) |
| 1562 | `map_server.map` | W5 | blocked-by-org_id | - | 1563 | Y | (b) native kvadmin/gis бий -> legacy retire **RETIRED 2026-11-07** (main `4943d1ca`, sidecar 404 guard `legacy/retired`) |
| 1564 | `map_server.map_layer_category` | W5 | blocked-by-org_id | - | 1565 | Y | (b) native kvadmin/gis бий -> legacy retire **RETIRED 2026-11-07** (main `4943d1ca`, sidecar 404 guard `legacy/retired`) |
| 1566 | `map_server.map_layer_legends` | W5 | blocked-by-org_id | - | - | Y | (b) native kvadmin/gis бий -> legacy retire **RETIRED 2026-11-07** (main `4943d1ca`, sidecar 404 guard `legacy/retired`) |
| 1568 | `map_server.map_filters` | W5 | blocked-by-org_id | - | - | Y | (b) native kvadmin/gis бий -> legacy retire **RETIRED 2026-11-07** (main `4943d1ca`, sidecar 404 guard `legacy/retired`) |
| 1574 | `party.individual` | W4 | blocked-by-org_id | - | 1575 | Y | (a) + эхлээд олон org-д давхцахыг шалга |
| 1581 | `party.relationship` | W4 | blocked-by-org_id | - | 1580, 1808 | Y | (a) + PRIMARY KEY(id) |
| 1585 | `public.users` | W6 | blocked-by-org_id | - | 1537 | Y | (b) org_id НЭМЭХГҮЙ. Built-in долгион: auth/profile нь khan/auth-д; org-ийн хэрэглэгчийн жагсаалт = employee-ээр scope-т |
| 1586 | `public.users` | W6 | blocked-by-org_id | - | 1537 | Y | (b) org_id НЭМЭХГҮЙ. Built-in долгион: auth/profile нь khan/auth-д; org-ийн хэрэглэгчийн жагсаалт = employee-ээр scope-т |
| 1587 | `public.users` | W6 | blocked-by-org_id | - | 1537 | - | (b) org_id НЭМЭХГҮЙ. Built-in долгион: auth/profile нь khan/auth-д; org-ийн хэрэглэгчийн жагсаалт = employee-ээр scope-т |
| 1588 | `public.users` | W6 | blocked-by-org_id | - | 1537 | - | (b) org_id НЭМЭХГҮЙ. Built-in долгион: auth/profile нь khan/auth-д; org-ийн хэрэглэгчийн жагсаалт = employee-ээр scope-т |
| 1589 | `public.users` | W6 | blocked-by-org_id | - | 1537 | - | (b) org_id НЭМЭХГҮЙ. Built-in долгион: auth/profile нь khan/auth-д; org-ийн хэрэглэгчийн жагсаалт = employee-ээр scope-т |
| 1590 | `public.users` | W6 | blocked-by-org_id | - | 1537 | - | (b) org_id НЭМЭХГҮЙ. Built-in долгион: auth/profile нь khan/auth-д; org-ийн хэрэглэгчийн жагсаалт = employee-ээр scope-т |
| 1591 | `public.users` | W6 | blocked-by-org_id | - | 1537 | - | (b) org_id НЭМЭХГҮЙ. Built-in долгион: auth/profile нь khan/auth-д; org-ийн хэрэглэгчийн жагсаалт = employee-ээр scope-т |
| 1603 | `document.category` | W7 | failed | - | 1606 | - | (c) legacy form 1603 (grid 1606) -> document модулийн одоогийн category API-аар солих; org_id асуудал БИШ **RETIRED 2026-11-07** (main `4943d1ca`, sidecar 404 guard `legacy/retired`) |
| 1604 | `category_sub` | W7 | failed | - | 1610 | - | (c) obsolete **RETIRED 2026-11-07** (main `4943d1ca`, sidecar 404 guard `legacy/retired`) |
| 1605 | `category_sub_sub` | W7 | failed | - | 1611 | - | (c) obsolete **RETIRED 2026-11-07** (main `4943d1ca`, sidecar 404 guard `legacy/retired`) |
| 1615 | `category_sub_sub` | W7 | failed | - | 1611 | - | (c) obsolete **RETIRED 2026-11-07** (main `4943d1ca`, sidecar 404 guard `legacy/retired`) |
| 1618 | `archive.sales_purchase_order` | W7 | blocked-by-org_id | - | 1560 | - | (c) obsolete: форм 1557/1618/1832, grid 1560-г устга; хүснэгтийг owner зөвшөөрлөөр архивла **RETIRED 2026-11-07** (main `4943d1ca`, sidecar 404 guard `legacy/retired`) |
| 1619 | `sale.sub_order_surcharge` | W7 | blocked-by-org_id | - | - | - | (c) **RETIRED 2026-11-07** (main `4943d1ca`, sidecar 404 guard `legacy/retired`) |
| 1620 | `archive.sales_purchase_order_items` | W7 | blocked-by-org_id | - | - | - | (c) **RETIRED 2026-11-07** (main `4943d1ca`, sidecar 404 guard `legacy/retired`) |
| 1621 | `workflow_and_process.workflow_system_type` | W4 | blocked-by-org_id | - | 1624 | - | (b) global lookup; lookup.Spec{Tenant: lookup.TenantOff()} |
| 1622 | `workflow_and_process.workflow_category` | W4 | blocked-by-org_id | - | 1625 | - | (b) |
| 1623 | `workflow_and_process.workflow` | W4 | logic TODO | custom-elementx1, gridx2, labelx1, subformx1 (subform алгассан: 1) | 1626 | - |  |
| 1630 | `workflow_and_process.workflow_voting_people` | W4 | logic TODO | custom-elementx1, labelx2, relationx1, show-hidex6 | - | - |  |
| 1640 | `service.service` | W4 | logic TODO | custom-elementx1, fill-by-userx1, gridx2, labelx2, relationx3, subformx2 (subform алгассан: 2) | 1645 | - |  |
| 1642 | `delivery.delivery` | W3 | logic TODO | custom-elementx2, gridx2, labelx2, relationx1, subformx2, triggerx1 (subform алгассан: 2) | 1577 | Y |  |
| 1647 | `organization.organizations` | W6 | blocked-by-org_id | - | 1497, 1676 | Y | (a-variant) generated org_id=id; sysadmin жагсаалт = tenant-гүй kv-admin API (owner баталгаажуулна) |
| 1650 | `inventory.warehouse` | W3 | logic TODO | column-missingx2, custom-elementx1, gridx2, labelx1 | 1654 | - |  |
| 1651 | `finance.insurance_percent` | W2 | cosmetic TODO | labelx1 | 1652 | Y |  |
| 1656 | `finance.lut_salary_deduction_type` | W2 | cosmetic TODO | labelx1 | 1655 | Y |  |
| 1657 | `finance.lut_salary_increment_type` | W2 | cosmetic TODO | labelx1 | 1658 | Y |  |
| 1660 | `finance.salary_type` | W2 | cosmetic TODO | labelx1 | 1663 | Y |  |
| 1661 | `finance.salary` | W2 | logic TODO | custom-elementx1, field-triggerx3, fill-by-userx1, gridx2, labelx1, show-hidex2, subformx2 (subform алгассан: 2) | 1659 | Y |  |
| 1662 | `finance.salary_history` | W2 | logic TODO | custom-elementx1, field-triggerx1, formulax1, gridx2, labelx1, show-hidex3, triggerx1 | 1664 | Y |  |
| 1668 | `inventory.warehouse_location` | W3 | cosmetic TODO | labelx2 | - | - |  |
| 1678 | `organization.struct` | W1 | logic TODO | custom-elementx2, gridx2, labelx1 | 1509, 1677 | Y |  |
| 1685 | `treasurer.equipment_sub_location` | W2 | logic TODO | gridx2, labelx1 | 1692 | Y |  |
| 1686 | `treasurer.equipment_main_location` | W2 | logic TODO | custom-elementx1, labelx1 | 1693 | Y |  |
| 1687 | `treasurer.lut_distribution` | W2 | blocked-by-org_id | - | 1694 | - | (b) |
| 1688 | `treasurer.lut_equipment_type` | W2 | cosmetic TODO | labelx1 | 1695 | Y |  |
| 1689 | `treasurer.equipment` | W2 | logic TODO | custom-elementx2, gridx2, labelx1 | 1691 | Y |  |
| 1690 | `treasurer.rooms` | W2 | logic TODO | gridx2, labelx1, subformx1 (subform алгассан: 1) | 1696 | Y |  |
| 1697 | `web.home_erp_cards` | W5 | blocked-by-org_id | - | 1698 | Y | (b) Аль хэдийн native: main/modules/kvadmin/* (role 4). Legacy form/grid-ийг ДАХИН генерацлахгүй, зүгээр retire **RETIRED 2026-11-07** (main `4943d1ca`, sidecar 404 guard `legacy/retired`) |
| 1699 | `web.home_card_info` | W5 | blocked-by-org_id | - | 1700 | Y | (b) Аль хэдийн native: main/modules/kvadmin/* (role 4). Legacy form/grid-ийг ДАХИН генерацлахгүй, зүгээр retire **RETIRED 2026-11-07** (main `4943d1ca`, sidecar 404 guard `legacy/retired`) |
| 1704 | `web.faq_new` | W5 | blocked-by-org_id | - | 1705 | Y | (b) Аль хэдийн native: main/modules/kvadmin/* (role 4). Legacy form/grid-ийг ДАХИН генерацлахгүй, зүгээр retire **RETIRED 2026-11-07** (main `4943d1ca`, sidecar 404 guard `legacy/retired`) |
| 1711 | `web.home_erp_cards_package` | W5 | blocked-by-org_id | - | 1712 | Y | **RETIRED 2026-11-07** (main `4943d1ca`, sidecar 404 guard `legacy/retired`) |
| 1717 | `web.home_card_info_right` | W5 | blocked-by-org_id | - | - | Y | (b) Аль хэдийн native: main/modules/kvadmin/* (role 4). Legacy form/grid-ийг ДАХИН генерацлахгүй, зүгээр retire **RETIRED 2026-11-07** (main `4943d1ca`, sidecar 404 guard `legacy/retired`) |
| 1719 | `public.about_us` | W5 | blocked-by-org_id | - | 1720 | Y | (b) Аль хэдийн native: main/modules/kvadmin/* (role 4). Legacy form/grid-ийг ДАХИН генерацлахгүй, зүгээр retire **RETIRED 2026-11-07** (main `4943d1ca`, sidecar 404 guard `legacy/retired`) |
| 1722 | `item.brands` | W3 | cosmetic TODO | labelx1 | 1723 | - |  |
| 1732 | `party.party_feedback` | W4 | logic TODO | gridx2, labelx1 | 1736 | Y |  |
| 1733 | `party.party_gratitude` | W4 | logic TODO | gridx2, labelx1 | 1737 | Y |  |
| 1740 | `service.assigned_services` | W4 | logic TODO | custom-elementx1, gridx2, labelx2, triggerx1 | 1846 | - |  |
| 1741 | `public.feed_back_khan_vision` | W5 | blocked-by-org_id | - | 1742 | Y | (b) Аль хэдийн native: main/modules/kvadmin/* (role 4). Legacy form/grid-ийг ДАХИН генерацлахгүй, зүгээр retire **RETIRED 2026-11-07** (main `4943d1ca`, sidecar 404 guard `legacy/retired`) |
| 1746 | `organization.employee` | W1 | logic TODO | custom-elementx2, gridx2, labelx2, show-hidex1, subformx2 (subform алгассан: 2) | 1515, 1743, 1748 | - |  |
| 1749 | `public.guide_list` | W5 | blocked-by-org_id | - | 1750 | Y | **RETIRED 2026-11-07** (main `4943d1ca`, sidecar 404 guard `legacy/retired`) |
| 1754 | `public.guide_images` | W5 | blocked-by-org_id | - | - | Y | (b) Аль хэдийн native: main/modules/kvadmin/* (role 4). Legacy form/grid-ийг ДАХИН генерацлахгүй, зүгээр retire **RETIRED 2026-11-07** (main `4943d1ca`, sidecar 404 guard `legacy/retired`) |
| 1762 | `organization.organizations` | W6 | blocked-by-org_id | - | 1497, 1676 | - | (a-variant) generated org_id=id; sysadmin жагсаалт = tenant-гүй kv-admin API (owner баталгаажуулна) |
| 1774 | `treasurer.equipment_count` | W2 | logic TODO | fill-by-userx1, gridx2, labelx1 | 1776 | Y |  |
| 1778 | `inventory.inventory_discrepancy_responsibility` | W3 | logic TODO | labelx1, relationx1, subformx1 (subform алгассан: 1) | 1779 | - |  |
| 1780 | `news.news` | W4 | logic TODO | column-missingx1, gridx1, labelx1 | 1781, 1848 | Y |  |
| 1787 | `organization.conference_room` | W1 | cosmetic TODO | labelx1 | 1788 | - |  |
| 1793 | `organization.conference_room_equipments` | W1 | cosmetic TODO | labelx1 | 1794 | Y |  |
| 1795 | `organization.conference_room_order` | W1 | logic TODO | gridx2, labelx2 | 1790, 1802, 1803 | Y |  |
| 1797 | `inventory.inventory_count_config` | W3 | cosmetic TODO | labelx1 | 1798 | - |  |
| 1806 | `item.lut_inventory_tag` | W3 | blocked-by-org_id | - | 1807 | - | (a-variant) lut_attendance_request-тэй ижил шийдэл |
| 1822 | `public.users` | W6 | blocked-by-org_id | - | 1537 | - | (b) org_id НЭМЭХГҮЙ. Built-in долгион: auth/profile нь khan/auth-д; org-ийн хэрэглэгчийн жагсаалт = employee-ээр scope-т |
| 1823 | `payment.golomt_integration` | W2 | cosmetic TODO | labelx1 | 1824 | Y |  |
| 1829 | `item.categories` | W3 | logic TODO | custom-elementx1, labelx2, relationx1 | 1852 | - |  |
| 1832 | `archive.sales_purchase_order` | W7 | blocked-by-org_id | - | 1560 | - | (c) obsolete: форм 1557/1618/1832, grid 1560-г устга; хүснэгтийг owner зөвшөөрлөөр архивла **RETIRED 2026-11-07** (main `4943d1ca`, sidecar 404 guard `legacy/retired`) |
| 1834 | `news.news` | W4 | logic TODO | column-missingx1, gridx1, labelx1 | 1781, 1848 | - |  |
| 1838 | `finance.bonus_salary_configuration` | W2 | logic TODO | gridx2, labelx1 | 1840 | Y |  |
| 1844 | `finance.bonus_salary_employee` | W2 | logic TODO | gridx2, labelx2 | 1843 | Y |  |
| 1847 | `news.news` | W4 | logic TODO | gridx1, labelx1 | 1781, 1848 | Y |  |
| 1851 | `item.categories` | W3 | cosmetic TODO | labelx2 | 1852 | - |  |
| 1854 | `sale.sale_order` | W4 | logic TODO | custom-elementx8, fill-by-userx1, gridx2, labelx2, show-hidex1, subformx4, triggerx2 (subform алгассан: 4) | 1857 | - |  |
| 1855 | `purchase.purchase_order` | W3 | logic TODO | gridx2, labelx2 | 1859 | - |  |
| 1861 | `aibot.chat_bot_package` | W5 | blocked-by-org_id | - | 1862 | Y | (b) Аль хэдийн native: main/modules/kvadmin/* (role 4). Legacy form/grid-ийг ДАХИН генерацлахгүй, зүгээр retire **RETIRED 2026-11-07** (main `4943d1ca`, sidecar 404 guard `legacy/retired`) |
| 1863 | `aibot.chat_bots` | W5 | logic TODO | column-typex1, gridx2, labelx2 | 1866 | Y | **RETIRED 2026-11-07** (main `4943d1ca`, sidecar 404 guard `legacy/retired`) |
