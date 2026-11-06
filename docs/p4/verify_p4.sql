-- P4 verify (ЗӨВХӨН SELECT). Local scratch дээр баталгаажсан; prod-д owner зөвшөөрсний дараа ажиллуулна.
-- Ашиглах: psql -d <db> -f docs/p4/verify_p4.sql . "FAIL" мөр = засах хэрэгтэй, "OK" = хэвийн.

-- #1 org_id NULL үлдсэн мөр (backfill хийгдээгүй: эцэг алга / FK NULL). Бүгд 0 байх ёстой.
SELECT '1 null_org_id' chk, t, n, CASE WHEN n=0 THEN 'OK' ELSE 'FAIL' END st FROM (
  SELECT 'organization.employee_extra_phones' t, count(*) n FROM organization.employee_extra_phones WHERE org_id IS NULL UNION ALL
  SELECT 'organization.employee_extra_emails', count(*) FROM organization.employee_extra_emails WHERE org_id IS NULL UNION ALL
  SELECT 'organization.employee_extra_shifts', count(*) FROM organization.employee_extra_shifts WHERE org_id IS NULL UNION ALL
  SELECT 'public.sub_employee_skill', count(*) FROM public.sub_employee_skill WHERE org_id IS NULL UNION ALL
  SELECT 'public.sub_employee_course', count(*) FROM public.sub_employee_course WHERE org_id IS NULL UNION ALL
  SELECT 'public.sub_employee_education', count(*) FROM public.sub_employee_education WHERE org_id IS NULL UNION ALL
  SELECT 'public.sub_employee_job_experience', count(*) FROM public.sub_employee_job_experience WHERE org_id IS NULL UNION ALL
  SELECT 'item.item_details', count(*) FROM item.item_details WHERE org_id IS NULL UNION ALL
  SELECT 'sale.sale_order_line', count(*) FROM sale.sale_order_line WHERE org_id IS NULL UNION ALL
  SELECT 'sale.sale_order_surcharge', count(*) FROM sale.sale_order_surcharge WHERE org_id IS NULL UNION ALL
  SELECT 'inventory.inventory_discrepancy_responsibility_employees', count(*) FROM inventory.inventory_discrepancy_responsibility_employees WHERE org_id IS NULL UNION ALL
  SELECT 'attendance.sub_shift', count(*) FROM attendance.sub_shift WHERE org_id IS NULL UNION ALL
  SELECT 'party.relationship', count(*) FROM party.relationship WHERE org_id IS NULL
) x ORDER BY 1,2;

