-- DRAFT (P4) - APPLY ХИЙГЭЭГҮЙ. Prod-д apply хийхээс өмнө owner/CTO батлана; өмнө нь docs/p4/prod_counts.sql-ээр хэмжээг шалга.
-- khan migrate: forward-only, runner `SET LOCAL lock_timeout=5s`, statement_timeout=15m. Дугаарыг main-ийн дараагийн сул хувилбар руу ДАХИН ДУГААРЛА.
-- Group 1: organization-hr - EXPAND: nullable org_id (+ NOT VALID CHECK зөвхөн orphan-гүй хүснэгтэд) + BEFORE INSERT derive trigger.
-- ЭНД ХИЙГДЭЭГҮЙ (trg горим): orphan мөртэй хүснэгтэд CHECK нэмэхгүй (NOT VALID CHECK нь orphan мөрийн UPDATE-ийг эвддэг); validate/NOT NULL -> _pending_owner/.
-- ЖИЧ: CHECK-тэй хүснэгтэд expand -> backfill хоорондох цонх богино байх ёстой (backfill хийгдээгүй мөрийн UPDATE CHECK-д унана). Дараагийн backfill файлыг даруй ажиллуул.

ALTER TABLE organization.employee_extra_phones ADD COLUMN IF NOT EXISTS org_id uuid;
ALTER TABLE organization.employee_extra_phones ADD CONSTRAINT employee_extra_phones_org_id_nn CHECK (org_id IS NOT NULL) NOT VALID;

ALTER TABLE organization.employee_extra_emails ADD COLUMN IF NOT EXISTS org_id uuid;
ALTER TABLE organization.employee_extra_emails ADD CONSTRAINT employee_extra_emails_org_id_nn CHECK (org_id IS NOT NULL) NOT VALID;

ALTER TABLE organization.employee_extra_shifts ADD COLUMN IF NOT EXISTS org_id uuid;
ALTER TABLE organization.employee_extra_shifts ADD CONSTRAINT employee_extra_shifts_org_id_nn CHECK (org_id IS NOT NULL) NOT VALID;

ALTER TABLE public.sub_employee_skill ADD COLUMN IF NOT EXISTS org_id uuid;

ALTER TABLE public.sub_employee_course ADD COLUMN IF NOT EXISTS org_id uuid;

ALTER TABLE public.sub_employee_education ADD COLUMN IF NOT EXISTS org_id uuid;

ALTER TABLE public.sub_employee_job_experience ADD COLUMN IF NOT EXISTS org_id uuid;

-- Хуучин апп (GORM/Lambda form) org_id бичдэггүй. BEFORE INSERT trigger эцэг хүснэгтээс org_id-г дүүргэнэ. Эцэг олдохгүй бол NULL хэвээр:
--   chk горим: CHECK унана (orphan insert татгалзана); trg горим: NULL-ээр орно (блоклохгүй).
-- public.sub_employee_*: employee_id = public.users.id (organization.employee.id БИШ) -> users.org_id (home org) ашиглана.
-- Апп org_id бичдэг болсны дараа trigger-ийг хасаж болно.
CREATE OR REPLACE FUNCTION organization.p4_employee_extra_phones_org_id_derive() RETURNS trigger LANGUAGE plpgsql AS $fn$
BEGIN
  IF NEW.org_id IS NULL THEN
    SELECT p.org_id INTO NEW.org_id FROM organization.employee p WHERE p.id = NEW.emp_id;
  END IF;
  RETURN NEW;
END $fn$;
DROP TRIGGER IF EXISTS p4_employee_extra_phones_org_id_derive ON organization.employee_extra_phones;
CREATE TRIGGER p4_employee_extra_phones_org_id_derive BEFORE INSERT ON organization.employee_extra_phones FOR EACH ROW EXECUTE FUNCTION organization.p4_employee_extra_phones_org_id_derive();
CREATE OR REPLACE FUNCTION organization.p4_employee_extra_emails_org_id_derive() RETURNS trigger LANGUAGE plpgsql AS $fn$
BEGIN
  IF NEW.org_id IS NULL THEN
    SELECT p.org_id INTO NEW.org_id FROM organization.employee p WHERE p.id = NEW.emp_id;
  END IF;
  RETURN NEW;
