-- khan:no-transaction
-- DRAFT (P4) - APPLY ХИЙГЭЭГҮЙ. Prod-д apply хийхээс өмнө owner/CTO батлана; өмнө нь docs/p4/prod_counts.sql-ээр хэмжээг шалга.
-- khan migrate: forward-only, runner `SET LOCAL lock_timeout=5s`, statement_timeout=15m. Дугаарыг main-ийн дараагийн сул хувилбар руу ДАХИН ДУГААРЛА.
-- Group 1: organization-hr - EXPAND: nullable org_id + NOT VALID CHECK (богино lock).
-- Group 4: BACKFILL.
-- individual: party.party-аас НЭГ org-той бол л тавина. Олон org-д холбогдсон (count(DISTINCT org_id) > 1) болон party-гүй individual NULL үлдэнэ
-- (HUMAN CONFIRM: owner шийднэ; docs/p4/verify_p4.sql #4). Ямар ч тохиолдолд NOT NULL/VALIDATE-д орохгүй (0410 pending).
-- relationship: ctid блок-мужаар from_party_id -> party.party.org_id.
DO $$
DECLARE b bigint; maxb bigint;
BEGIN
  b := 0; maxb := pg_relation_size('party.individual'::regclass) / current_setting('block_size')::bigint + 1;
  WHILE b <= maxb LOOP
    UPDATE party.individual i SET org_id = s.org_id
      FROM (SELECT individual_id, min(org_id::text)::uuid AS org_id
              FROM party.party WHERE individual_id IS NOT NULL AND org_id IS NOT NULL
             GROUP BY individual_id HAVING count(DISTINCT org_id) = 1) s
     WHERE s.individual_id = i.id AND i.org_id IS NULL
       AND i.ctid >= format('(%s,0)', b)::tid AND i.ctid < format('(%s,0)', b + 200)::tid;
    b := b + 200;
    COMMIT;
  END LOOP;
  b := 0; maxb := pg_relation_size('party.relationship'::regclass) / current_setting('block_size')::bigint + 1;
  WHILE b <= maxb LOOP
    UPDATE party.relationship t SET org_id = p.org_id FROM party.party p
     WHERE p.id = t.from_party_id AND t.org_id IS NULL AND p.org_id IS NOT NULL
       AND t.ctid >= format('(%s,0)', b)::tid AND t.ctid < format('(%s,0)', b + 200)::tid;
    b := b + 200;
    COMMIT;
  END LOOP;
END $$
