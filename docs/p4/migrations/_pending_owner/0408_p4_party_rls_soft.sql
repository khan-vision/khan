-- DRAFT (P4) - APPLY ХИЙГЭЭГҮЙ. Prod-д apply хийхээс өмнө owner/CTO батлана; өмнө нь docs/p4/prod_counts.sql-ээр хэмжээг шалга.
-- khan migrate: forward-only, runner `SET LOCAL lock_timeout=5s`, statement_timeout=15m. Дугаарыг main-ийн дараагийн сул хувилбар руу ДАХИН ДУГААРЛА.
-- Group 4: RLS soft. (G1-ийн rls файлтай ижил загвар; khan schema rls -schema party -mode soft-оор үүсгэж болно.)

ALTER TABLE party.relationship ENABLE ROW LEVEL SECURITY;
ALTER TABLE party.relationship FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS khan_tenant_isolation ON party.relationship;
CREATE POLICY khan_tenant_isolation ON party.relationship
  USING ((NULLIF(current_setting('app.org_id', true), '') IS NULL OR current_setting('app.bypass_rls', true) = 'on' OR org_id = NULLIF(current_setting('app.org_id', true), '')::uuid))
  WITH CHECK ((NULLIF(current_setting('app.org_id', true), '') IS NULL OR current_setting('app.bypass_rls', true) = 'on' OR org_id = NULLIF(current_setting('app.org_id', true), '')::uuid));
