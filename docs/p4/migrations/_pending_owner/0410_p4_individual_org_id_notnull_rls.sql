-- DRAFT (P4) - APPLY ХИЙГЭЭГҮЙ. Prod-д apply хийхээс өмнө owner/CTO батлана; өмнө нь docs/p4/prod_counts.sql-ээр хэмжээг шалга.
-- khan migrate: forward-only, runner `SET LOCAL lock_timeout=5s`, statement_timeout=15m. Дугаарыг main-ийн дараагийн сул хувилбар руу ДАХИН ДУГААРЛА.
-- Group 1: organization-hr - EXPAND: nullable org_id + NOT VALID CHECK (богино lock).
-- PENDING OWNER (party.individual). Урьдчилсан нөхцөл:
--  1) апп (organization/registry saveWithParty) individual INSERT-д org_id бичдэг болсон (эсвэл derive trigger нэмсэн);
--  2) олон org-д хуваалцсан individual (count(DISTINCT org_id)>1) болон party-гүй individual-ийн шийдвэр (verify_p4.sql #4) - хуулбарла/устга;
--  3) org_id IS NULL = 0.
-- Тэгээд: CHECK NOT VALID -> VALIDATE -> SET NOT NULL -> RLS (soft). RLS-ийг org_id бичигддэг болохоос өмнө асаавал tenant session-ий INSERT unaна.
ALTER TABLE party.individual ADD CONSTRAINT individual_org_id_nn CHECK (org_id IS NOT NULL) NOT VALID;
ALTER TABLE party.individual VALIDATE CONSTRAINT individual_org_id_nn;
ALTER TABLE party.individual ALTER COLUMN org_id SET NOT NULL;
ALTER TABLE party.individual DROP CONSTRAINT individual_org_id_nn;
