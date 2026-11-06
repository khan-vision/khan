-- DRAFT (P4) - APPLY ХИЙГЭЭГҮЙ. Prod-д apply хийхээс өмнө owner/CTO батлана; өмнө нь docs/p4/prod_counts.sql-ээр хэмжээг шалга.
-- khan migrate: forward-only, runner `SET LOCAL lock_timeout=5s`, statement_timeout=15m. Дугаарыг main-ийн дараагийн сул хувилбар руу ДАХИН ДУГААРЛА.
-- Group 3: RLS (soft горим = tenant.PolicySQL(PolicySoft)). Апп DB role NOSUPERUSER/NOBYPASSRLS эсэхийг prod-д ЭХЛЭЭД шалга.
-- Тэмдэглэл: энэ нь `khan schema rls -mode soft`-ийн гаралттай ижил; org_id-ийн төрлөөр дахин үүсгэж болно.

ALTER TABLE attendance.sub_shift ENABLE ROW LEVEL SECURITY;
ALTER TABLE attendance.sub_shift FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS khan_tenant_isolation ON attendance.sub_shift;
CREATE POLICY khan_tenant_isolation ON attendance.sub_shift
  USING ((NULLIF(current_setting('app.org_id', true), '') IS NULL OR current_setting('app.bypass_rls', true) = 'on' OR org_id = NULLIF(current_setting('app.org_id', true), '')::uuid))
  WITH CHECK ((NULLIF(current_setting('app.org_id', true), '') IS NULL OR current_setting('app.bypass_rls', true) = 'on' OR org_id = NULLIF(current_setting('app.org_id', true), '')::uuid));
