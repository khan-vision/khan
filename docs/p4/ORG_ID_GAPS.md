# P4 - org_id / timestamps цоорхой (ORG_ID_GAPS)

Үүсгэсэн: 2026-11-06. Эх сурвалж: `khan gen audit` (catalog = `khan_vision_test` snapshot), main/modules grep. **Уншигч: owner.** Бүх өөрчлөлт DRAFT - юу ч apply хийгээгүй.

**Анхаар - мөрийн тоо:** `khan_vision_test` нь ихэнх хүснэгтэд ХООСОН (0 мөр). Бодит эзлэхүүн, NULL/орphan тоог prod-оос ЗӨВХӨН УНШИХ `prod_counts.sql`-ээр owner/CTO авна (доор).

## 1. Хураангуй

- Нийт афектлэгдсэн хүснэгт: **76** (org_id-гүй/алга: **53**; зөвхөн timestamps дутуу: **23**).
- org_id-гүй хүснэгтүүдийн ангилал: GLOBAL-IDENTITY=1, GLOBAL-LOOKUP=7, GLOBAL-PLATFORM=20, HYBRID=2, MISSING=3, OBSOLETE=3, TENANT-D=14, TENANT-ROOT=1
- Parent (форм татгалзсан) org_id-гүй хүснэгт: 31 (формын тоо 43); child (SubForm) org_id-гүй: 26.
- **Дүгнэлт**: tenant (org_id нэмнэ) = TENANT-D 14 + TENANT-ROOT 1; global (tenant-scope хэрэггүй) = platform 20 + lookup 7 + identity 1; hybrid (NULL=систем) = 2; obsolete/алга = 6.
- Хамгийн эрсдэлтэй: `public.users` (8789 мөр, org_id ихэнхэд NULL), `organization.organizations` (tenant root), `sale.sale_order_line` (санаатай org-гүй, том), `party.individual` (олон org-д хуваалцсан байж болно), `public.sub_employee_*` (int8 id, PK-гүй).

## 2. Ангиллын утга

