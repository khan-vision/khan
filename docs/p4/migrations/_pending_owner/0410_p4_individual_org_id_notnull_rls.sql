-- PENDING OWNER (party.individual). W0-д party.individual ӨӨРЧЛӨГДӨӨГҮЙ (org_id багана ч байхгүй).
-- БАТАЛСАН СЕМАНТИК: party.party.org_id нь зөвхөн type='organization' party-д (prod: 596/596; individual 4008-д 0) - individual-ийн tenant party.party-аас БИШ,
-- relationship-ээр (from_party=tenant org party -> to_party=individual party) тодорно. Prod: 4008 individual party -> 3927 нэг org, 20 олон org, 61 relationship-гүй.
-- Олон org-д хуваалцсан individual (20) = нэг мөр, олон tenant -> org_id NOT NULL боломжгүй (хуулбарлах эсвэл join хүснэгт). saveWithParty individual-г party-аас өмнө бичдэг.
-- Урьдчилсан нөхцөл: (1) owner загвар (хуулбар/join); (2) апп individual INSERT-д org_id бичдэг болсон; (3) org_id IS NULL = 0.
-- Дараалал: багана -> backfill (доорх) -> индекс -> CHECK NOT VALID -> VALIDATE -> SET NOT NULL -> RLS.
ALTER TABLE party.individual ADD COLUMN IF NOT EXISTS org_id uuid;
UPDATE party.individual i SET org_id = s.org_id
  FROM (SELECT p.individual_id, min(f.org_id::text)::uuid AS org_id
          FROM party.party p
          JOIN party.relationship r ON r.to_party_id = p.id
          JOIN party.party f ON f.id = r.from_party_id
         WHERE p.individual_id IS NOT NULL AND f.org_id IS NOT NULL
         GROUP BY p.individual_id HAVING count(DISTINCT f.org_id) = 1) s
 WHERE s.individual_id = i.id AND i.org_id IS NULL;
-- (0406 индекс: _pending_owner/0406_p4_individual_org_idx.sql)
ALTER TABLE party.individual ADD CONSTRAINT individual_org_id_nn CHECK (org_id IS NOT NULL) NOT VALID;
ALTER TABLE party.individual VALIDATE CONSTRAINT individual_org_id_nn;
ALTER TABLE party.individual ALTER COLUMN org_id SET NOT NULL;
ALTER TABLE party.individual DROP CONSTRAINT individual_org_id_nn;
