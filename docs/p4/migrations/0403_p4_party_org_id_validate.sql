-- DRAFT (P4) - APPLY ХИЙГЭЭГҮЙ. Prod-д apply хийхээс өмнө owner/CTO батлана; өмнө нь docs/p4/prod_counts.sql-ээр хэмжээг шалга.
-- khan migrate: forward-only, runner `SET LOCAL lock_timeout=5s`, statement_timeout=15m. Дугаарыг main-ийн дараагийн сул хувилбар руу ДАХИН ДУГААРЛА.
-- Group 1: organization-hr - EXPAND: nullable org_id + NOT VALID CHECK (богино lock).
-- GATE 2: PK-гүй хүснэгтийн id давхардал/NULL -> дараагийн UNIQUE INDEX CONCURRENTLY унаж INVALID index үлдээхээс ӨМНӨ барина.
DO $$
DECLARE n bigint; msg text := '';
BEGIN
  SELECT count(*) INTO n FROM (SELECT id FROM party.relationship GROUP BY id HAVING count(*) > 1 OR id IS NULL) d; IF n > 0 THEN msg := msg || format(' party.relationship=%s', n); END IF;
  IF msg <> '' THEN RAISE EXCEPTION 'P4 gate: давхардсан/NULL id:% (docs/p4/verify_p4.sql #5)', msg; END IF;
END $$;
-- Group 4: VALIDATE (relationship). individual ЭНД ОРОХГҮЙ (0410 pending).
DO $$
DECLARE n bigint; x bigint;
BEGIN
  SELECT count(*) INTO n FROM party.relationship WHERE org_id IS NULL;
  IF n > 0 THEN RAISE EXCEPTION 'P4 gate: party.relationship org_id NULL=% (from_party_id NULL/orphan). docs/p4/verify_p4.sql #3', n; END IF;
  SELECT count(*) INTO x FROM party.relationship r JOIN party.party t ON t.id = r.to_party_id WHERE t.org_id IS DISTINCT FROM r.org_id;
  IF x > 0 THEN RAISE WARNING 'P4: % cross-org relationship (to_party өөр org-д). docs/p4/verify_p4.sql #3b', x; END IF;
END $$;
ALTER TABLE party.relationship VALIDATE CONSTRAINT relationship_org_id_nn;
