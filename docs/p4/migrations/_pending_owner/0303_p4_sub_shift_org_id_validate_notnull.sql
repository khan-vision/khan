-- PENDING OWNER (attendance.sub_shift). Prod: 5/1286 мөр shift_id нь устсан shift-ийг заадаг (dangling, FK байхгүй) -> org_id NULL үлдэнэ.
-- Урьдчилсан нөхцөл: owner эдгээр 5 мөрийг (a) архивлах/устгах (b) аль org-д хамаарахыг гараар тогтоох. Тэгээд org_id IS NULL = 0.
-- Дараа нь: ЭНЭ файл (NOT VALID CHECK -> VALIDATE -> SET NOT NULL -> CHECK drop). Дугаарыг дахин ол.
DO $$
DECLARE n bigint;
BEGIN
  SELECT count(*) INTO n FROM attendance.sub_shift WHERE org_id IS NULL;
  IF n > 0 THEN RAISE EXCEPTION 'P4 gate: attendance.sub_shift org_id NULL=% (orphan, owner шийдээгүй)', n; END IF;
END $$;
ALTER TABLE attendance.sub_shift ADD CONSTRAINT sub_shift_org_id_nn CHECK (org_id IS NOT NULL) NOT VALID;
ALTER TABLE attendance.sub_shift VALIDATE CONSTRAINT sub_shift_org_id_nn;
ALTER TABLE attendance.sub_shift ALTER COLUMN org_id SET NOT NULL;
ALTER TABLE attendance.sub_shift DROP CONSTRAINT sub_shift_org_id_nn;
