-- DRAFT (P4) - APPLY ХИЙГЭЭГҮЙ. Prod-д apply хийхээс өмнө owner/CTO батлана; өмнө нь docs/p4/prod_counts.sql-ээр хэмжээг шалга.
-- khan migrate: forward-only, runner `SET LOCAL lock_timeout=5s`, statement_timeout=15m. Дугаарыг main-ийн дараагийн сул хувилбар руу ДАХИН ДУГААРЛА.
-- PENDING OWNER: organization.organizations нь tenant root. org_id = id болгох generated багана.
-- STORED generated багана нэмэх нь хүснэгтийг дахин бичнэ (ACCESS EXCLUSIVE, 741 мөр test; prod-д хэмжээг шалга) - оройн цагт.
-- Хэрэв owner татгалзвал: organizations-ийг tenant-гүй kv-admin API-д үлдээнэ (ORG_ID_GAPS.md).
ALTER TABLE organization.organizations ADD COLUMN IF NOT EXISTS org_id uuid GENERATED ALWAYS AS (id) STORED;
