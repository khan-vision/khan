-- DRAFT (P4) - APPLY ХИЙГЭЭГҮЙ. Prod-д apply хийхээс өмнө owner/CTO батлана; өмнө нь docs/p4/prod_counts.sql-ээр хэмжээг шалга.
-- khan migrate: forward-only, runner `SET LOCAL lock_timeout=5s`, statement_timeout=15m. Дугаарыг main-ийн дараагийн сул хувилбар руу ДАХИН ДУГААРЛА.
-- Group 3: attendance - EXPAND: nullable org_id (+ NOT VALID CHECK зөвхөн orphan-гүй хүснэгтэд) + BEFORE INSERT derive trigger.
-- ЭНД ХИЙГДЭЭГҮЙ (trg горим): orphan мөртэй хүснэгтэд CHECK нэмэхгүй (NOT VALID CHECK нь orphan мөрийн UPDATE-ийг эвддэг); validate/NOT NULL -> _pending_owner/.
-- ЖИЧ: CHECK-тэй хүснэгтэд expand -> backfill хоорондох цонх богино байх ёстой (backfill хийгдээгүй мөрийн UPDATE CHECK-д унана). Дараагийн backfill файлыг даруй ажиллуул.

ALTER TABLE attendance.sub_shift ADD COLUMN IF NOT EXISTS org_id uuid;

-- Хуучин апп (GORM/Lambda form) org_id бичдэггүй. BEFORE INSERT trigger эцэг хүснэгтээс org_id-г дүүргэнэ. Эцэг олдохгүй бол NULL хэвээр:
--   chk горим: CHECK унана (orphan insert татгалзана); trg горим: NULL-ээр орно (блоклохгүй).
-- public.sub_employee_*: employee_id = public.users.id (organization.employee.id БИШ) -> users.org_id (home org) ашиглана.
-- Апп org_id бичдэг болсны дараа trigger-ийг хасаж болно.
CREATE OR REPLACE FUNCTION attendance.p4_sub_shift_org_id_derive() RETURNS trigger LANGUAGE plpgsql AS $fn$
BEGIN
  IF NEW.org_id IS NULL THEN
    SELECT p.org_id INTO NEW.org_id FROM attendance.shift p WHERE p.id = NEW.shift_id;
  END IF;
  RETURN NEW;
END $fn$;
DROP TRIGGER IF EXISTS p4_sub_shift_org_id_derive ON attendance.sub_shift;
CREATE TRIGGER p4_sub_shift_org_id_derive BEFORE INSERT ON attendance.sub_shift FOR EACH ROW EXECUTE FUNCTION attendance.p4_sub_shift_org_id_derive();