-- #2 backfill зөв эсэх: хүүхдийн org_id <> эцгийн org_id (0 байх ёстой)
SELECT '2 org_mismatch' chk, t, n, CASE WHEN n=0 THEN 'OK' ELSE 'FAIL' END st FROM (
  SELECT 'organization.employee_extra_phones' t, count(*) n FROM organization.employee_extra_phones c JOIN organization.employee p ON p.id=c.emp_id WHERE c.org_id<>p.org_id UNION ALL
  SELECT 'organization.employee_extra_emails', count(*) FROM organization.employee_extra_emails c JOIN organization.employee p ON p.id=c.emp_id WHERE c.org_id<>p.org_id UNION ALL
  SELECT 'organization.employee_extra_shifts', count(*) FROM organization.employee_extra_shifts c JOIN organization.employee p ON p.id=c.emp_id WHERE c.org_id<>p.org_id UNION ALL
  SELECT 'public.sub_employee_skill', count(*) FROM public.sub_employee_skill c JOIN organization.employee p ON p.id=c.employee_id WHERE c.org_id<>p.org_id UNION ALL
  SELECT 'public.sub_employee_course', count(*) FROM public.sub_employee_course c JOIN organization.employee p ON p.id=c.employee_id WHERE c.org_id<>p.org_id UNION ALL
  SELECT 'public.sub_employee_education', count(*) FROM public.sub_employee_education c JOIN organization.employee p ON p.id=c.employee_id WHERE c.org_id<>p.org_id UNION ALL
  SELECT 'public.sub_employee_job_experience', count(*) FROM public.sub_employee_job_experience c JOIN organization.employee p ON p.id=c.employee_id WHERE c.org_id<>p.org_id UNION ALL
  SELECT 'item.item_details', count(*) FROM item.item_details c JOIN item.item p ON p.id=c.item_id WHERE c.org_id<>p.org_id UNION ALL
  SELECT 'sale.sale_order_line', count(*) FROM sale.sale_order_line c JOIN sale.sale_order p ON p.id=c.order_id WHERE c.org_id<>p.org_id UNION ALL
  SELECT 'sale.sale_order_surcharge', count(*) FROM sale.sale_order_surcharge c JOIN sale.sale_order p ON p.id=c.orders_id WHERE c.org_id<>p.org_id UNION ALL
  SELECT 'inventory.inventory_discrepancy_responsibility_employees', count(*) FROM inventory.inventory_discrepancy_responsibility_employees c JOIN inventory.inventory_discrepancy_responsibility p ON p.id=c.inventory_discrepancy_responsibility_id WHERE c.org_id<>p.org_id UNION ALL
  SELECT 'attendance.sub_shift', count(*) FROM attendance.sub_shift c JOIN attendance.shift p ON p.id=c.shift_id WHERE c.org_id<>p.org_id UNION ALL
  SELECT 'party.relationship', count(*) FROM party.relationship c JOIN party.party p ON p.id=c.from_party_id WHERE c.org_id<>p.org_id
) x ORDER BY 1,2;

-- #3 party: to_party өөр org-д (cross-org relationship) - HUMAN CONFIRM
SELECT '3 relationship_cross_org' chk, count(*) n, CASE WHEN count(*)=0 THEN 'OK' ELSE 'REVIEW' END st
  FROM party.relationship r JOIN party.party t ON t.id=r.to_party_id WHERE t.org_id IS DISTINCT FROM r.org_id;

-- #4 party.individual: олон org-д хуваалцсан / party-гүй / org_id NULL (0410 pending-ийн урьдчилсан нөхцөл)
SELECT '4 individual' chk, k, n FROM (
  SELECT 'shared_multi_org' k, count(*) n FROM (SELECT individual_id FROM party.party WHERE individual_id IS NOT NULL AND org_id IS NOT NULL GROUP BY 1 HAVING count(DISTINCT org_id)>1) s UNION ALL
  SELECT 'no_party', count(*) FROM party.individual i WHERE NOT EXISTS (SELECT 1 FROM party.party p WHERE p.individual_id=i.id) UNION ALL
  SELECT 'org_id_null', count(*) FROM party.individual WHERE org_id IS NULL UNION ALL
  SELECT 'total', count(*) FROM party.individual) x;

-- #5 PK-гүй хүснэгтийн давхардсан id (unique index/PK үүсгэхээс өмнө 0 байх ёстой)
SELECT '5 dup_id' chk, t, n, CASE WHEN n=0 THEN 'OK' ELSE 'FAIL' END st FROM (
  SELECT 'public.sub_employee_skill' t, count(*) n FROM (SELECT id FROM public.sub_employee_skill GROUP BY id HAVING count(*)>1) d UNION ALL
  SELECT 'public.sub_employee_job_experience', count(*) FROM (SELECT id FROM public.sub_employee_job_experience GROUP BY id HAVING count(*)>1) d UNION ALL
  SELECT 'item.item_details', count(*) FROM (SELECT id FROM item.item_details GROUP BY id HAVING count(*)>1) d UNION ALL
  SELECT 'party.relationship', count(*) FROM (SELECT id FROM party.relationship GROUP BY id HAVING count(*)>1) d) x;

-- #6 INVALID index (CREATE INDEX CONCURRENTLY унасан үлдэгдэл; DROP INDEX CONCURRENTLY хийгээд migration-г дахин ажиллуул)
SELECT '6 invalid_index' chk, n.nspname||'.'||c.relname idx, 'FAIL' st
  FROM pg_index i JOIN pg_class c ON c.oid=i.indexrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE NOT i.indisvalid;

