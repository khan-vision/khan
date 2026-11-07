-- PENDING OWNER (item.item_details). Prod: 3/15 мөр item_id нь устсан item-ийг заадаг (dangling, FK байхгүй) -> org_id NULL.
-- Урьдчилсан нөхцөл: owner 3 мөрийг архивлах/устгах эсвэл org-ийг гараар тогтоох; org_id IS NULL = 0. (PK(id) нь 0205-д аль хэдийн орсон.)
DO $$
DECLARE n bigint;
BEGIN
  SELECT count(*) INTO n FROM item.item_details WHERE org_id IS NULL;
  IF n > 0 THEN RAISE EXCEPTION 'P4 gate: item.item_details org_id NULL=% (orphan, owner шийдээгүй)', n; END IF;
END $$;
ALTER TABLE item.item_details ADD CONSTRAINT item_details_org_id_nn CHECK (org_id IS NOT NULL) NOT VALID;
ALTER TABLE item.item_details VALIDATE CONSTRAINT item_details_org_id_nn;
ALTER TABLE item.item_details ALTER COLUMN org_id SET NOT NULL;
ALTER TABLE item.item_details DROP CONSTRAINT item_details_org_id_nn;
