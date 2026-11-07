-- DRAFT (P4) - APPLY ХИЙГЭЭГҮЙ. Prod-д apply хийхээс өмнө owner/CTO батлана; өмнө нь docs/p4/prod_counts.sql-ээр хэмжээг шалга.
-- khan migrate: forward-only, runner `SET LOCAL lock_timeout=5s`, statement_timeout=15m. Дугаарыг main-ийн дараагийн сул хувилбар руу ДАХИН ДУГААРЛА.
-- Group 1: RLS (soft горим = tenant.PolicySQL(PolicySoft)). Апп DB role NOSUPERUSER/NOBYPASSRLS эсэхийг prod-д ЭХЛЭЭД шалга.
-- Тэмдэглэл: энэ нь `khan schema rls -mode soft`-ийн гаралттай ижил; org_id-ийн төрлөөр дахин үүсгэж болно.

ALTER TABLE organization.employee_extra_phones ENABLE ROW LEVEL SECURITY;
ALTER TABLE organization.employee_extra_phones FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS khan_tenant_isolation ON organization.employee_extra_phones;
CREATE POLICY khan_tenant_isolation ON organization.employee_extra_phones
  USING ((NULLIF(current_setting('app.org_id', true), '') IS NULL OR current_setting('app.bypass_rls', true) = 'on' OR org_id = NULLIF(current_setting('app.org_id', true), '')::uuid))
  WITH CHECK ((NULLIF(current_setting('app.org_id', true), '') IS NULL OR current_setting('app.bypass_rls', true) = 'on' OR org_id = NULLIF(current_setting('app.org_id', true), '')::uuid));

ALTER TABLE organization.employee_extra_emails ENABLE ROW LEVEL SECURITY;
ALTER TABLE organization.employee_extra_emails FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS khan_tenant_isolation ON organization.employee_extra_emails;
CREATE POLICY khan_tenant_isolation ON organization.employee_extra_emails
  USING ((NULLIF(current_setting('app.org_id', true), '') IS NULL OR current_setting('app.bypass_rls', true) = 'on' OR org_id = NULLIF(current_setting('app.org_id', true), '')::uuid))
  WITH CHECK ((NULLIF(current_setting('app.org_id', true), '') IS NULL OR current_setting('app.bypass_rls', true) = 'on' OR org_id = NULLIF(current_setting('app.org_id', true), '')::uuid));

ALTER TABLE organization.employee_extra_shifts ENABLE ROW LEVEL SECURITY;
ALTER TABLE organization.employee_extra_shifts FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS khan_tenant_isolation ON organization.employee_extra_shifts;
CREATE POLICY khan_tenant_isolation ON organization.employee_extra_shifts
  USING ((NULLIF(current_setting('app.org_id', true), '') IS NULL OR current_setting('app.bypass_rls', true) = 'on' OR org_id = NULLIF(current_setting('app.org_id', true), '')::uuid))
  WITH CHECK ((NULLIF(current_setting('app.org_id', true), '') IS NULL OR current_setting('app.bypass_rls', true) = 'on' OR org_id = NULLIF(current_setting('app.org_id', true), '')::uuid));

ALTER TABLE public.sub_employee_skill ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.sub_employee_skill FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS khan_tenant_isolation ON public.sub_employee_skill;
CREATE POLICY khan_tenant_isolation ON public.sub_employee_skill
  USING ((NULLIF(current_setting('app.org_id', true), '') IS NULL OR current_setting('app.bypass_rls', true) = 'on' OR org_id = NULLIF(current_setting('app.org_id', true), '')::uuid))
  WITH CHECK ((NULLIF(current_setting('app.org_id', true), '') IS NULL OR current_setting('app.bypass_rls', true) = 'on' OR org_id = NULLIF(current_setting('app.org_id', true), '')::uuid));

ALTER TABLE public.sub_employee_course ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.sub_employee_course FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS khan_tenant_isolation ON public.sub_employee_course;
CREATE POLICY khan_tenant_isolation ON public.sub_employee_course
  USING ((NULLIF(current_setting('app.org_id', true), '') IS NULL OR current_setting('app.bypass_rls', true) = 'on' OR org_id = NULLIF(current_setting('app.org_id', true), '')::uuid))
  WITH CHECK ((NULLIF(current_setting('app.org_id', true), '') IS NULL OR current_setting('app.bypass_rls', true) = 'on' OR org_id = NULLIF(current_setting('app.org_id', true), '')::uuid));

ALTER TABLE public.sub_employee_education ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.sub_employee_education FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS khan_tenant_isolation ON public.sub_employee_education;
CREATE POLICY khan_tenant_isolation ON public.sub_employee_education
  USING ((NULLIF(current_setting('app.org_id', true), '') IS NULL OR current_setting('app.bypass_rls', true) = 'on' OR org_id = NULLIF(current_setting('app.org_id', true), '')::uuid))
  WITH CHECK ((NULLIF(current_setting('app.org_id', true), '') IS NULL OR current_setting('app.bypass_rls', true) = 'on' OR org_id = NULLIF(current_setting('app.org_id', true), '')::uuid));

ALTER TABLE public.sub_employee_job_experience ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.sub_employee_job_experience FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS khan_tenant_isolation ON public.sub_employee_job_experience;
CREATE POLICY khan_tenant_isolation ON public.sub_employee_job_experience
  USING ((NULLIF(current_setting('app.org_id', true), '') IS NULL OR current_setting('app.bypass_rls', true) = 'on' OR org_id = NULLIF(current_setting('app.org_id', true), '')::uuid))
  WITH CHECK ((NULLIF(current_setting('app.org_id', true), '') IS NULL OR current_setting('app.bypass_rls', true) = 'on' OR org_id = NULLIF(current_setting('app.org_id', true), '')::uuid));
