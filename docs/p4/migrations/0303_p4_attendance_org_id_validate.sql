-- DRAFT (P4) - APPLY ХИЙГЭЭГҮЙ. Prod-д apply хийхээс өмнө owner/CTO батлана; өмнө нь docs/p4/prod_counts.sql-ээр хэмжээг шалга.
-- khan migrate: forward-only, runner `SET LOCAL lock_timeout=5s`, statement_timeout=15m. Дугаарыг main-ийн дараагийн сул хувилбар руу ДАХИН ДУГААРЛА.
-- Group 3: VALIDATE (SHARE UPDATE EXCLUSIVE; бичилтийг хаахгүй).
-- GATE: backfill-ийн дараа org_id NULL үлдсэн мөр байвал ТОГТООСОН мессежээр унана (VALIDATE-ийн ойлгомжгүй алдаа биш). Жагсаалт: docs/p4/verify_p4.sql
DO $$
DECLARE msg text := ''; n bigint;
BEGIN
  SELECT count(*) INTO n FROM attendance.sub_shift WHERE org_id IS NULL; IF n > 0 THEN msg := msg || format(' attendance.sub_shift=%s', n); END IF;
  IF msg <> '' THEN RAISE EXCEPTION 'P4 gate: org_id NULL мөр үлдсэн (orphan эцэг/NULL FK):%. Цэвэрлэж/шийдээд дахин ажиллуул.', msg; END IF;
END $$;

ALTER TABLE attendance.sub_shift VALIDATE CONSTRAINT sub_shift_org_id_nn;