| Ангилал | Утга | Үйлдэл |
|---|---|---|
| TENANT-D | Tenant өгөгдөл; эцэг хүснэгтээр org тодорхой | (a) org_id нэм -> backfill -> NOT NULL -> index -> RLS |
| TENANT-ROOT | Хүснэгт өөрөө tenant (organizations) | (a-variant) `org_id GENERATED ALWAYS AS (id)` |
| HYBRID | org_id nullable, NULL = системийн default | NOT NULL болгохгүй; crud-д system-row дэмжлэг эсвэл per-org хуулбар |
| GLOBAL-PLATFORM | Khan платформын контент/тохиргоо (sysadmin role 4) | (b) tenant-scope-гүй admin API; main/modules/kvadmin/* аль хэдийн native |
| GLOBAL-LOOKUP | Бүх org-д ижил лавлах | (b) `lookup.Spec{Tenant: lookup.TenantOff()}` read-only; засвар = kv-admin |
| GLOBAL-IDENTITY | Хэрэглэгчийн бүртгэл (олон org) | (b) org_id нэмэхгүй; employee-ээр scope |
| OBSOLETE / MISSING | Ашиглагдахгүй/хүснэгт алга | (c) retire |

**(b)-ийн санал (crud tenant-гүй resource ДЭМЖИГДЭХГҮЙ):**
1. Унших: `lookup.Spec{Table, Tenant: lookup.TenantOff(), SoftDelete}` - форм дотрох select/relation болон жижиг лавлахад.
2. Засах (platform контент, GIS, AIBot багц, sector): үүнийг `crud.Resource` болгохгүй. Одоогийн `main/modules/kvadmin/*` (role 4 хаалттай, хөнгөн `Resource`) хэвээр - legacy form/grid-ийг retire. Хэрэв khan-д нэг загвар хэрэгтэй бол `crud.Resource.Scope = crud.ScopeGlobal` (Can заавал `sysadmin`, org_id багана хэрэглэхгүй, RLS-д орохгүй) нэмэх саналтай - энэ нь khan/crud дахь шинэ feature, owner баталгаа шаардлагатай.
3. Global хүснэгтүүд RLS-ийн `TenantTables` жагсаалтад ОРОХГҮЙ (org_id баганагүй тул автоматаар хасагдана).

## 3. Хүснэгт бүрийн шийдвэр (org_id-гүй / тохиргоо хэрэгтэй)

Багана: форм/grid = legacy id; `child of` = SubForm-оор ашигласан эцэг форм; мөр = test DB; native hint = main-д тухайн form id-г дурдсан Go багц (heuristic).

| Хүснэгт | Ангилал | Формууд | child of | Grid | Мөр (test) | Org-ийн нотолгоо | Backfill-ийн эх | Шийдвэр |
|---|---|---|---|---|---|---|---|---|
| `attendance.lut_attendance_request` | HYBRID | 1419 | - | 1420 | 0 | Цагийн хүсэлтийн төрөл. org_id баганатай ч NULLABLE: NULL = систем (attcore/types.go:14-33 `org_id IS NULL OR org_id=?`; засах/устгах зөвхөн org-ийнхыг). [native hint: attendance/attcore] | Хэрэггүй: багана байгаа. NULL мөр = системийн default. | (a-variant) NOT NULL БОЛГОХГҮЙ; crud-д `OrgOrSystem` (NULL=read-only system) дэмжлэг хэрэгтэй, эсвэл org бүрт системийн мөрийг хуулж NOT NULL болгох (owner) |
| `organization.organizations` | TENANT-ROOT | 1495, 1647, 1762 | - | 1497, 1676 | 741 | Байгууллага ӨӨРӨӨ = tenant. org_id багана утгагүй. main: kvadmin глобал (sysadmin, org.go:94) + өөрийн байгууллагын профайл. 3 форм: 1495 (profile?), 1647, 1762. [native hint: organization/controllers, organization/org.go, organization/registry] | org_id := id (GENERATED ALWAYS AS (id) STORED) - crud-ийн scope `org_id = JWT org` өөрийн мөрийг л харуулна | (a-variant) generated org_id=id; sysadmin жагсаалт = tenant-гүй kv-admin API (owner баталгаажуулна) |
| `organization.employee_extra_phones` | TENANT-D | - | 1513, 1746 | - | 0 | Ажилтны нэмэлт утас; main-д ЗӨВХӨН ажилтны controller-оор (JOIN employee) хандана; өөрөө org-гүй. | emp_id -> organization.employee.id -> org_id | (a) org_id+backfill+NOT NULL+idx+RLS |
| `organization.employee_extra_emails` | TENANT-D | - | 1513, 1746 | - | 0 | Дээрхтэй адил (нэмэлт и-мэйл). | emp_id -> organization.employee.org_id | (a) |
| `organization.employee_extra_shifts` | TENANT-D | - | 1513 | - | 0 | Нэмэлт ээлжийн junction; main/modules/hr/shift/service_shift.go:236, hr/controllers/EmployeeController.go:313 employee-тэй JOIN хийж org шүүнэ. | emp_id -> organization.employee.org_id (shift_id -> attendance.shift.org_id-ээр давхар шалга) | (a) |
| `public.users` | GLOBAL-IDENTITY | 1536, 1585, 1586, 1587, 1588, 1589, 1590, 1591, 1822 | - | 1537 | 8789 | Нэг хэрэглэгч олон org-д ажиллаж болно; org_id ихэнхэд NULL (8789 мөрөөс ~630 нь л org_id-тай; memory: login JWT org_id=null = ХЭВИЙН). Org-ийн харьяалал = organization.employee.user_id + organization.user_extra_roles. 58 Go файл ашигладаг. [native hint: krudpublic/krudpublic.go, userformguard/userformguard.go] | Дериваци хийхгүй. Org-ийн хэрэглэгчид = JOIN organization.employee(user_id, org_id). | **OWNER ШИЙДВЭР 2026-11-06: action = "no org_id; tenant scoping via organization.organization_users / organization.employee join"** (organization_users: org_id uuid, user_id uuid, access varchar, owner_via_job_id uuid). public.users-д org_id багана, backfill, NOT NULL, RLS ХИЙХГҮЙ. (b) org_id НЭМЭХГҮЙ |
| `item.item_details` | TENANT-D | - | 1538 | - | 0 | Савлагааны төрөл; main/modules/item/models/item.go:157, ItemCrudController.go PackagingRow (item-ээр дамжуулан). PK ТАЛГҮЙ. | item_id -> item.item.org_id | (a) + PRIMARY KEY(id) |
| `archive.sales_purchase_order` | OBSOLETE | 1557, 1618, 1832 | - | 1560 | 0 | `archive` схем; main: «устсан sales_purchase_order хүснэгт» (inventory/models/inventory.go:167, delivery/services/complete.go:171 - ОРЛУУЛСАН). PK ТАЛГҮЙ, 0 мөр. [native hint: delivery/models, delivery/services, inventory/countengine] | - | (c) obsolete: форм 1557/1618/1832, grid 1560-г устга; хүснэгтийг owner зөвшөөрлөөр архивла |
| `sale.sub_order_surcharge` | OBSOLETE | 1619 | 1557, 1618, 1832 | - | 0 | sale.sale_order_surcharge-тэй адил багана; main Go-д 0 ашиглалт; зөвхөн archive.sales_purchase_order-ийн хүүхэд. | - | (c) |
| `archive.sales_purchase_order_items` | OBSOLETE | 1620 | 1557, 1618, 1832 | - | 0 | Дээрхийн мөр; PK ТАЛГҮЙ. | - | (c) |
| `map_server.map_layers` | GLOBAL-PLATFORM | 1561 | - | 1567 | 0 | Давхарга; db_schema/db_table нь платформын DB-ийг заана. Бүх мөр 0 (test), main-д зөвхөн kvadmin/gis. [native hint: kvadmin/gis] | - | (b) native kvadmin/gis бий -> legacy retire |
| `map_server.map_layer_legends` | GLOBAL-PLATFORM | 1566 | 1561 | - | 0 | Легенд; FK map_layers. Бүх мөр 0 (test), main-д зөвхөн kvadmin/gis. [native hint: kvadmin/gis] | - | (b) native kvadmin/gis бий -> legacy retire |
| `map_server.sub_map_layer_permissions` | GLOBAL-PLATFORM | - | 1561 | - | 0 | role_id int8 (public.roles) - эрхийн холбоо; платформ. Бүх мөр 0 (test), main-д зөвхөн kvadmin/gis. | - | (b) native kvadmin/gis бий -> legacy retire |
| `map_server.sub_map_layer_admin_filters` | GLOBAL-PLATFORM | - | 1561 | - | 0 | Админ шүүлтүүр. Бүх мөр 0 (test), main-д зөвхөн kvadmin/gis. | - | (b) native kvadmin/gis бий -> legacy retire |
| `map_server.sub_map_layer_filters` | GLOBAL-PLATFORM | - | 1561 | - | 0 | user_column/table_column - шүүлтүүрийн тохиргоо. Бүх мөр 0 (test), main-д зөвхөн kvadmin/gis. | - | (b) native kvadmin/gis бий -> legacy retire |
| `map_server.map` | GLOBAL-PLATFORM | 1562 | - | 1563 | 0 | Газрын зургийн тохиргоо; kvadmin/gis role 4. Бүх мөр 0 (test), main-д зөвхөн kvadmin/gis. [native hint: kvadmin/gis] | - | (b) native kvadmin/gis бий -> legacy retire |
| `map_server.sub_map_layer_categories` | GLOBAL-PLATFORM | - | 1562 | - | 0 | map<->category холбоо. Бүх мөр 0 (test), main-д зөвхөн kvadmin/gis. | - | (b) native kvadmin/gis бий -> legacy retire |
| `map_server.map_filters` | GLOBAL-PLATFORM | 1568 | 1562 | - | 0 | Газрын зургийн шүүлтүүр. Бүх мөр 0 (test), main-д зөвхөн kvadmin/gis. [native hint: kvadmin/gis] | - | (b) native kvadmin/gis бий -> legacy retire |
| `map_server.map_layer_category` | GLOBAL-PLATFORM | 1564 | - | 1565 | 0 | Давхаргын бүлэг. Бүх мөр 0 (test), main-д зөвхөн kvadmin/gis. [native hint: kvadmin/gis] | - | (b) native kvadmin/gis бий -> legacy retire |
| `party.individual` | TENANT-D | 1574 | - | 1575 | 0 | Хувь хүн. party.party-д org_id + individual_id бий. main/modules/organization/registry/handlers.go saveWithParty: party мөр org-тэй, individual нь түүнийг дагана. Нэг individual хэд хэдэн org-ийн party-д холбогдсон бол ТУЛГАРНА. [native hint: organization/controllers, organization/org.go, organization/registry] | party.party.individual_id -> party.party.org_id (DISTINCT org тоо >1 бол человек шийд) | (a) + эхлээд олон org-д давхцахыг шалга |
| `party.relationship` | TENANT-D | 1581 | - | 1580, 1808 | 0 | party хоорондын холбоо (crm/relationships). PK ТАЛГҮЙ (id uuid байгаа). [native hint: crm/relationships] | from_party_id -> party.party.org_id (to_party_id ижил org эсэхийг шалга) | (a) + PRIMARY KEY(id) |
| `public.sub_employee_skill` | TENANT-D | - | 1587 | - | 0 | Ажилтны дэд бүртгэл (форм 158x-ийн SubForm). main Go-д шууд хандалт ОЛДСОНГҮЙ (0 файл) -> legacy krud-оор л орно. id нь int8 (uuid БИШ) ; PK ТАЛГҮЙ. | employee_id -> organization.employee.org_id | (a) + id int8: crud child зөвхөн uuid ID -> эсвэл gen/crud-д IDInt child дэмжих, эсвэл uuid болгох (owner) |
| `public.sub_employee_course` | TENANT-D | - | 1588 | - | 0 | Ажилтны дэд бүртгэл (форм 158x-ийн SubForm). main Go-д шууд хандалт ОЛДСОНГҮЙ (0 файл) -> legacy krud-оор л орно. id нь int8 (uuid БИШ) . | employee_id -> organization.employee.org_id | (a) + id int8: crud child зөвхөн uuid ID -> эсвэл gen/crud-д IDInt child дэмжих, эсвэл uuid болгох (owner) |
| `public.sub_employee_education` | TENANT-D | - | 1589 | - | 0 | Ажилтны дэд бүртгэл (форм 158x-ийн SubForm). main Go-д шууд хандалт ОЛДСОНГҮЙ (0 файл) -> legacy krud-оор л орно. id нь int8 (uuid БИШ) . | employee_id -> organization.employee.org_id | (a) + id int8: crud child зөвхөн uuid ID -> эсвэл gen/crud-д IDInt child дэмжих, эсвэл uuid болгох (owner) |
| `public.sub_employee_job_experience` | TENANT-D | - | 1590 | - | 0 | Ажилтны дэд бүртгэл (форм 158x-ийн SubForm). main Go-д шууд хандалт ОЛДСОНГҮЙ (0 файл) -> legacy krud-оор л орно. id нь int8 (uuid БИШ) ; PK ТАЛГҮЙ. | employee_id -> organization.employee.org_id | (a) + id int8: crud child зөвхөн uuid ID -> эсвэл gen/crud-д IDInt child дэмжих, эсвэл uuid болгох (owner) |
| `document.category` | MISSING | 1603 | - | 1606 | 0 | Form 1603 model `category` схемгүй; document.category-д org_id байна ч формын 10 талбар одоогийн баганад таарахгүй (spec хуучирсан). | - | (c) legacy form 1603 (grid 1606) -> document модулийн одоогийн category API-аар солих; org_id асуудал БИШ |
| `category_sub` | MISSING | 1604 | - | 1610 | ? | Хүснэгт catalog-д алга (форм 1604, grid 1610). | - | (c) obsolete |
| `category_sub_sub` | MISSING | 1605, 1615 | - | 1611 | ? | Хүснэгт catalog-д алга (форм 1605/1615, grid 1611). | - | (c) obsolete |
| `workflow_and_process.workflow_system_type` | GLOBAL-LOOKUP | 1621 | - | 1624 | 1 | Систем төрөл (1 мөр). workflow_category.system_type_id-ээр лавлана. | - | (b) global lookup; lookup.Spec{Tenant: lookup.TenantOff()} |
| `workflow_and_process.workflow_category` | GLOBAL-LOOKUP | 1622 | - | 1625 | 2 | Workflow ангилал (2 мөр); sale_order.workflow_category_id түүнийг заана; org-гүй. | - | (b) |
| `treasurer.lut_distribution` | GLOBAL-LOOKUP | 1687 | - | 1694 | 0 | Өрөөний «Төлөв» лавлах (int4 id); inventory/assets/rooms.go:19,29 уншина, org-д хамааралгүй. | - | (b) |
| `web.home_erp_cards` | GLOBAL-PLATFORM | 1697 | - | 1698 | 0 | Нүүр хуудасны карт (khan-vision маркетинг). kvadmin/sitecontent (role 4) native. Платформын хүснэгт, tenant өгөгдөл БИШ. [native hint: kvadmin/sitecontent] | - | (b) Аль хэдийн native: main/modules/kvadmin/* (role 4). Legacy form/grid-ийг ДАХИН генерацлахгүй, зүгээр retire |
| `web.home_card_info` | GLOBAL-PLATFORM | 1699 | 1697, 1711 | 1700 | 0 | home_erp_cards-ийн хүүхэд. Платформын хүснэгт, tenant өгөгдөл БИШ. [native hint: kvadmin/sitecontent] | - | (b) Аль хэдийн native: main/modules/kvadmin/* (role 4). Legacy form/grid-ийг ДАХИН генерацлахгүй, зүгээр retire |
| `web.home_card_list` | GLOBAL-PLATFORM | - | 1699, 1717 | - | 0 | home_card_info-ийн хүүхэд. Платформын хүснэгт, tenant өгөгдөл БИШ. | - | (b) Аль хэдийн native: main/modules/kvadmin/* (role 4). Legacy form/grid-ийг ДАХИН генерацлахгүй, зүгээр retire |
| `web.faq_new` | GLOBAL-PLATFORM | 1704 | - | 1705 | 0 | Түгээмэл асуулт. kvadmin/sitecontent -> /api/kv-admin/site/faq. gqlguard public. Платформын хүснэгт, tenant өгөгдөл БИШ. [native hint: kvadmin/sitecontent] | - | (b) Аль хэдийн native: main/modules/kvadmin/* (role 4). Legacy form/grid-ийг ДАХИН генерацлахгүй, зүгээр retire |
| `web.home_erp_cards_package` | ? | 1711 | - | 1712 | 0 |  [native hint: kvadmin/sitecontent] |  |  |
| `web.home_erp_system_in_package` | GLOBAL-PLATFORM | - | 1711 | - | 0 | Багц доторх систем. Платформын хүснэгт, tenant өгөгдөл БИШ. | - | (b) Аль хэдийн native: main/modules/kvadmin/* (role 4). Legacy form/grid-ийг ДАХИН генерацлахгүй, зүгээр retire |
| `web.home_card_info_right` | GLOBAL-PLATFORM | 1717 | 1711 | - | 0 | home_erp_cards-ийн хүүхэд. Платформын хүснэгт, tenant өгөгдөл БИШ. [native hint: kvadmin/sitecontent] | - | (b) Аль хэдийн native: main/modules/kvadmin/* (role 4). Legacy form/grid-ийг ДАХИН генерацлахгүй, зүгээр retire |
| `public.about_us` | GLOBAL-PLATFORM | 1719 | - | 1720 | 0 | Холбоо барих (платформ). kvadmin/sitecontent. Платформын хүснэгт, tenant өгөгдөл БИШ. [native hint: kvadmin/sitecontent] | - | (b) Аль хэдийн native: main/modules/kvadmin/* (role 4). Legacy form/grid-ийг ДАХИН генерацлахгүй, зүгээр retire |
| `public.feed_back_khan_vision` | GLOBAL-PLATFORM | 1741 | - | 1742 | 0 | Санал хүсэлт (platform). kvadmin/sitefeedback native. Платформын хүснэгт, tenant өгөгдөл БИШ. [native hint: kvadmin/sitefeedback] | - | (b) Аль хэдийн native: main/modules/kvadmin/* (role 4). Legacy form/grid-ийг ДАХИН генерацлахгүй, зүгээр retire |
| `public.guide_list` | ? | 1749 | - | 1750 | 0 |  [native hint: kvadmin/guide] |  |  |
| `public.guide_desc_table` | GLOBAL-PLATFORM | - | 1749 | - | 0 | Гарын авлага (platform). kvadmin/guide native. Платформын хүснэгт, tenant өгөгдөл БИШ. | - | (b) Аль хэдийн native: main/modules/kvadmin/* (role 4). Legacy form/grid-ийг ДАХИН генерацлахгүй, зүгээр retire |
| `public.guide_images` | GLOBAL-PLATFORM | 1754 | 1749 | - | 0 | Гарын авлагын зураг. kvadmin/guide native. Платформын хүснэгт, tenant өгөгдөл БИШ. [native hint: kvadmin/guide] | - | (b) Аль хэдийн native: main/modules/kvadmin/* (role 4). Legacy form/grid-ийг ДАХИН генерацлахгүй, зүгээр retire |
| `inventory.inventory_discrepancy_responsibility_employees` | TENANT-D | - | 1778 | - | 0 | DiscrepancyController.go:63 эцэг хүснэгтээр JOIN хийнэ. Эцэг (…_responsibility) org_id-тай. | inventory_discrepancy_responsibility_id -> inventory.inventory_discrepancy_responsibility.org_id | (a) |
| `item.lut_inventory_tag` | HYBRID | 1806 | - | 1807 | 0 | org_id nullable (parent-no-timestamps/NOT NULL зөрчил). Системийн таг + org-ийн таг холилдсон байж болзошгүй. | org_id IS NULL мөрийг шалга | (a-variant) lut_attendance_request-тэй ижил шийдэл |
| `sale.sale_order_surcharge` | TENANT-D | - | 1854 | - | 0 | sale_order-ийн нэмэгдэл; main Go-д шууд хандалт 0 (sale_order-ийн legacy krud SubForm-оор л). | orders_id -> sale.sale_order.org_id | (a) |
| `sale.sale_order_line` | TENANT-D | - | 1854 | - | 0 | sale.sale_order-ийн мөр. main/modules/sale/controllers/SOController.go:445: «org_id БАЙХГҮЙ (эцэг захиалгаараа хамгаалагдана)» - санаатай шийдвэр байсан. 7 Go файл ашигладаг (том хүснэгт болзошгүй). | order_id -> sale.sale_order.org_id | (a) (batch backfill; prod хэмжээг эхлээд шалга) |
| `aibot.chat_bot_package` | GLOBAL-PLATFORM | 1861 | - | 1862 | 0 | AIBot багцын үнэ (platform). kvadmin/aibotadmin native. Платформын хүснэгт, tenant өгөгдөл БИШ. [native hint: kvadmin/aibotadmin] | - | (b) Аль хэдийн native: main/modules/kvadmin/* (role 4). Legacy form/grid-ийг ДАХИН генерацлахгүй, зүгээр retire |
| `classification.sector` | GLOBAL-LOOKUP | 204 | - | 205 | 0 | Үйл ажиллагааны салбарын ангилал; organization/sectors native (grid 205/208). [native hint: organization/sectors] | - | (b) global reference; өөрийн native модуль бий |
| `organization.sub_sector` | GLOBAL-LOOKUP | 206 | - | 208 | 0 | Дэд салбар (sector_id FK); organization/sectors native. [native hint: organization/sectors] | - | (b) |
| `attendance.sub_shift` | TENANT-D | - | 678 | - | 0 | 7 хоногийн ээлжийн мөр (attcore, GateBridge: sub_shift.shift_id=e.shift_id). | shift_id -> attendance.shift.org_id | (a) |
| `attendance.lut_leave` | GLOBAL-LOOKUP | 793 | - | 794 | 0 | Чөлөөний төрөл (id int2, нэг багана). main Go-д 0 ашиглалт. | - | (b)/(c) prod-д мөр/хэрэглээ шалга; хэрэглээ байхгүй бол retire |
| `attendance.lut_period` | GLOBAL-LOOKUP | 795 | - | 796 | 0 | Хугацааны лавлах (id int2). main Go-д 0 ашиглалт. | - | (b)/(c) дээрхтэй адил |

## 4. created_at/updated_at дутуу хүснэгтүүд

org_id-тай ч timestamps (AGENTS.md заавал) дутуу. Gen: child-д `crud` timestamps бичдэг тул SubForm алгасдаг; parent-д `Timestamps: crud.Off()`.

| Хүснэгт | Үүрэг | Формууд | Дутуу | Мөр (test) |
|---|---|---|---|---|
| `planning.task_members` | child | - | created_at, updated_at, deleted_at | 0 |
| `attendance.lut_attendance_request` | parent | 1419 | created_at, updated_at, deleted_at | 0 |
| `organization.employee_extra_phones` | child | - | created_at, updated_at, deleted_at | 0 |
| `organization.employee_extra_emails` | child | - | created_at, updated_at, deleted_at | 0 |
| `organization.employee_extra_shifts` | child | - | created_at, updated_at, deleted_at | 0 |
| `item.item_details` | child | - | created_at, updated_at, deleted_at | 0 |
| `item.item_tag` | child | - | updated_at, deleted_at | 0 |
| `item.item_bundle` | child | - | created_at, updated_at, deleted_at | 0 |
| `sale.sub_order_surcharge` | global/obsolete (хамаарахгүй) | 1619 | created_at, updated_at, deleted_at | 0 |
| `archive.sales_purchase_order_items` | global/obsolete (хамаарахгүй) | 1620 | created_at, updated_at, deleted_at | 0 |
| `workflow_and_process.process_voting_people` | child | - | created_at, updated_at, deleted_at | 0 |
| `map_server.map_layers` | global/obsolete (хамаарахгүй) | 1561 | created_at, updated_at, deleted_at | 0 |
| `map_server.map_layer_legends` | global/obsolete (хамаарахгүй) | 1566 | created_at, updated_at, deleted_at | 0 |
| `map_server.sub_map_layer_permissions` | global/obsolete (хамаарахгүй) | - | created_at, updated_at, deleted_at | 0 |
| `map_server.sub_map_layer_filters` | global/obsolete (хамаарахгүй) | - | created_at, updated_at, deleted_at | 0 |
| `map_server.sub_map_layer_categories` | global/obsolete (хамаарахгүй) | - | created_at, updated_at, deleted_at | 0 |
| `map_server.map_layer_category` | global/obsolete (хамаарахгүй) | 1564 | created_at, updated_at, deleted_at | 0 |
| `party.relationship` | parent | 1581 | created_at, updated_at, deleted_at | 0 |
| `public.sub_employee_skill` | child | - | created_at, updated_at, deleted_at | 0 |
| `public.sub_employee_course` | child | - | created_at, updated_at, deleted_at | 0 |
| `public.sub_employee_education` | child | - | created_at, updated_at, deleted_at | 0 |
| `public.sub_employee_job_experience` | child | - | created_at, updated_at, deleted_at | 0 |
| `workflow_and_process.workflow_system_type` | global/obsolete (хамаарахгүй) | 1621 | created_at, updated_at, deleted_at | 1 |
| `workflow_and_process.workflow_category` | global/obsolete (хамаарахгүй) | 1622 | created_at, updated_at, deleted_at | 2 |
| `workflow_and_process.workflow_voting_people` | child | 1630 | created_at, updated_at, deleted_at | 0 |
| `service.service_employees` | child | - | created_at, updated_at, deleted_at | 0 |
| `service.service_inventory` | child | - | created_at, updated_at, deleted_at | 0 |
| `finance.salary_increment` | child | - | created_at, updated_at, deleted_at | 0 |
| `finance.salary_deduction` | child | - | created_at, updated_at, deleted_at | 0 |
| `treasurer.lut_distribution` | global/obsolete (хамаарахгүй) | 1687 | created_at, updated_at, deleted_at | 0 |
| `treasurer.sub_rooms` | child | - | created_at, updated_at, deleted_at | 0 |
| `web.home_erp_cards` | global/obsolete (хамаарахгүй) | 1697 | created_at, updated_at, deleted_at | 0 |
| `web.home_card_info` | global/obsolete (хамаарахгүй) | 1699 | created_at, updated_at, deleted_at | 0 |
| `web.home_card_list` | global/obsolete (хамаарахгүй) | - | created_at, updated_at, deleted_at | 0 |
| `web.faq_new` | global/obsolete (хамаарахгүй) | 1704 | created_at, updated_at, deleted_at | 0 |
| `web.home_erp_cards_package` | parent | 1711 | created_at, updated_at, deleted_at | 0 |
| `web.home_erp_system_in_package` | global/obsolete (хамаарахгүй) | - | created_at, updated_at, deleted_at | 0 |
| `web.home_card_info_right` | global/obsolete (хамаарахгүй) | 1717 | created_at, updated_at, deleted_at | 0 |
| `public.about_us` | global/obsolete (хамаарахгүй) | 1719 | created_at, updated_at, deleted_at | 0 |
| `public.feed_back_khan_vision` | global/obsolete (хамаарахгүй) | 1741 | created_at, updated_at, deleted_at | 0 |
| `public.guide_list` | parent | 1749 | created_at, updated_at, deleted_at | 0 |
| `public.guide_desc_table` | global/obsolete (хамаарахгүй) | - | created_at, updated_at, deleted_at | 0 |
| `public.guide_images` | global/obsolete (хамаарахгүй) | 1754 | created_at, updated_at, deleted_at | 0 |
| `inventory.inventory_discrepancy_responsibility_employees` | child | - | created_at, updated_at, deleted_at | 0 |
| `item.lut_inventory_tag` | parent | 1806 | created_at, updated_at, deleted_at | 0 |
| `sale.sale_order_surcharge` | child | - | created_at, updated_at, deleted_at | 0 |
| `sale.sale_order_line` | child | - | deleted_at | 0 |
| `classification.sector` | global/obsolete (хамаарахгүй) | 204 | updated_at, deleted_at | 0 |
| `organization.sub_sector` | global/obsolete (хамаарахгүй) | 206 | created_at, updated_at, deleted_at | 0 |
| `attendance.sub_shift` | child | - | created_at, updated_at, deleted_at | 0 |
| `attendance.lut_leave` | global/obsolete (хамаарахгүй) | 793 | created_at, updated_at, deleted_at | 0 |
| `attendance.lut_period` | global/obsolete (хамаарахгүй) | 795 | created_at, updated_at, deleted_at | 0 |
| `planning.project_priorities` | parent | 1021 | created_at, updated_at, deleted_at | 0 |
| `inventory.inventory` | parent | 1539 | created_at | 71 |
| `loyalty.loyalty_card` | parent | 1542 | updated_at, deleted_at | 0 |
| `loyalty.card_batch_print` | parent | 1545 | updated_at, deleted_at | 0 |
| `finance.insurance_percent` | parent | 1651 | created_at, updated_at, deleted_at | 0 |
| `finance.lut_salary_deduction_type` | parent | 1656 | created_at, updated_at, deleted_at | 0 |
| `finance.lut_salary_increment_type` | parent | 1657 | created_at, updated_at, deleted_at | 0 |
| `finance.salary_type` | parent | 1660 | created_at, updated_at, deleted_at | 0 |
| `treasurer.equipment_sub_location` | parent | 1685 | created_at, updated_at, deleted_at | 0 |
| `treasurer.equipment_main_location` | parent | 1686 | created_at, updated_at, deleted_at | 0 |
| `treasurer.lut_equipment_type` | parent | 1688 | created_at, updated_at, deleted_at | 0 |
| `organization.conference_room` | parent | 1787 | created_at, updated_at, deleted_at | 0 |
| `organization.conference_room_equipments` | parent | 1793 | created_at, updated_at, deleted_at | 0 |

Нэмэлт санал: `deleted_at`-ийг энэ долгионд НЭМЭХГҮЙ (нэмбэл crud soft-delete горимд шилжиж, legacy hard-delete зан төлөв өөрчлөгдөнө) - зөвхөн created_at/updated_at (migration `P4_G5`).

## 5. Prod-оос унших (owner/CTO ажиллуулна, ЗӨВХӨН SELECT)

`prod_counts.sql` - доорх ижил жишээ: хүснэгт бүрийн мөр, backfill-ийн эх хүснэгттэй JOIN хийхэд орphan (parent алга) тоо, давхцсан org (party.individual).

## 6. Нээлттэй асуулт

- ~~public.users: org_id нэмэх үү?~~ ШИЙДСЭН (owner 2026-11-06): org_id нэмэхгүй; scope = organization.organization_users / organization.employee JOIN.
- organizations: `org_id GENERATED AS (id)` зөвшөөрөх үү? Эсвэл organizations формыг tenant-гүй kv-admin API-д л үлдээх үү?
- HYBRID (lut_attendance_request, lut_inventory_tag): khan/crud-д `NULL=system read-only` горим нэмэх үү, эсвэл org бүрт хуулбарлах уу?
- Child хүснэгтийн int8 id (sub_employee_*): crud child-д IDInt дэмжих үү, эсвэл uuid болгон шилжүүлэх үү? (тэдгээр формууд 1587-1590 бүгд public.users parent-тай нь холбоотой.)
- sale_order_line/sale_order_surcharge: «эцэг захиалгаараа хамгаалагдана» гэсэн хуучин шийдвэрийг цуцалж org_id нэмэх үү (RLS-д хэрэгтэй)? Эсвэл crud child org_id шаардлагыг (denormalized) хэвээр авах уу?
- party.individual: нэг individual олон org-ийн party-д холбогдсон тохиолдол prod-д байгаа эсэх (prod_counts.sql #party).
- archive.sales_purchase_order* , sale.sub_order_surcharge: устгах/архивлахыг зөвшөөрөх үү (DROP = таны зөвшөөрөл)?
- lut_leave/lut_period: prod-д хэрэглэгдэж байна уу?

## Prod баталгаажуулалт (2026-11-07, W0) - дээрх таамаглалын засвар
- `public.sub_employee_*`.employee_id = **public.users.id** (organization.employee БИШ). Нотолгоо: Lambda form 1587-1590 `model=users`, SubForm `parent=employee_id` (`main/lambda/schemas/form/158{7..0}.json`, `main/lambda/models/form/Users1587.go:28,75`; FE `front_end/src/modules/core/forms/UserSkillForm.vue:5,124`). Prod: 26/26 мөр users-тэй таарна, employee-тэй 0/26; эзэмшигч хэрэглэгчид 100% олон org-ийн гишүүн. -> profile өгөгдөл, tenant загвар owner-ийн шийдвэр (`_pending_owner/0120`).
- `party.party.org_id` зөвхөн `type='organization'` party-д (prod 596/596); individual (4008) party.org_id = NULL. `party.view_party` нь org_id-г `organization.organizations`-тай join хийдэг = "party нь тэр байгууллага өөрөө". Иймд individual-ийн tenant party-аас БИШ, relationship-ээр тодорно (4008: 3927 нэг org, 20 олон org, 61 холбоогүй).
- `party.relationship`: from_party_id = нэвтэрсэн хэрэглэгчийн `party_id` (tenant-ын org party) (`main/modules/organization/controllers/PartyController.go:43-58`, `registry/registry.go:223-236`), to_party = харилцагч. Иймд org_id = from_party.org_id ЗӨВ, "cross-org 4403/4406" нь ХЭВИЙН (to_party = individual эсвэл өөр org). Prod: from_party 4406/4406 organization төрөлтэй (31 org).
- Prod апп DB role `doadmin`: rolsuper=f, **rolbypassrls=t** -> RLS policy (FORCE-тэй ч) эффектгүй. RLS бүгд `_pending_owner/`.
- Orphan (FK байхгүй, эцэг устсан): item.item_details 3/15, attendance.sub_shift 5/1286. Бусад child-д орphan 0.
