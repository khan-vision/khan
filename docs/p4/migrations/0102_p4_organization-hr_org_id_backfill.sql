-- khan:no-transaction
-- DRAFT (P4) - APPLY ХИЙГЭЭГҮЙ. Prod-д apply хийхээс өмнө owner/CTO батлана; өмнө нь docs/p4/prod_counts.sql-ээр хэмжээг шалга.
-- khan migrate: forward-only, runner `SET LOCAL lock_timeout=5s`, statement_timeout=15m. Дугаарыг main-ийн дараагийн сул хувилбар руу ДАХИН ДУГААРЛА.
-- Group 1: organization-hr - EXPAND: nullable org_id + NOT VALID CHECK (богино lock).
-- Group 1: BACKFILL. ctid блок-мужаар (TID range scan, индексгүй ч шугаман; batch=200 блок, batch бүр COMMIT).
-- Эцэггүй (orphan) мөр NULL үлдэнэ -> дараагийн VALIDATE migration-ий gate тоог гаргаж унана (docs/p4/verify_p4.sql).

DO $$
DECLARE b bigint; maxb bigint; n bigint;
BEGIN
  -- organization.employee_extra_phones.emp_id -> organization.employee.org_id
  b := 0; maxb := pg_relation_size('organization.employee_extra_phones'::regclass) / current_setting('block_size')::bigint + 1;
  WHILE b <= maxb LOOP
    UPDATE organization.employee_extra_phones t SET org_id = p.org_id FROM organization.employee p
     WHERE p.id = t.emp_id AND t.org_id IS NULL AND p.org_id IS NOT NULL
       AND t.ctid >= format('(%s,0)', b)::tid AND t.ctid < format('(%s,0)', b + 200)::tid;
    b := b + 200;
    COMMIT;
  END LOOP;
  -- organization.employee_extra_emails.emp_id -> organization.employee.org_id
  b := 0; maxb := pg_relation_size('organization.employee_extra_emails'::regclass) / current_setting('block_size')::bigint + 1;
  WHILE b <= maxb LOOP
    UPDATE organization.employee_extra_emails t SET org_id = p.org_id FROM organization.employee p
     WHERE p.id = t.emp_id AND t.org_id IS NULL AND p.org_id IS NOT NULL
       AND t.ctid >= format('(%s,0)', b)::tid AND t.ctid < format('(%s,0)', b + 200)::tid;
    b := b + 200;
    COMMIT;
  END LOOP;
  -- organization.employee_extra_shifts.emp_id -> organization.employee.org_id
  b := 0; maxb := pg_relation_size('organization.employee_extra_shifts'::regclass) / current_setting('block_size')::bigint + 1;
  WHILE b <= maxb LOOP
    UPDATE organization.employee_extra_shifts t SET org_id = p.org_id FROM organization.employee p
     WHERE p.id = t.emp_id AND t.org_id IS NULL AND p.org_id IS NOT NULL
       AND t.ctid >= format('(%s,0)', b)::tid AND t.ctid < format('(%s,0)', b + 200)::tid;
    b := b + 200;
    COMMIT;
  END LOOP;
  -- public.sub_employee_skill.employee_id -> organization.employee.org_id
  b := 0; maxb := pg_relation_size('public.sub_employee_skill'::regclass) / current_setting('block_size')::bigint + 1;
  WHILE b <= maxb LOOP
    UPDATE public.sub_employee_skill t SET org_id = p.org_id FROM organization.employee p
     WHERE p.id = t.employee_id AND t.org_id IS NULL AND p.org_id IS NOT NULL
       AND t.ctid >= format('(%s,0)', b)::tid AND t.ctid < format('(%s,0)', b + 200)::tid;
    b := b + 200;
    COMMIT;
  END LOOP;
  -- public.sub_employee_course.employee_id -> organization.employee.org_id
  b := 0; maxb := pg_relation_size('public.sub_employee_course'::regclass) / current_setting('block_size')::bigint + 1;
  WHILE b <= maxb LOOP
    UPDATE public.sub_employee_course t SET org_id = p.org_id FROM organization.employee p
     WHERE p.id = t.employee_id AND t.org_id IS NULL AND p.org_id IS NOT NULL
       AND t.ctid >= format('(%s,0)', b)::tid AND t.ctid < format('(%s,0)', b + 200)::tid;
    b := b + 200;
    COMMIT;
  END LOOP;
  -- public.sub_employee_education.employee_id -> organization.employee.org_id
  b := 0; maxb := pg_relation_size('public.sub_employee_education'::regclass) / current_setting('block_size')::bigint + 1;
  WHILE b <= maxb LOOP
    UPDATE public.sub_employee_education t SET org_id = p.org_id FROM organization.employee p
     WHERE p.id = t.employee_id AND t.org_id IS NULL AND p.org_id IS NOT NULL
       AND t.ctid >= format('(%s,0)', b)::tid AND t.ctid < format('(%s,0)', b + 200)::tid;
    b := b + 200;
    COMMIT;
  END LOOP;
  -- public.sub_employee_job_experience.employee_id -> organization.employee.org_id
  b := 0; maxb := pg_relation_size('public.sub_employee_job_experience'::regclass) / current_setting('block_size')::bigint + 1;
  WHILE b <= maxb LOOP
    UPDATE public.sub_employee_job_experience t SET org_id = p.org_id FROM organization.employee p
     WHERE p.id = t.employee_id AND t.org_id IS NULL AND p.org_id IS NOT NULL
       AND t.ctid >= format('(%s,0)', b)::tid AND t.ctid < format('(%s,0)', b + 200)::tid;
    b := b + 200;
    COMMIT;
  END LOOP;
END $$
