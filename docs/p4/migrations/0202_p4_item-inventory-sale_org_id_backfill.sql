-- khan:no-transaction
-- DRAFT (P4) - APPLY ХИЙГЭЭГҮЙ. Prod-д apply хийхээс өмнө owner/CTO батлана; өмнө нь docs/p4/prod_counts.sql-ээр хэмжээг шалга.
-- khan migrate: forward-only, runner `SET LOCAL lock_timeout=5s`, statement_timeout=15m. Дугаарыг main-ийн дараагийн сул хувилбар руу ДАХИН ДУГААРЛА.
-- Group 1: organization-hr - EXPAND: nullable org_id + NOT VALID CHECK (богино lock).
-- Group 2: BACKFILL. ctid блок-мужаар (TID range scan, индексгүй ч шугаман; batch=200 блок, batch бүр COMMIT).
-- Эцэггүй (orphan) мөр NULL үлдэнэ -> дараагийн VALIDATE migration-ий gate тоог гаргаж унана (docs/p4/verify_p4.sql).

DO $$
DECLARE b bigint; maxb bigint; n bigint;
BEGIN
  -- item.item_details.item_id -> item.item.org_id
  b := 0; maxb := pg_relation_size('item.item_details'::regclass) / current_setting('block_size')::bigint + 1;
  WHILE b <= maxb LOOP
    UPDATE item.item_details t SET org_id = p.org_id FROM item.item p
     WHERE p.id = t.item_id AND t.org_id IS NULL AND p.org_id IS NOT NULL
       AND t.ctid >= format('(%s,0)', b)::tid AND t.ctid < format('(%s,0)', b + 200)::tid;
    b := b + 200;
    COMMIT;
  END LOOP;
  -- sale.sale_order_line.order_id -> sale.sale_order.org_id
  b := 0; maxb := pg_relation_size('sale.sale_order_line'::regclass) / current_setting('block_size')::bigint + 1;
  WHILE b <= maxb LOOP
    UPDATE sale.sale_order_line t SET org_id = p.org_id FROM sale.sale_order p
     WHERE p.id = t.order_id AND t.org_id IS NULL AND p.org_id IS NOT NULL
       AND t.ctid >= format('(%s,0)', b)::tid AND t.ctid < format('(%s,0)', b + 200)::tid;
    b := b + 200;
    COMMIT;
  END LOOP;
  -- sale.sale_order_surcharge.orders_id -> sale.sale_order.org_id
  b := 0; maxb := pg_relation_size('sale.sale_order_surcharge'::regclass) / current_setting('block_size')::bigint + 1;
  WHILE b <= maxb LOOP
    UPDATE sale.sale_order_surcharge t SET org_id = p.org_id FROM sale.sale_order p
     WHERE p.id = t.orders_id AND t.org_id IS NULL AND p.org_id IS NOT NULL
       AND t.ctid >= format('(%s,0)', b)::tid AND t.ctid < format('(%s,0)', b + 200)::tid;
    b := b + 200;
    COMMIT;
  END LOOP;
  -- inventory.inventory_discrepancy_responsibility_employees.inventory_discrepancy_responsibility_id -> inventory.inventory_discrepancy_responsibility.org_id
  b := 0; maxb := pg_relation_size('inventory.inventory_discrepancy_responsibility_employees'::regclass) / current_setting('block_size')::bigint + 1;
  WHILE b <= maxb LOOP
    UPDATE inventory.inventory_discrepancy_responsibility_employees t SET org_id = p.org_id FROM inventory.inventory_discrepancy_responsibility p
     WHERE p.id = t.inventory_discrepancy_responsibility_id AND t.org_id IS NULL AND p.org_id IS NOT NULL
       AND t.ctid >= format('(%s,0)', b)::tid AND t.ctid < format('(%s,0)', b + 200)::tid;
    b := b + 200;
    COMMIT;
  END LOOP;
END $$
