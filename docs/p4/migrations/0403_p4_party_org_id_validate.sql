-- DRAFT (P4) - APPLY ХИЙГЭЭГҮЙ. Prod-д apply хийхээс өмнө owner/CTO батлана; өмнө нь docs/p4/prod_counts.sql-ээр хэмжээг шалга.
-- khan migrate: forward-only, runner `SET LOCAL lock_timeout=5s`, statement_timeout=15m. Дугаарыг main-ийн дараагийн сул хувилбар руу ДАХИН ДУГААРЛА.
-- Group 4: VALIDATE (relationship). cross-org (to_party өөр org/NULL) = ХЭВИЙН (харилцагч), анхааруулга байхгүй.
DO $$
DECLARE n bigint; msg text := '';
BEGIN
  SELECT count(*) INTO n FROM (SELECT id FROM party.relationship GROUP BY id HAVING count(*) > 1 OR id IS NULL) d; IF n > 0 THEN msg := msg || format(' party.relationship=%s', n); END IF;
  IF msg <> '' THEN RAISE EXCEPTION 'P4 gate: давхардсан/NULL id:% (docs/p4/verify_p4.sql #5)', msg; END IF;
END $$;
DO $$
DECLARE msg text := ''; n bigint;
BEGIN
  SELECT count(*) INTO n FROM party.relationship WHERE org_id IS NULL; IF n > 0 THEN msg := msg || format(' party.relationship=%s', n); END IF;
  IF msg <> '' THEN RAISE EXCEPTION 'P4 gate: org_id NULL мөр үлдсэн (orphan эцэг/NULL FK):%. УСТГАХГҮЙ - owner шийдээд (docs/p4/W0_RUNBOOK.md) дахин ажиллуул.', msg; END IF;
END $$;

ALTER TABLE party.relationship VALIDATE CONSTRAINT relationship_org_id_nn;
