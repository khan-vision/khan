-- DRAFT (P4) - APPLY ХИЙГЭЭГҮЙ. Prod-д apply хийхээс өмнө owner/CTO батлана; өмнө нь docs/p4/prod_counts.sql-ээр хэмжээг шалга.
-- khan migrate: forward-only, runner `SET LOCAL lock_timeout=5s`, statement_timeout=15m. Дугаарыг main-ийн дараагийн сул хувилбар руу ДАХИН ДУГААРЛА.
-- GATE: CREATE UNIQUE INDEX CONCURRENTLY унасан бол INVALID index үлддэг (IF NOT EXISTS дахин ажиллуулахад алгасч '✓' болдог). Ийм бол гараар:
--   DROP INDEX CONCURRENTLY <index>;  CREATE UNIQUE INDEX CONCURRENTLY <index> ON <table> (id);  (psql, tx-гүй) - дараа нь migrate up дахин.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_index i JOIN pg_class c ON c.oid=i.indexrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='party' AND c.relname='relationship_id_uidx' AND NOT i.indisvalid) THEN RAISE EXCEPTION 'P4 gate: INVALID index party.relationship_id_uidx: DROP INDEX CONCURRENTLY party.relationship_id_uidx; CREATE UNIQUE INDEX CONCURRENTLY ... ON <tbl>(id); (psql, tx-гүй) дараа нь migrate up'; END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='party' AND c.relname='relationship_id_uidx') THEN RAISE EXCEPTION 'P4 gate: index party.relationship_id_uidx алга'; END IF;
END $$;
ALTER TABLE party.relationship ALTER COLUMN org_id SET NOT NULL;
ALTER TABLE party.relationship DROP CONSTRAINT relationship_org_id_nn;

ALTER TABLE party.relationship ADD CONSTRAINT relationship_pkey PRIMARY KEY USING INDEX relationship_id_uidx;
