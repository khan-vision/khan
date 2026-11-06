-- DRAFT (P4) - APPLY ХИЙГЭЭГҮЙ. Prod-д apply хийхээс өмнө owner/CTO батлана; өмнө нь docs/p4/prod_counts.sql-ээр хэмжээг шалга.
-- khan migrate: forward-only, runner `SET LOCAL lock_timeout=5s`, statement_timeout=15m. Дугаарыг main-ийн дараагийн сул хувилбар руу ДАХИН ДУГААРЛА.
-- Group 1: organization-hr - EXPAND: nullable org_id + NOT VALID CHECK (богино lock).

ALTER TABLE organization.employee_extra_phones ADD COLUMN IF NOT EXISTS org_id uuid;
ALTER TABLE organization.employee_extra_phones ADD CONSTRAINT employee_extra_phones_org_id_nn CHECK (org_id IS NOT NULL) NOT VALID;

ALTER TABLE organization.employee_extra_emails ADD COLUMN IF NOT EXISTS org_id uuid;
ALTER TABLE organization.employee_extra_emails ADD CONSTRAINT employee_extra_emails_org_id_nn CHECK (org_id IS NOT NULL) NOT VALID;

ALTER TABLE organization.employee_extra_shifts ADD COLUMN IF NOT EXISTS org_id uuid;
ALTER TABLE organization.employee_extra_shifts ADD CONSTRAINT employee_extra_shifts_org_id_nn CHECK (org_id IS NOT NULL) NOT VALID;

ALTER TABLE public.sub_employee_skill ADD COLUMN IF NOT EXISTS org_id uuid;
ALTER TABLE public.sub_employee_skill ADD CONSTRAINT sub_employee_skill_org_id_nn CHECK (org_id IS NOT NULL) NOT VALID;

ALTER TABLE public.sub_employee_course ADD COLUMN IF NOT EXISTS org_id uuid;
ALTER TABLE public.sub_employee_course ADD CONSTRAINT sub_employee_course_org_id_nn CHECK (org_id IS NOT NULL) NOT VALID;

ALTER TABLE public.sub_employee_education ADD COLUMN IF NOT EXISTS org_id uuid;
ALTER TABLE public.sub_employee_education ADD CONSTRAINT sub_employee_education_org_id_nn CHECK (org_id IS NOT NULL) NOT VALID;

ALTER TABLE public.sub_employee_job_experience ADD COLUMN IF NOT EXISTS org_id uuid;
ALTER TABLE public.sub_employee_job_experience ADD CONSTRAINT sub_employee_job_experience_org_id_nn CHECK (org_id IS NOT NULL) NOT VALID;
