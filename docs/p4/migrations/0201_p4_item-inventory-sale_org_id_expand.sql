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

-- Хуучин апп org_id бичдэггүй тул NOT VALID CHECK нь INSERT-ийг шууд эвдэнэ. BEFORE INSERT trigger эцэг хүснэгтээс org_id-г автоматаар дүүргэнэ
-- (эцэг олдохгүй бол NULL хэвээр -> CHECK унана = orphan insert татгалзана). Апп org_id бичдэг болсны дараа trigger-ийг хасаж болно.
CREATE OR REPLACE FUNCTION item.p4_item_details_org_id_derive() RETURNS trigger LANGUAGE plpgsql AS $fn$
BEGIN
  IF NEW.org_id IS NULL THEN
    SELECT p.org_id INTO NEW.org_id FROM item.item p WHERE p.id = NEW.item_id;
  END IF;
  RETURN NEW;
END $fn$;
DROP TRIGGER IF EXISTS p4_item_details_org_id_derive ON item.item_details;
CREATE TRIGGER p4_item_details_org_id_derive BEFORE INSERT ON item.item_details FOR EACH ROW EXECUTE FUNCTION item.p4_item_details_org_id_derive();
CREATE OR REPLACE FUNCTION sale.p4_sale_order_line_org_id_derive() RETURNS trigger LANGUAGE plpgsql AS $fn$
BEGIN
  IF NEW.org_id IS NULL THEN
    SELECT p.org_id INTO NEW.org_id FROM sale.sale_order p WHERE p.id = NEW.order_id;
  END IF;
  RETURN NEW;
END $fn$;
DROP TRIGGER IF EXISTS p4_sale_order_line_org_id_derive ON sale.sale_order_line;
CREATE TRIGGER p4_sale_order_line_org_id_derive BEFORE INSERT ON sale.sale_order_line FOR EACH ROW EXECUTE FUNCTION sale.p4_sale_order_line_org_id_derive();
CREATE OR REPLACE FUNCTION sale.p4_sale_order_surcharge_org_id_derive() RETURNS trigger LANGUAGE plpgsql AS $fn$
BEGIN
  IF NEW.org_id IS NULL THEN
    SELECT p.org_id INTO NEW.org_id FROM sale.sale_order p WHERE p.id = NEW.orders_id;
  END IF;
  RETURN NEW;
END $fn$;
DROP TRIGGER IF EXISTS p4_sale_order_surcharge_org_id_derive ON sale.sale_order_surcharge;
CREATE TRIGGER p4_sale_order_surcharge_org_id_derive BEFORE INSERT ON sale.sale_order_surcharge FOR EACH ROW EXECUTE FUNCTION sale.p4_sale_order_surcharge_org_id_derive();
CREATE OR REPLACE FUNCTION inventory.p4_inventory_discrepancy_responsibility_employees_org_id_derive() RETURNS trigger LANGUAGE plpgsql AS $fn$
BEGIN
  IF NEW.org_id IS NULL THEN
    SELECT p.org_id INTO NEW.org_id FROM inventory.inventory_discrepancy_responsibility p WHERE p.id = NEW.inventory_discrepancy_responsibility_id;
  END IF;
  RETURN NEW;
END $fn$;
DROP TRIGGER IF EXISTS p4_inventory_discrepancy_responsibility_employees_org_id_derive ON inventory.inventory_discrepancy_responsibility_employees;
CREATE TRIGGER p4_inventory_discrepancy_responsibility_employees_org_id_derive BEFORE INSERT ON inventory.inventory_discrepancy_responsibility_employees FOR EACH ROW EXECUTE FUNCTION inventory.p4_inventory_discrepancy_responsibility_employees_org_id_derive();
