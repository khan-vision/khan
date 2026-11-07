-- DRAFT (P4) - APPLY ХИЙГЭЭГҮЙ. Prod-д apply хийхээс өмнө owner/CTO батлана; өмнө нь docs/p4/prod_counts.sql-ээр хэмжээг шалга.
-- khan migrate: forward-only, runner `SET LOCAL lock_timeout=5s`, statement_timeout=15m. Дугаарыг main-ийн дараагийн сул хувилбар руу ДАХИН ДУГААРЛА.
-- Group 2: VALIDATE. Зөвхөн CHECK-тэй 3 хүснэгт. item.item_details (3/15 dangling item_id) -> _pending_owner/0216.
DO $$
DECLARE n bigint; msg text := '';
BEGIN
  SELECT count(*) INTO n FROM (SELECT id FROM item.item_details GROUP BY id HAVING count(*) > 1 OR id IS NULL) d; IF n > 0 THEN msg := msg || format(' item.item_details=%s', n); END IF;
  IF msg <> '' THEN RAISE EXCEPTION 'P4 gate: давхардсан/NULL id:% (docs/p4/verify_p4.sql #5)', msg; END IF;
END $$;
DO $$
DECLARE msg text := ''; n bigint;
BEGIN
  SELECT count(*) INTO n FROM sale.sale_order_line WHERE org_id IS NULL; IF n > 0 THEN msg := msg || format(' sale.sale_order_line=%s', n); END IF;
  SELECT count(*) INTO n FROM sale.sale_order_surcharge WHERE org_id IS NULL; IF n > 0 THEN msg := msg || format(' sale.sale_order_surcharge=%s', n); END IF;
  SELECT count(*) INTO n FROM inventory.inventory_discrepancy_responsibility_employees WHERE org_id IS NULL; IF n > 0 THEN msg := msg || format(' inventory.inventory_discrepancy_responsibility_employees=%s', n); END IF;
  IF msg <> '' THEN RAISE EXCEPTION 'P4 gate: org_id NULL мөр үлдсэн (orphan эцэг/NULL FK):%. УСТГАХГҮЙ - owner шийдээд (docs/p4/W0_RUNBOOK.md) дахин ажиллуул.', msg; END IF;
END $$;

ALTER TABLE sale.sale_order_line VALIDATE CONSTRAINT sale_order_line_org_id_nn;
ALTER TABLE sale.sale_order_surcharge VALIDATE CONSTRAINT sale_order_surcharge_org_id_nn;
ALTER TABLE inventory.inventory_discrepancy_responsibility_employees VALIDATE CONSTRAINT inventory_discrepancy_responsibility_employees_org_id_nn;
