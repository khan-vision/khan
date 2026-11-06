-- DRAFT (P4) - APPLY ХИЙГЭЭГҮЙ. Prod-д apply хийхээс өмнө owner/CTO батлана; өмнө нь docs/p4/prod_counts.sql-ээр хэмжээг шалга.
-- khan migrate: forward-only, runner `SET LOCAL lock_timeout=5s`, statement_timeout=15m. Дугаарыг main-ийн дараагийн сул хувилбар руу ДАХИН ДУГААРЛА.
-- Group 2: SET NOT NULL (valid CHECK байгаа тул full scan-гүй, PG12+), дараа нь CHECK-ийг хас.

ALTER TABLE item.item_details ALTER COLUMN org_id SET NOT NULL;
ALTER TABLE item.item_details DROP CONSTRAINT item_details_org_id_nn;
ALTER TABLE item.item_details ADD CONSTRAINT item_details_pkey PRIMARY KEY USING INDEX item_details_id_uidx;

ALTER TABLE sale.sale_order_line ALTER COLUMN org_id SET NOT NULL;
ALTER TABLE sale.sale_order_line DROP CONSTRAINT sale_order_line_org_id_nn;

ALTER TABLE sale.sale_order_surcharge ALTER COLUMN org_id SET NOT NULL;
ALTER TABLE sale.sale_order_surcharge DROP CONSTRAINT sale_order_surcharge_org_id_nn;

ALTER TABLE inventory.inventory_discrepancy_responsibility_employees ALTER COLUMN org_id SET NOT NULL;
ALTER TABLE inventory.inventory_discrepancy_responsibility_employees DROP CONSTRAINT inventory_discrepancy_responsibility_employees_org_id_nn;