END $fn$;
DROP TRIGGER IF EXISTS p4_employee_extra_emails_org_id_derive ON organization.employee_extra_emails;
CREATE TRIGGER p4_employee_extra_emails_org_id_derive BEFORE INSERT ON organization.employee_extra_emails FOR EACH ROW EXECUTE FUNCTION organization.p4_employee_extra_emails_org_id_derive();
CREATE OR REPLACE FUNCTION organization.p4_employee_extra_shifts_org_id_derive() RETURNS trigger LANGUAGE plpgsql AS $fn$
BEGIN
  IF NEW.org_id IS NULL THEN
    SELECT p.org_id INTO NEW.org_id FROM organization.employee p WHERE p.id = NEW.emp_id;
  END IF;
  RETURN NEW;
END $fn$;
DROP TRIGGER IF EXISTS p4_employee_extra_shifts_org_id_derive ON organization.employee_extra_shifts;
CREATE TRIGGER p4_employee_extra_shifts_org_id_derive BEFORE INSERT ON organization.employee_extra_shifts FOR EACH ROW EXECUTE FUNCTION organization.p4_employee_extra_shifts_org_id_derive();
CREATE OR REPLACE FUNCTION public.p4_sub_employee_skill_org_id_derive() RETURNS trigger LANGUAGE plpgsql AS $fn$
BEGIN
  IF NEW.org_id IS NULL THEN
    SELECT p.org_id INTO NEW.org_id FROM public.users p WHERE p.id = NEW.employee_id;
  END IF;
  RETURN NEW;
END $fn$;
DROP TRIGGER IF EXISTS p4_sub_employee_skill_org_id_derive ON public.sub_employee_skill;
CREATE TRIGGER p4_sub_employee_skill_org_id_derive BEFORE INSERT ON public.sub_employee_skill FOR EACH ROW EXECUTE FUNCTION public.p4_sub_employee_skill_org_id_derive();
CREATE OR REPLACE FUNCTION public.p4_sub_employee_course_org_id_derive() RETURNS trigger LANGUAGE plpgsql AS $fn$
BEGIN
  IF NEW.org_id IS NULL THEN
    SELECT p.org_id INTO NEW.org_id FROM public.users p WHERE p.id = NEW.employee_id;
  END IF;
  RETURN NEW;
END $fn$;
DROP TRIGGER IF EXISTS p4_sub_employee_course_org_id_derive ON public.sub_employee_course;
CREATE TRIGGER p4_sub_employee_course_org_id_derive BEFORE INSERT ON public.sub_employee_course FOR EACH ROW EXECUTE FUNCTION public.p4_sub_employee_course_org_id_derive();
CREATE OR REPLACE FUNCTION public.p4_sub_employee_education_org_id_derive() RETURNS trigger LANGUAGE plpgsql AS $fn$
BEGIN
  IF NEW.org_id IS NULL THEN
    SELECT p.org_id INTO NEW.org_id FROM public.users p WHERE p.id = NEW.employee_id;
  END IF;
  RETURN NEW;
END $fn$;
DROP TRIGGER IF EXISTS p4_sub_employee_education_org_id_derive ON public.sub_employee_education;
CREATE TRIGGER p4_sub_employee_education_org_id_derive BEFORE INSERT ON public.sub_employee_education FOR EACH ROW EXECUTE FUNCTION public.p4_sub_employee_education_org_id_derive();
CREATE OR REPLACE FUNCTION public.p4_sub_employee_job_experience_org_id_derive() RETURNS trigger LANGUAGE plpgsql AS $fn$
BEGIN
  IF NEW.org_id IS NULL THEN
    SELECT p.org_id INTO NEW.org_id FROM public.users p WHERE p.id = NEW.employee_id;
  END IF;
  RETURN NEW;
END $fn$;
DROP TRIGGER IF EXISTS p4_sub_employee_job_experience_org_id_derive ON public.sub_employee_job_experience;
CREATE TRIGGER p4_sub_employee_job_experience_org_id_derive BEFORE INSERT ON public.sub_employee_job_experience FOR EACH ROW EXECUTE FUNCTION public.p4_sub_employee_job_experience_org_id_derive();
