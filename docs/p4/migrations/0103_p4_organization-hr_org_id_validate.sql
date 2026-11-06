-- DRAFT (P4) - APPLY ХИЙГЭЭГҮЙ. Prod-д apply хийхээс өмнө owner/CTO батлана; өмнө нь docs/p4/prod_counts.sql-ээр хэмжээг шалга.
-- khan migrate: forward-only, runner `SET LOCAL lock_timeout=5s`, statement_timeout=15m. Дугаарыг main-ийн дараагийн сул хувилбар руу ДАХИН ДУГААРЛА.
-- Group 1: VALIDATE (SHARE UPDATE EXCLUSIVE; бичилтийг хаахгүй).
-- GATE 2: PK-гүй хүснэгтийн id давхардал/NULL -> дараагийн UNIQUE INDEX CONCURRENTLY унаж INVALID index үлдээхээс ӨМНӨ барина.
DO $$
DECLARE n bigint; msg text := '';
BEGIN
  SELECT count(*) INTO n FROM (SELECT id FROM public.sub_employee_skill GROUP BY id HAVING count(*) > 1 OR id IS NULL) d; IF n > 0 THEN msg := msg || format(' public.sub_employee_skill=%s', n); END IF;
  SELECT count(*) INTO n FROM (SELECT id FROM public.sub_employee_job_experience GROUP BY id HAVING count(*) > 1 OR id IS NULL) d; IF n > 0 THEN msg := msg || format(' public.sub_employee_job_experience=%s', n); END IF;
  IF msg <> '' THEN RAISE EXCEPTION 'P4 gate: давхардсан/NULL id:% (docs/p4/verify_p4.sql #5)', msg; END IF;
END $$;
-- GATE: backfill-ийн дараа org_id NULL үлдсэн мөр байвал ТОГТООСОН мессежээр унана (VALIDATE-ийн ойлгомжгүй алдаа биш). Жагсаалт: docs/p4/verify_p4.sql
DO $$
DECLARE msg text := ''; n bigint;
BEGIN
  SELECT count(*) INTO n FROM organization.employee_extra_phones WHERE org_id IS NULL; IF n > 0 THEN msg := msg || format(' organization.employee_extra_phones=%s', n); END IF;
  SELECT count(*) INTO n FROM organization.employee_extra_emails WHERE org_id IS NULL; IF n > 0 THEN msg := msg || format(' organization.employee_extra_emails=%s', n); END IF;
  SELECT count(*) INTO n FROM organization.employee_extra_shifts WHERE org_id IS NULL; IF n > 0 THEN msg := msg || format(' organization.employee_extra_shifts=%s', n); END IF;
  SELECT count(*) INTO n FROM public.sub_employee_skill WHERE org_id IS NULL; IF n > 0 THEN msg := msg || format(' public.sub_employee_skill=%s', n); END IF;
  SELECT count(*) INTO n FROM public.sub_employee_course WHERE org_id IS NULL; IF n > 0 THEN msg := msg || format(' public.sub_employee_course=%s', n); END IF;
  SELECT count(*) INTO n FROM public.sub_employee_education WHERE org_id IS NULL; IF n > 0 THEN msg := msg || format(' public.sub_employee_education=%s', n); END IF;
  SELECT count(*) INTO n FROM public.sub_employee_job_experience WHERE org_id IS NULL; IF n > 0 THEN msg := msg || format(' public.sub_employee_job_experience=%s', n); END IF;
  IF msg <> '' THEN RAISE EXCEPTION 'P4 gate: org_id NULL мөр үлдсэн (orphan эцэг/NULL FK):%. Цэвэрлэж/шийдээд дахин ажиллуул.', msg; END IF;
END $$;

ALTER TABLE organization.employee_extra_phones VALIDATE CONSTRAINT employee_extra_phones_org_id_nn;
ALTER TABLE organization.employee_extra_emails VALIDATE CONSTRAINT employee_extra_emails_org_id_nn;
ALTER TABLE organization.employee_extra_shifts VALIDATE CONSTRAINT employee_extra_shifts_org_id_nn;
ALTER TABLE public.sub_employee_skill VALIDATE CONSTRAINT sub_employee_skill_org_id_nn;
ALTER TABLE public.sub_employee_course VALIDATE CONSTRAINT sub_employee_course_org_id_nn;
ALTER TABLE public.sub_employee_education VALIDATE CONSTRAINT sub_employee_education_org_id_nn;
ALTER TABLE public.sub_employee_job_experience VALIDATE CONSTRAINT sub_employee_job_experience_org_id_nn;
