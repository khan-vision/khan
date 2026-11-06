-- DRAFT (P4) - APPLY ХИЙГЭЭГҮЙ. Prod-д apply хийхээс өмнө owner/CTO батлана; өмнө нь docs/p4/prod_counts.sql-ээр хэмжээг шалга.
-- khan migrate: forward-only, runner `SET LOCAL lock_timeout=5s`, statement_timeout=15m. Дугаарыг main-ийн дараагийн сул хувилбар руу ДАХИН ДУГААРЛА.
-- Group 2: RLS (soft горим = tenant.PolicySQL(PolicySoft)). Апп DB role NOSUPERUSER/NOBYPASSRLS эсэхийг prod-д ЭХЛЭЭД шалга.
-- Тэмдэглэл: энэ нь `khan schema rls -mode soft`-ийн гаралттай ижил; org_id-ийн төрлөөр дахин үүсгэж болно.

ALTER TABLE item.item_details ENABLE ROW LEVEL SECURITY;
ALTER TABLE item.item_details FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS khan_tenant_isolation ON item.item_details;
CREATE POLICY khan_tenant_isolation ON item.item_details
  USING ((NULLIF(current_setting('app.org_id', true), '') IS NULL OR current_setting('app.bypass_rls', true) = 'on' OR org_id = NULLIF(current_setting('app.org_id', true), '')::uuid))
  WITH CHECK ((NULLIF(current_setting('app.org_id', true), '') IS NULL OR current_setting('app.bypass_rls', true) = 'on' OR org_id = NULLIF(current_setting('app.org_id', true), '')::uuid));

ALTER TABLE sale.sale_order_line ENABLE ROW LEVEL SECURITY;
ALTER TABLE sale.sale_order_line FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS khan_tenant_isolation ON sale.sale_order_line;
CREATE POLICY khan_tenant_isolation ON sale.sale_order_line
  USING ((NULLIF(current_setting('app.org_id', true), '') IS NULL OR current_setting('app.bypass_rls', true) = 'on' OR org_id = NULLIF(current_setting('app.org_id', true), '')::uuid))
  WITH CHECK ((NULLIF(current_setting('app.org_id', true), '') IS NULL OR current_setting('app.bypass_rls', true) = 'on' OR org_id = NULLIF(current_setting('app.org_id', true), '')::uuid));

ALTER TABLE sale.sale_order_surcharge ENABLE ROW LEVEL SECURITY;
ALTER TABLE sale.sale_order_surcharge FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS khan_tenant_isolation ON sale.sale_order_surcharge;
CREATE POLICY khan_tenant_isolation ON sale.sale_order_surcharge
  USING ((NULLIF(current_setting('app.org_id', true), '') IS NULL OR current_setting('app.bypass_rls', true) = 'on' OR org_id = NULLIF(current_setting('app.org_id', true), '')::uuid))
  WITH CHECK ((NULLIF(current_setting('app.org_id', true), '') IS NULL OR current_setting('app.bypass_rls', true) = 'on' OR org_id = NULLIF(current_setting('app.org_id', true), '')::uuid));

ALTER TABLE inventory.inventory_discrepancy_responsibility_employees ENABLE ROW LEVEL SECURITY;
ALTER TABLE inventory.inventory_discrepancy_responsibility_employees FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS khan_tenant_isolation ON inventory.inventory_discrepancy_responsibility_employees;
CREATE POLICY khan_tenant_isolation ON inventory.inventory_discrepancy_responsibility_employees
  USING ((NULLIF(current_setting('app.org_id', true), '') IS NULL OR current_setting('app.bypass_rls', true) = 'on' OR org_id = NULLIF(current_setting('app.org_id', true), '')::uuid))
  WITH CHECK ((NULLIF(current_setting('app.org_id', true), '') IS NULL OR current_setting('app.bypass_rls', true) = 'on' OR org_id = NULLIF(current_setting('app.org_id', true), '')::uuid));
