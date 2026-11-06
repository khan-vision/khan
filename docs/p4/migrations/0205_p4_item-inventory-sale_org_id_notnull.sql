-- DRAFT (P4) - APPLY ХИЙГЭЭГҮЙ. Prod-д apply хийхээс өмнө owner/CTO батлана; өмнө нь docs/p4/prod_counts.sql-ээр хэмжээг шалга.
-- khan migrate: forward-only, runner `SET LOCAL lock_timeout=5s`, statement_timeout=15m. Дугаарыг main-ийн дараагийн сул хувилбар руу ДАХИН ДУГААРЛА.
-- GATE: CREATE UNIQUE INDEX CONCURRENTLY унасан бол INVALID index үлддэг (IF NOT EXISTS дахин ажиллуулахад алгасч '✓' болдог). lock_timeout/cancel үед ч үлдэнэ. Ийм бол гараар:
--   DROP INDEX CONCURRENTLY <index>;  CREATE UNIQUE INDEX CONCURRENTLY <index> ON <table> (id);  (psql, tx-гүй) - дараа нь migrate up дахин.
DO $$
DECLARE r record;
BEGIN
  FOR r IN SELECT c.relname FROM pg_index i JOIN pg_class c ON c.oid=i.indexrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='item' AND c.relname='item_details_id_uidx' AND NOT i.indisvalid LOOP
    RAISE EXCEPTION 'P4 gate: INVALID index item.item_details_id_uidx: DROP INDEX CONCURRENTLY item.item_details_id_uidx; CREATE UNIQUE INDEX CONCURRENTLY ... ON <tbl>(id); (psql, tx-гүй) дараа нь migrate up';
  END LOOP;
  IF NOT EXISTS (SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='item' AND c.relname='item_details_id_uidx') THEN RAISE EXCEPTION 'P4 gate: index item.item_details_id_uidx алга'; END IF;
END $$;
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
