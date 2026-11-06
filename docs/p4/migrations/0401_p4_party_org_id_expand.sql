-- DRAFT (P4) - APPLY ХИЙГЭЭГҮЙ. Prod-д apply хийхээс өмнө owner/CTO батлана; өмнө нь docs/p4/prod_counts.sql-ээр хэмжээг шалга.
-- khan migrate: forward-only, runner `SET LOCAL lock_timeout=5s`, statement_timeout=15m. Дугаарыг main-ийн дараагийн сул хувилбар руу ДАХИН ДУГААРЛА.
-- Group 4: party - EXPAND. individual нь party.party-ээр org-той холбогдоно.
ALTER TABLE party.individual ADD COLUMN IF NOT EXISTS org_id uuid;
ALTER TABLE party.relationship ADD COLUMN IF NOT EXISTS org_id uuid;
ALTER TABLE party.individual ADD CONSTRAINT individual_org_id_nn CHECK (org_id IS NOT NULL) NOT VALID;
ALTER TABLE party.relationship ADD CONSTRAINT relationship_org_id_nn CHECK (org_id IS NOT NULL) NOT VALID;
