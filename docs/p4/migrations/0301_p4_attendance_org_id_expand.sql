-- DRAFT (P4) - APPLY ХИЙГЭЭГҮЙ. Prod-д apply хийхээс өмнө owner/CTO батлана; өмнө нь docs/p4/prod_counts.sql-ээр хэмжээг шалга.
-- khan migrate: forward-only, runner `SET LOCAL lock_timeout=5s`, statement_timeout=15m. Дугаарыг main-ийн дараагийн сул хувилбар руу ДАХИН ДУГААРЛА.
-- Group 3: attendance - EXPAND: nullable org_id + NOT VALID CHECK (богино lock).

ALTER TABLE attendance.sub_shift ADD COLUMN IF NOT EXISTS org_id uuid;
ALTER TABLE attendance.sub_shift ADD CONSTRAINT sub_shift_org_id_nn CHECK (org_id IS NOT NULL) NOT VALID;

-- Хуучин апп org_id бичдэггүй тул NOT VALID CHECK нь INSERT-ийг шууд эвдэнэ. BEFORE INSERT trigger эцэг хүснэгтээс org_id-г автоматаар дүүргэнэ
-- (эцэг олдохгүй бол NULL хэвээр -> CHECK унана = orphan insert татгалзана). Апп org_id бичдэг болсны дараа trigger-ийг хасаж болно.
CREATE OR REPLACE FUNCTION attendance.p4_sub_shift_org_id_derive() RETURNS trigger LANGUAGE plpgsql AS $fn$
BEGIN
  IF NEW.org_id IS NULL THEN
    SELECT p.org_id INTO NEW.org_id FROM attendance.shift p WHERE p.id = NEW.shift_id;
  END IF;
  RETURN NEW;
END $fn$;
DROP TRIGGER IF EXISTS p4_sub_shift_org_id_derive ON attendance.sub_shift;
CREATE TRIGGER p4_sub_shift_org_id_derive BEFORE INSERT ON attendance.sub_shift FOR EACH ROW EXECUTE FUNCTION attendance.p4_sub_shift_org_id_derive();
