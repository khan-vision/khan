-- khan:no-transaction
-- DRAFT (P4) - APPLY ХИЙГЭЭГҮЙ. Prod-д apply хийхээс өмнө owner/CTO батлана; өмнө нь docs/p4/prod_counts.sql-ээр хэмжээг шалга.
-- khan migrate: forward-only, runner `SET LOCAL lock_timeout=5s`, statement_timeout=15m. Дугаарыг main-ийн дараагийн сул хувилбар руу ДАХИН ДУГААРЛА.
-- Group 3: attendance: BACKFILL. ctid блок-мужаар (TID range scan, индексгүй ч шугаман; batch=200 блок, batch бүр COMMIT).
-- Эцэггүй (orphan) мөр NULL үлдэнэ, УСТГАХГҮЙ. chk хүснэгтэд orphan байвал дараагийн VALIDATE gate тоог гаргаж унана; trg хүснэгтийн orphan -> _pending_owner.

DO $$
DECLARE b bigint; maxb bigint;
BEGIN
  -- attendance.sub_shift.shift_id -> attendance.shift.org_id
  b := 0; maxb := pg_relation_size('attendance.sub_shift'::regclass) / current_setting('block_size')::bigint + 1;
  WHILE b <= maxb LOOP
    UPDATE attendance.sub_shift t SET org_id = p.org_id FROM attendance.shift p
     WHERE p.id = t.shift_id AND t.org_id IS NULL AND p.org_id IS NOT NULL
       AND t.ctid >= format('(%s,0)', b)::tid AND t.ctid < format('(%s,0)', b + 200)::tid;
    b := b + 200;
    COMMIT;
  END LOOP;
END $$