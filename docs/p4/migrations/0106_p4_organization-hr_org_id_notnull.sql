-- DRAFT (P4) - APPLY ХИЙГЭЭГҮЙ. Prod-д apply хийхээс өмнө owner/CTO батлана; өмнө нь docs/p4/prod_counts.sql-ээр хэмжээг шалга.
-- khan migrate: forward-only, runner `SET LOCAL lock_timeout=5s`, statement_timeout=15m. Дугаарыг main-ийн дараагийн сул хувилбар руу ДАХИН ДУГААРЛА.
-- GATE: CREATE UNIQUE INDEX CONCURRENTLY унасан бол INVALID index үлддэг (IF NOT EXISTS дахин ажиллуулахад алгасч '✓' болдог). lock_timeout/cancel үед ч үлдэнэ. Ийм бол гараар:
--   DROP INDEX CONCURRENTLY <index>;  CREATE UNIQUE INDEX CONCURRENTLY <index> ON <table> (id);  (psql, tx-гүй) - дараа нь migrate up дахин.
DO $$
DECLARE r record;
BEGIN
  FOR r IN SELECT c.relname FROM pg_index i JOIN pg_class c ON c.oid=i.indexrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relname='sub_employee_skill_id_uidx' AND NOT i.indisvalid LOOP
    RAISE EXCEPTION 'P4 gate: INVALID index public.sub_employee_skill_id_uidx: DROP INDEX CONCURRENTLY public.sub_employee_skill_id_uidx; CREATE UNIQUE INDEX CONCURRENTLY ... ON <tbl>(id); (psql, tx-гүй) дараа нь migrate up';
  END LOOP;
  IF NOT EXISTS (SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relname='sub_employee_skill_id_uidx') THEN RAISE EXCEPTION 'P4 gate: index public.sub_employee_skill_id_uidx алга'; END IF;
  FOR r IN SELECT c.relname FROM pg_index i JOIN pg_class c ON c.oid=i.indexrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relname='sub_employee_job_experience_id_uidx' AND NOT i.indisvalid LOOP
    RAISE EXCEPTION 'P4 gate: INVALID index public.sub_employee_job_experience_id_uidx: DROP INDEX CONCURRENTLY public.sub_employee_job_experience_id_uidx; CREATE UNIQUE INDEX CONCURRENTLY ... ON <tbl>(id); (psql, tx-гүй) дараа нь migrate up';
  END LOOP;
  IF NOT EXISTS (SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relname='sub_employee_job_experience_id_uidx') THEN RAISE EXCEPTION 'P4 gate: index public.sub_employee_job_experience_id_uidx алга'; END IF;
END $$;
-- Group 1: SET NOT NULL (valid CHECK байгаа тул full scan-гүй, PG12+), дараа нь CHECK-ийг хас.

ALTER TABLE organization.employee_extra_phones ALTER COLUMN org_id SET NOT NULL;
ALTER TABLE organization.employee_extra_phones DROP CONSTRAINT employee_extra_phones_org_id_nn;

ALTER TABLE organization.employee_extra_emails ALTER COLUMN org_id SET NOT NULL;
ALTER TABLE organization.employee_extra_emails DROP CONSTRAINT employee_extra_emails_org_id_nn;

ALTER TABLE organization.employee_extra_shifts ALTER COLUMN org_id SET NOT NULL;
ALTER TABLE organization.employee_extra_shifts DROP CONSTRAINT employee_extra_shifts_org_id_nn;

ALTER TABLE public.sub_employee_skill ALTER COLUMN org_id SET NOT NULL;
ALTER TABLE public.sub_employee_skill DROP CONSTRAINT sub_employee_skill_org_id_nn;
ALTER TABLE public.sub_employee_skill ADD CONSTRAINT sub_employee_skill_pkey PRIMARY KEY USING INDEX sub_employee_skill_id_uidx;

ALTER TABLE public.sub_employee_course ALTER COLUMN org_id SET NOT NULL;
ALTER TABLE public.sub_employee_course DROP CONSTRAINT sub_employee_course_org_id_nn;

ALTER TABLE public.sub_employee_education ALTER COLUMN org_id SET NOT NULL;
ALTER TABLE public.sub_employee_education DROP CONSTRAINT sub_employee_education_org_id_nn;

ALTER TABLE public.sub_employee_job_experience ALTER COLUMN org_id SET NOT NULL;
ALTER TABLE public.sub_employee_job_experience DROP CONSTRAINT sub_employee_job_experience_org_id_nn;
ALTER TABLE public.sub_employee_job_experience ADD CONSTRAINT sub_employee_job_experience_pkey PRIMARY KEY USING INDEX sub_employee_job_experience_id_uidx;
