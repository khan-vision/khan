-- khan:no-transaction
-- DRAFT (P4) - APPLY ХИЙГЭЭГҮЙ. Prod-д apply хийхээс өмнө owner/CTO батлана; өмнө нь docs/p4/prod_counts.sql-ээр хэмжээг шалга.
-- khan migrate: forward-only, runner `SET LOCAL lock_timeout=5s`, statement_timeout=15m. Дугаарыг main-ийн дараагийн сул хувилбар руу ДАХИН ДУГААРЛА.
-- Group 4: BACKFILL.
-- HUMAN CONFIRM: нэг individual олон org-ийн party-д холбогдсон бол (count(DISTINCT org_id) > 1) доорх UPDATE тэднийг ТЭГ орхино
-- (NULL үлдэнэ -> VALIDATE унана). Шийдэл: org бүрт individual мөрийг хуулж party.individual_id-г шинэчлэх (owner-ийн шийдвэр).
DO $$
DECLARE n bigint;
BEGIN
  LOOP
    UPDATE party.individual i SET org_id = s.org_id
      FROM (SELECT individual_id, (array_agg(DISTINCT org_id))[1] AS org_id
              FROM party.party WHERE individual_id IS NOT NULL AND org_id IS NOT NULL
             GROUP BY individual_id HAVING count(DISTINCT org_id) = 1) s
     WHERE s.individual_id = i.id AND i.org_id IS NULL;
    GET DIAGNOSTICS n = ROW_COUNT;
    EXIT WHEN n = 0;
    COMMIT;
  END LOOP;
  LOOP
    UPDATE party.relationship r SET org_id = p.org_id
      FROM party.party p
     WHERE p.id = r.from_party_id AND r.org_id IS NULL AND p.org_id IS NOT NULL;
    GET DIAGNOSTICS n = ROW_COUNT;
    EXIT WHEN n = 0;
    COMMIT;
  END LOOP;
  -- HUMAN CONFIRM: relationship.to_party_id нь ижил org-д байх ёстой. Шалгах: SELECT count(*) FROM party.relationship r JOIN party.party t ON t.id=r.to_party_id WHERE t.org_id <> r.org_id;
END $$
