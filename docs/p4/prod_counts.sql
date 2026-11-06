-- P4: PROD-д ЗӨВХӨН УНШИХ (SELECT) - owner/CTO ажиллуулна. DDL/DML ДЭЭР ОГТ ХИЙХГҮЙ. Би (agent) prod-д хандаагүй.
-- 1) Мөрийн тоо (org_id нэмэх хүснэгтүүд)
SELECT 'organization.employee_extra_phones' t, count(*) FROM organization.employee_extra_phones UNION ALL
SELECT 'organization.employee_extra_emails', count(*) FROM organization.employee_extra_emails UNION ALL
SELECT 'organization.employee_extra_shifts', count(*) FROM organization.employee_extra_shifts UNION ALL
SELECT 'public.sub_employee_skill', count(*) FROM public.sub_employee_skill UNION ALL
SELECT 'public.sub_employee_course', count(*) FROM public.sub_employee_course UNION ALL
SELECT 'public.sub_employee_education', count(*) FROM public.sub_employee_education UNION ALL
SELECT 'public.sub_employee_job_experience', count(*) FROM public.sub_employee_job_experience UNION ALL
SELECT 'item.item_details', count(*) FROM item.item_details UNION ALL
SELECT 'sale.sale_order_line', count(*) FROM sale.sale_order_line UNION ALL
SELECT 'sale.sale_order_surcharge', count(*) FROM sale.sale_order_surcharge UNION ALL
SELECT 'inventory.inventory_discrepancy_responsibility_employees', count(*) FROM inventory.inventory_discrepancy_responsibility_employees UNION ALL
SELECT 'attendance.sub_shift', count(*) FROM attendance.sub_shift UNION ALL
SELECT 'party.individual', count(*) FROM party.individual UNION ALL
SELECT 'party.relationship', count(*) FROM party.relationship UNION ALL
SELECT 'archive.sales_purchase_order', count(*) FROM archive.sales_purchase_order UNION ALL
SELECT 'sale.sub_order_surcharge', count(*) FROM sale.sub_order_surcharge UNION ALL
SELECT 'attendance.lut_leave', count(*) FROM attendance.lut_leave UNION ALL
SELECT 'attendance.lut_period', count(*) FROM attendance.lut_period;

-- 2) #orphan: эцэг алга (backfill-д org олдохгүй). Бүгд 0 байх ёстой.
SELECT 'employee_extra_phones' t, count(*) FROM organization.employee_extra_phones c LEFT JOIN organization.employee p ON p.id=c.emp_id WHERE p.id IS NULL UNION ALL
SELECT 'employee_extra_emails', count(*) FROM organization.employee_extra_emails c LEFT JOIN organization.employee p ON p.id=c.emp_id WHERE p.id IS NULL UNION ALL
SELECT 'employee_extra_shifts', count(*) FROM organization.employee_extra_shifts c LEFT JOIN organization.employee p ON p.id=c.emp_id WHERE p.id IS NULL UNION ALL
SELECT 'sub_employee_skill', count(*) FROM public.sub_employee_skill c LEFT JOIN organization.employee p ON p.id=c.employee_id WHERE p.id IS NULL UNION ALL
SELECT 'sub_employee_course', count(*) FROM public.sub_employee_course c LEFT JOIN organization.employee p ON p.id=c.employee_id WHERE p.id IS NULL UNION ALL
SELECT 'sub_employee_education', count(*) FROM public.sub_employee_education c LEFT JOIN organization.employee p ON p.id=c.employee_id WHERE p.id IS NULL UNION ALL
SELECT 'sub_employee_job_experience', count(*) FROM public.sub_employee_job_experience c LEFT JOIN organization.employee p ON p.id=c.employee_id WHERE p.id IS NULL UNION ALL
SELECT 'item_details', count(*) FROM item.item_details c LEFT JOIN item.item p ON p.id=c.item_id WHERE p.id IS NULL UNION ALL
SELECT 'sale_order_line', count(*) FROM sale.sale_order_line c LEFT JOIN sale.sale_order p ON p.id=c.order_id WHERE p.id IS NULL UNION ALL
SELECT 'sale_order_surcharge', count(*) FROM sale.sale_order_surcharge c LEFT JOIN sale.sale_order p ON p.id=c.orders_id WHERE p.id IS NULL UNION ALL
SELECT 'discrepancy_responsibility_employees', count(*) FROM inventory.inventory_discrepancy_responsibility_employees c LEFT JOIN inventory.inventory_discrepancy_responsibility p ON p.id=c.inventory_discrepancy_responsibility_id WHERE p.id IS NULL UNION ALL
SELECT 'sub_shift', count(*) FROM attendance.sub_shift c LEFT JOIN attendance.shift p ON p.id=c.shift_id WHERE p.id IS NULL UNION ALL
SELECT 'relationship', count(*) FROM party.relationship c LEFT JOIN party.party p ON p.id=c.from_party_id WHERE p.id IS NULL;

-- 3) #party: нэг individual олон org-ийн party-д холбогдсон уу (>0 бол org бүрт хуулбарлах шийдвэр хэрэгтэй)
SELECT count(*) AS individuals_in_multi_org FROM (
  SELECT individual_id FROM party.party WHERE individual_id IS NOT NULL AND org_id IS NOT NULL
  GROUP BY individual_id HAVING count(DISTINCT org_id) > 1) x;
SELECT count(*) AS individuals_without_party FROM party.individual i WHERE NOT EXISTS (SELECT 1 FROM party.party p WHERE p.individual_id=i.id);

-- 4) #relationship: холбооны хоёр тал өөр org-д байгаа эсэх
SELECT count(*) FROM party.relationship r JOIN party.party f ON f.id=r.from_party_id JOIN party.party t ON t.id=r.to_party_id WHERE f.org_id IS DISTINCT FROM t.org_id;

-- 5) #users: org_id-тай/гүй, employee холбоо
SELECT count(*) total, count(org_id) with_org_id FROM public.users;
SELECT count(*) AS users_without_employee FROM public.users u WHERE NOT EXISTS (SELECT 1 FROM organization.employee e WHERE e.user_id=u.id);
SELECT count(*) AS users_in_multi_org FROM (SELECT user_id FROM organization.employee WHERE user_id IS NOT NULL GROUP BY user_id HAVING count(DISTINCT org_id)>1) x;

-- 6) #hybrid: NULL org_id (системийн) мөр
SELECT count(*) FILTER (WHERE org_id IS NULL) sys_rows, count(*) FILTER (WHERE org_id IS NOT NULL) org_rows FROM attendance.lut_attendance_request;
SELECT count(*) FILTER (WHERE org_id IS NULL) sys_rows, count(*) FILTER (WHERE org_id IS NOT NULL) org_rows FROM item.lut_inventory_tag;

-- 7) sale_order_line хэмжээ (batch backfill-ийн хугацаа)
SELECT pg_size_pretty(pg_total_relation_size('sale.sale_order_line')) AS size_sale_order_line;
