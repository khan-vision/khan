-- khan:no-transaction
-- DRAFT (P4) - APPLY ХИЙГЭЭГҮЙ. Prod-д apply хийхээс өмнө owner/CTO батлана; өмнө нь docs/p4/prod_counts.sql-ээр хэмжээг шалга.
-- khan migrate: forward-only, runner `SET LOCAL lock_timeout=5s`, statement_timeout=15m. Дугаарыг main-ийн дараагийн сул хувилбар руу ДАХИН ДУГААРЛА.
-- Group 2: BACKFILL (batch 5000, batch бүр COMMIT). Нэг statement (DO) тул no-transaction.
-- Эцэг хүснэгтгүй (orphan) мөр NULL хэвээр үлдэж, дараагийн VALIDATE унана -> prod_counts.sql #orphan-оор өмнө нь шалга/цэвэрл (human confirm).

DO $$
DECLARE n bigint;
BEGIN
  -- item.item_details.item_id -> item.item.org_id
  LOOP
    UPDATE item.item_details t SET org_id = p.org_id
      FROM item.item p
     WHERE p.id = t.item_id
       AND t.ctid IN (SELECT t2.ctid FROM item.item_details t2 JOIN item.item p2 ON p2.id = t2.item_id
                       WHERE t2.org_id IS NULL AND p2.org_id IS NOT NULL LIMIT 5000);
    GET DIAGNOSTICS n = ROW_COUNT;
    EXIT WHEN n = 0;
    COMMIT;
  END LOOP;
  COMMIT;
  -- sale.sale_order_line.order_id -> sale.sale_order.org_id
  LOOP
    UPDATE sale.sale_order_line t SET org_id = p.org_id
      FROM sale.sale_order p
     WHERE p.id = t.order_id
       AND t.ctid IN (SELECT t2.ctid FROM sale.sale_order_line t2 JOIN sale.sale_order p2 ON p2.id = t2.order_id
                       WHERE t2.org_id IS NULL AND p2.org_id IS NOT NULL LIMIT 5000);
    GET DIAGNOSTICS n = ROW_COUNT;
    EXIT WHEN n = 0;
    COMMIT;
  END LOOP;
  COMMIT;
  -- sale.sale_order_surcharge.orders_id -> sale.sale_order.org_id
  LOOP
    UPDATE sale.sale_order_surcharge t SET org_id = p.org_id
      FROM sale.sale_order p
     WHERE p.id = t.orders_id
       AND t.ctid IN (SELECT t2.ctid FROM sale.sale_order_surcharge t2 JOIN sale.sale_order p2 ON p2.id = t2.orders_id
                       WHERE t2.org_id IS NULL AND p2.org_id IS NOT NULL LIMIT 5000);
    GET DIAGNOSTICS n = ROW_COUNT;
    EXIT WHEN n = 0;
    COMMIT;
  END LOOP;
  COMMIT;
  -- inventory.inventory_discrepancy_responsibility_employees.inventory_discrepancy_responsibility_id -> inventory.inventory_discrepancy_responsibility.org_id
  LOOP
    UPDATE inventory.inventory_discrepancy_responsibility_employees t SET org_id = p.org_id
      FROM inventory.inventory_discrepancy_responsibility p
     WHERE p.id = t.inventory_discrepancy_responsibility_id
       AND t.ctid IN (SELECT t2.ctid FROM inventory.inventory_discrepancy_responsibility_employees t2 JOIN inventory.inventory_discrepancy_responsibility p2 ON p2.id = t2.inventory_discrepancy_responsibility_id
                       WHERE t2.org_id IS NULL AND p2.org_id IS NOT NULL LIMIT 5000);
    GET DIAGNOSTICS n = ROW_COUNT;
    EXIT WHEN n = 0;
    COMMIT;
  END LOOP;
  COMMIT;
END $$
