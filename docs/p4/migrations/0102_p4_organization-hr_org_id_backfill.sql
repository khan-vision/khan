-- khan:no-transaction
-- DRAFT (P4) - APPLY ХИЙГЭЭГҮЙ. Prod-д apply хийхээс өмнө owner/CTO батлана; өмнө нь docs/p4/prod_counts.sql-ээр хэмжээг шалга.
-- khan migrate: forward-only, runner `SET LOCAL lock_timeout=5s`, statement_timeout=15m. Дугаарыг main-ийн дараагийн сул хувилбар руу ДАХИН ДУГААРЛА.
-- Group 1: BACKFILL (batch 5000, batch бүр COMMIT). Нэг statement (DO) тул no-transaction.
-- Эцэг хүснэгтгүй (orphan) мөр NULL хэвээр үлдэж, дараагийн VALIDATE унана -> prod_counts.sql #orphan-оор өмнө нь шалга/цэвэрл (human confirm).

DO $$
DECLARE n bigint;
BEGIN
  -- organization.employee_extra_phones.emp_id -> organization.employee.org_id
  LOOP
    UPDATE organization.employee_extra_phones t SET org_id = p.org_id
      FROM organization.employee p
     WHERE p.id = t.emp_id
       AND t.ctid IN (SELECT t2.ctid FROM organization.employee_extra_phones t2 JOIN organization.employee p2 ON p2.id = t2.emp_id
                       WHERE t2.org_id IS NULL AND p2.org_id IS NOT NULL LIMIT 5000);
    GET DIAGNOSTICS n = ROW_COUNT;
    EXIT WHEN n = 0;
    COMMIT;
  END LOOP;
  COMMIT;
  -- organization.employee_extra_emails.emp_id -> organization.employee.org_id
  LOOP
    UPDATE organization.employee_extra_emails t SET org_id = p.org_id
      FROM organization.employee p
     WHERE p.id = t.emp_id
       AND t.ctid IN (SELECT t2.ctid FROM organization.employee_extra_emails t2 JOIN organization.employee p2 ON p2.id = t2.emp_id
                       WHERE t2.org_id IS NULL AND p2.org_id IS NOT NULL LIMIT 5000);
    GET DIAGNOSTICS n = ROW_COUNT;
    EXIT WHEN n = 0;
    COMMIT;
  END LOOP;
  COMMIT;
  -- organization.employee_extra_shifts.emp_id -> organization.employee.org_id
  LOOP
    UPDATE organization.employee_extra_shifts t SET org_id = p.org_id
      FROM organization.employee p
     WHERE p.id = t.emp_id
       AND t.ctid IN (SELECT t2.ctid FROM organization.employee_extra_shifts t2 JOIN organization.employee p2 ON p2.id = t2.emp_id
                       WHERE t2.org_id IS NULL AND p2.org_id IS NOT NULL LIMIT 5000);
    GET DIAGNOSTICS n = ROW_COUNT;
    EXIT WHEN n = 0;
    COMMIT;
  END LOOP;
  COMMIT;
  -- public.sub_employee_skill.employee_id -> organization.employee.org_id
  LOOP
    UPDATE public.sub_employee_skill t SET org_id = p.org_id
      FROM organization.employee p
     WHERE p.id = t.employee_id
       AND t.ctid IN (SELECT t2.ctid FROM public.sub_employee_skill t2 JOIN organization.employee p2 ON p2.id = t2.employee_id
                       WHERE t2.org_id IS NULL AND p2.org_id IS NOT NULL LIMIT 5000);
    GET DIAGNOSTICS n = ROW_COUNT;
    EXIT WHEN n = 0;
    COMMIT;
  END LOOP;
  COMMIT;
  -- public.sub_employee_course.employee_id -> organization.employee.org_id
  LOOP
    UPDATE public.sub_employee_course t SET org_id = p.org_id
      FROM organization.employee p
     WHERE p.id = t.employee_id
       AND t.ctid IN (SELECT t2.ctid FROM public.sub_employee_course t2 JOIN organization.employee p2 ON p2.id = t2.employee_id
                       WHERE t2.org_id IS NULL AND p2.org_id IS NOT NULL LIMIT 5000);
    GET DIAGNOSTICS n = ROW_COUNT;
    EXIT WHEN n = 0;
    COMMIT;
  END LOOP;
  COMMIT;
  -- public.sub_employee_education.employee_id -> organization.employee.org_id
  LOOP
    UPDATE public.sub_employee_education t SET org_id = p.org_id
      FROM organization.employee p
     WHERE p.id = t.employee_id
       AND t.ctid IN (SELECT t2.ctid FROM public.sub_employee_education t2 JOIN organization.employee p2 ON p2.id = t2.employee_id
                       WHERE t2.org_id IS NULL AND p2.org_id IS NOT NULL LIMIT 5000);
    GET DIAGNOSTICS n = ROW_COUNT;
    EXIT WHEN n = 0;
    COMMIT;
  END LOOP;
  COMMIT;
  -- public.sub_employee_job_experience.employee_id -> organization.employee.org_id
  LOOP
    UPDATE public.sub_employee_job_experience t SET org_id = p.org_id
      FROM organization.employee p
     WHERE p.id = t.employee_id
       AND t.ctid IN (SELECT t2.ctid FROM public.sub_employee_job_experience t2 JOIN organization.employee p2 ON p2.id = t2.employee_id
                       WHERE t2.org_id IS NULL AND p2.org_id IS NOT NULL LIMIT 5000);
    GET DIAGNOSTICS n = ROW_COUNT;
    EXIT WHEN n = 0;
    COMMIT;
  END LOOP;
  COMMIT;
END $$
