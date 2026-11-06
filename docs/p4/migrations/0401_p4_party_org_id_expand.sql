-- DRAFT (P4) - APPLY ХИЙГЭЭГҮЙ. Prod-д apply хийхээс өмнө owner/CTO батлана; өмнө нь docs/p4/prod_counts.sql-ээр хэмжээг шалга.
-- khan migrate: forward-only, runner `SET LOCAL lock_timeout=5s`, statement_timeout=15m. Дугаарыг main-ийн дараагийн сул хувилбар руу ДАХИН ДУГААРЛА.
-- Group 4: party - EXPAND. individual нь party.party-ээр org-той холбогдоно.
ALTER TABLE party.individual ADD COLUMN IF NOT EXISTS org_id uuid;
ALTER TABLE party.relationship ADD COLUMN IF NOT EXISTS org_id uuid;
-- party.individual: ЗӨВХӨН багана. CHECK/NOT NULL/RLS нэмэхгүй - апп (saveWithParty) individual-г party-аас ӨМНӨ, org_id-гүй бичдэг тул
-- NOT VALID CHECK нь INSERT-ийг шууд эвдэнэ. NOT NULL/RLS нь _pending_owner/0410 (апп org_id бичдэг болсны дараа).
ALTER TABLE party.relationship ADD CONSTRAINT relationship_org_id_nn CHECK (org_id IS NOT NULL) NOT VALID;

-- Хуучин апп org_id бичдэггүй тул NOT VALID CHECK нь INSERT-ийг шууд эвдэнэ. BEFORE INSERT trigger эцэг хүснэгтээс org_id-г автоматаар дүүргэнэ
-- (эцэг олдохгүй бол NULL хэвээр -> CHECK унана = orphan insert татгалзана). Апп org_id бичдэг болсны дараа trigger-ийг хасаж болно.
CREATE OR REPLACE FUNCTION party.p4_relationship_org_id_derive() RETURNS trigger LANGUAGE plpgsql AS $fn$
BEGIN
  IF NEW.org_id IS NULL THEN
    SELECT p.org_id INTO NEW.org_id FROM party.party p WHERE p.id = NEW.from_party_id;
  END IF;
  RETURN NEW;
END $fn$;
DROP TRIGGER IF EXISTS p4_relationship_org_id_derive ON party.relationship;
CREATE TRIGGER p4_relationship_org_id_derive BEFORE INSERT ON party.relationship FOR EACH ROW EXECUTE FUNCTION party.p4_relationship_org_id_derive();