-- #7 validate хийгдээгүй CHECK
SELECT '7 unvalidated_check' chk, conrelid::regclass::text tbl, conname, 'FAIL' st FROM pg_constraint WHERE NOT convalidated AND conname LIKE '%\_org\_id\_nn'; -- (sale.sale_order_sales_team_fk гэх мэт өмнөх NOT VALID FK-г оруулахгүй)

-- #8 org_id нullability / PK / RLS / policy / derive trigger (энд org_id NOT NULL=true байх ёстой, party.individual-аас бусад)
SELECT '8 state' chk, c.oid::regclass::text tbl,
  (SELECT a.attnotnull FROM pg_attribute a WHERE a.attrelid=c.oid AND a.attname='org_id' AND NOT a.attisdropped) org_id_notnull,
  EXISTS (SELECT 1 FROM pg_constraint k WHERE k.conrelid=c.oid AND k.contype='p') has_pk,
  c.relrowsecurity rls, c.relforcerowsecurity rls_force,
  EXISTS (SELECT 1 FROM pg_policy p WHERE p.polrelid=c.oid AND p.polname='khan_tenant_isolation') policy,
  EXISTS (SELECT 1 FROM pg_trigger tg WHERE tg.tgrelid=c.oid AND tg.tgname LIKE 'p4\_%\_org\_id\_derive' AND NOT tg.tgisinternal) derive_trg
FROM pg_class c WHERE c.oid IN (
 'organization.employee_extra_phones'::regclass,'organization.employee_extra_emails'::regclass,'organization.employee_extra_shifts'::regclass,
 'public.sub_employee_skill'::regclass,'public.sub_employee_course'::regclass,'public.sub_employee_education'::regclass,'public.sub_employee_job_experience'::regclass,
 'item.item_details'::regclass,'sale.sale_order_line'::regclass,'sale.sale_order_surcharge'::regclass,'inventory.inventory_discrepancy_responsibility_employees'::regclass,
 'attendance.sub_shift'::regclass,'party.relationship'::regclass,'party.individual'::regclass) ORDER BY 2;

-- #9 created_at/updated_at дутуу хэвээр байгаа (group 5; planning.task_members гэх мэт) - зөвхөн мэдээлэл
SELECT '9 missing_ts' chk, table_schema||'.'||table_name tbl, string_agg(c, ',') missing FROM (
  SELECT t.table_schema, t.table_name, c FROM information_schema.tables t CROSS JOIN (VALUES ('created_at'),('updated_at')) v(c)
   WHERE (t.table_schema,t.table_name) IN (('planning','task_members'),('item','item_tag'),('item','item_bundle'),('workflow_and_process','process_voting_people'),('workflow_and_process','workflow_voting_people'),('service','service_employees'),('service','service_inventory'),('finance','salary_increment'),('finance','salary_deduction'),('treasurer','sub_rooms'),('planning','project_priorities'),('inventory','inventory'),('loyalty','loyalty_card'),('loyalty','card_batch_print'),('finance','insurance_percent'),('finance','lut_salary_deduction_type'),('finance','lut_salary_increment_type'),('finance','salary_type'),('treasurer','equipment_sub_location'),('treasurer','equipment_main_location'),('treasurer','lut_equipment_type'),('organization','conference_room'),('organization','conference_room_equipments'))
     AND NOT EXISTS (SELECT 1 FROM information_schema.columns k WHERE k.table_schema=t.table_schema AND k.table_name=t.table_name AND k.column_name=v.c)) m GROUP BY 2;

-- #10 public.users: owner шийдвэр 2026-11-06 - P4 migration-ууд public.users-д ӨӨРЧЛӨЛТ ХИЙХГҮЙ (backfill/NOT NULL/RLS байхгүй).
-- Шалгалт (migration файлд users гарахгүй): grep -rn 'public\.users' docs/p4/migrations  -> хоосон байх ёстой.
