-- DRAFT (P4) - APPLY ХИЙГЭЭГҮЙ. Prod-д apply хийхээс өмнө owner/CTO батлана; өмнө нь docs/p4/prod_counts.sql-ээр хэмжээг шалга.
-- khan migrate: forward-only, runner `SET LOCAL lock_timeout=5s`, statement_timeout=15m. Дугаарыг main-ийн дараагийн сул хувилбар руу ДАХИН ДУГААРЛА.
-- Group 4: party (ЗӨВХӨН party.relationship) - EXPAND: nullable org_id (+ NOT VALID CHECK зөвхөн orphan-гүй хүснэгтэд) + BEFORE INSERT derive trigger.
-- ЭНД ХИЙГДЭЭГҮЙ (trg горим): orphan мөртэй хүснэгтэд CHECK нэмэхгүй (NOT VALID CHECK нь orphan мөрийн UPDATE-ийг эвддэг); validate/NOT NULL -> _pending_owner/.
-- ЖИЧ: CHECK-тэй хүснэгтэд expand -> backfill хоорондох цонх богино байх ёстой (backfill хийгдээгүй мөрийн UPDATE CHECK-д унана). Дараагийн backfill файлыг даруй ажиллуул.

ALTER TABLE party.relationship ADD COLUMN IF NOT EXISTS org_id uuid;
ALTER TABLE party.relationship ADD CONSTRAINT relationship_org_id_nn CHECK (org_id IS NOT NULL) NOT VALID;

-- Хуучин апп (GORM/Lambda form) org_id бичдэггүй. BEFORE INSERT trigger эцэг хүснэгтээс org_id-г дүүргэнэ. Эцэг олдохгүй бол NULL хэвээр:
--   chk горим: CHECK унана (orphan insert татгалзана); trg горим: NULL-ээр орно (блоклохгүй).
-- public.sub_employee_*: employee_id = public.users.id (organization.employee.id БИШ) -> users.org_id (home org) ашиглана.
-- Апп org_id бичдэг болсны дараа trigger-ийг хасаж болно.
CREATE OR REPLACE FUNCTION party.p4_relationship_org_id_derive() RETURNS trigger LANGUAGE plpgsql AS $fn$
BEGIN
  IF NEW.org_id IS NULL THEN
    SELECT p.org_id INTO NEW.org_id FROM party.party p WHERE p.id = NEW.from_party_id;
  END IF;
  RETURN NEW;
END $fn$;
DROP TRIGGER IF EXISTS p4_relationship_org_id_derive ON party.relationship;
CREATE TRIGGER p4_relationship_org_id_derive BEFORE INSERT ON party.relationship FOR EACH ROW EXECUTE FUNCTION party.p4_relationship_org_id_derive();
-- party.individual W0-д ОРОХГҮЙ: party.party.org_id нь зөвхөн type='organization' party-д бөглөгддөг (individual party.org_id = NULL),
-- тиймээс individual-ийн tenant нь relationship-ээр л тодорно (олон org-д хуваалцах боломжтой). Бүх зүйл -> _pending_owner/0410.
-- relationship.org_id = from_party.org_id ЗӨВ: from_party_id = нэвтэрсэн хэрэглэгчийн party_id (tenant-ын өөрийн org party), to_party = харилцагч (өөр org эсвэл individual).
