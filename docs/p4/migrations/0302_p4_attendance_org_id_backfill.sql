-- khan:no-transaction
-- DRAFT (P4) - APPLY ХИЙГЭЭГҮЙ. Prod-д apply хийхээс өмнө owner/CTO батлана; өмнө нь docs/p4/prod_counts.sql-ээр хэмжээг шалга.
-- khan migrate: forward-only, runner `SET LOCAL lock_timeout=5s`, statement_timeout=15m. Дугаарыг main-ийн дараагийн сул хувилбар руу ДАХИН ДУГААРЛА.
-- Group 3: BACKFILL (batch 5000, batch бүр COMMIT). Нэг statement (DO) тул no-transaction.
-- Эцэг хүснэгтгүй (orphan) мөр NULL хэвээр үлдэж, дараагийн VALIDATE унана -> prod_counts.sql #orphan-оор өмнө нь шалга/цэвэрл (human confirm).

DO $$
DECLARE n bigint;
BEGIN
  -- attendance.sub_shift.shift_id -> attendance.shift.org_id
  LOOP
    UPDATE attendance.sub_shift t SET org_id = p.org_id
      FROM attendance.shift p
     WHERE p.id = t.shift_id
       AND t.ctid IN (SELECT t2.ctid FROM attendance.sub_shift t2 JOIN attendance.shift p2 ON p2.id = t2.shift_id
                       WHERE t2.org_id IS NULL AND p2.org_id IS NOT NULL LIMIT 5000);
    GET DIAGNOSTICS n = ROW_COUNT;
    EXIT WHEN n = 0;
    COMMIT;
  END LOOP;
  COMMIT;
END $$
