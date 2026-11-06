-- DRAFT (P4) - APPLY ХИЙГЭЭГҮЙ. Prod-д apply хийхээс өмнө owner/CTO батлана; өмнө нь docs/p4/prod_counts.sql-ээр хэмжээг шалга.
-- khan migrate: forward-only, runner `SET LOCAL lock_timeout=5s`, statement_timeout=15m. Дугаарыг main-ийн дараагийн сул хувилбар руу ДАХИН ДУГААРЛА.
ALTER TABLE party.individual ALTER COLUMN org_id SET NOT NULL;
ALTER TABLE party.individual DROP CONSTRAINT individual_org_id_nn;
ALTER TABLE party.relationship ALTER COLUMN org_id SET NOT NULL;
ALTER TABLE party.relationship DROP CONSTRAINT relationship_org_id_nn;
ALTER TABLE party.relationship ADD CONSTRAINT relationship_pkey PRIMARY KEY USING INDEX relationship_id_uidx;
