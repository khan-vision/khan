-- DRAFT (P4) - APPLY ХИЙГЭЭГҮЙ. Prod-д apply хийхээс өмнө owner/CTO батлана; өмнө нь docs/p4/prod_counts.sql-ээр хэмжээг шалга.
-- khan migrate: forward-only, runner `SET LOCAL lock_timeout=5s`, statement_timeout=15m. Дугаарыг main-ийн дараагийн сул хувилбар руу ДАХИН ДУГААРЛА.
-- Group 2: item-inventory-sale - EXPAND: nullable org_id + NOT VALID CHECK (богино lock).

ALTER TABLE item.item_details ADD COLUMN IF NOT EXISTS org_id uuid;
ALTER TABLE item.item_details ADD CONSTRAINT item_details_org_id_nn CHECK (org_id IS NOT NULL) NOT VALID;

ALTER TABLE sale.sale_order_line ADD COLUMN IF NOT EXISTS org_id uuid;
ALTER TABLE sale.sale_order_line ADD CONSTRAINT sale_order_line_org_id_nn CHECK (org_id IS NOT NULL) NOT VALID;

ALTER TABLE sale.sale_order_surcharge ADD COLUMN IF NOT EXISTS org_id uuid;
ALTER TABLE sale.sale_order_surcharge ADD CONSTRAINT sale_order_surcharge_org_id_nn CHECK (org_id IS NOT NULL) NOT VALID;

ALTER TABLE inventory.inventory_discrepancy_responsibility_employees ADD COLUMN IF NOT EXISTS org_id uuid;
ALTER TABLE inventory.inventory_discrepancy_responsibility_employees ADD CONSTRAINT inventory_discrepancy_responsibility_employees_org_id_nn CHECK (org_id IS NOT NULL) NOT VALID;
