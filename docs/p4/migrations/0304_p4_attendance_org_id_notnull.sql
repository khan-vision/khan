-- DRAFT (P4) - APPLY ХИЙГЭЭГҮЙ. Prod-д apply хийхээс өмнө owner/CTO батлана; өмнө нь docs/p4/prod_counts.sql-ээр хэмжээг шалга.
-- khan migrate: forward-only, runner `SET LOCAL lock_timeout=5s`, statement_timeout=15m. Дугаарыг main-ийн дараагийн сул хувилбар руу ДАХИН ДУГААРЛА.
-- Group 3: SET NOT NULL (valid CHECK байгаа тул full scan-гүй, PG12+), дараа нь CHECK-ийг хас.

ALTER TABLE attendance.sub_shift ALTER COLUMN org_id SET NOT NULL;
ALTER TABLE attendance.sub_shift DROP CONSTRAINT sub_shift_org_id_nn;
