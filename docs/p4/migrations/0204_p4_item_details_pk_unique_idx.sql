-- khan:no-transaction
-- DRAFT (P4) - APPLY ХИЙГЭЭГҮЙ. Prod-д apply хийхээс өмнө owner/CTO батлана; өмнө нь docs/p4/prod_counts.sql-ээр хэмжээг шалга.
-- khan migrate: forward-only, runner `SET LOCAL lock_timeout=5s`, statement_timeout=15m. Дугаарыг main-ийн дараагийн сул хувилбар руу ДАХИН ДУГААРЛА.
-- PK-гүй хүснэгт: эхлээд давхардсан id байгаа эсэхийг шалга: SELECT id,count(*) FROM item.item_details GROUP BY id HAVING count(*)>1 OR id IS NULL;
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS item_details_id_uidx ON item.item_details (id)
