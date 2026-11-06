-- DRAFT (P4) - APPLY ХИЙГЭЭГҮЙ. Prod-д apply хийхээс өмнө owner/CTO батлана; өмнө нь docs/p4/prod_counts.sql-ээр хэмжээг шалга.
-- khan migrate: forward-only, runner `SET LOCAL lock_timeout=5s`, statement_timeout=15m. Дугаарыг main-ийн дараагийн сул хувилбар руу ДАХИН ДУГААРЛА.
-- Group 3: attendance - EXPAND: nullable org_id + NOT VALID CHECK (богино lock).

ALTER TABLE attendance.sub_shift ADD COLUMN IF NOT EXISTS org_id uuid;
ALTER TABLE attendance.sub_shift ADD CONSTRAINT sub_shift_org_id_nn CHECK (org_id IS NOT NULL) NOT VALID;
