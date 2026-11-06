-- DRAFT (P4) - APPLY ХИЙГЭЭГҮЙ. Prod-д apply хийхээс өмнө owner/CTO батлана; өмнө нь docs/p4/prod_counts.sql-ээр хэмжээг шалга.
-- khan migrate: forward-only, runner `SET LOCAL lock_timeout=5s`, statement_timeout=15m. Дугаарыг main-ийн дараагийн сул хувилбар руу ДАХИН ДУГААРЛА.
-- Group 2: VALIDATE (SHARE UPDATE EXCLUSIVE; бичилтийг хаахгүй).
ALTER TABLE item.item_details VALIDATE CONSTRAINT item_details_org_id_nn;
ALTER TABLE sale.sale_order_line VALIDATE CONSTRAINT sale_order_line_org_id_nn;
ALTER TABLE sale.sale_order_surcharge VALIDATE CONSTRAINT sale_order_surcharge_org_id_nn;
ALTER TABLE inventory.inventory_discrepancy_responsibility_employees VALIDATE CONSTRAINT inventory_discrepancy_responsibility_employees_org_id_nn;
