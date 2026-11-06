package db_test

import (
	"errors"
	"testing"

	"github.com/khan-vision/khan/db"
	"github.com/khan-vision/khan/testkit"
	"gorm.io/gorm"
)

func TestUpdateVersionedDB(t *testing.T) {
	tx := testkit.TxDB(t)
	if err := tx.Exec(`CREATE TEMP TABLE ver_doc (
		id int primary key, org_id uuid not null, name text, version bigint not null default 1,
		updated_at timestamptz not null default now() - interval '1 day')`).Error; err != nil {
		t.Fatal(err)
	}
	const org = "11111111-1111-1111-1111-111111111111"
	const other = "22222222-2222-2222-2222-222222222222"
	if err := tx.Exec(`INSERT INTO ver_doc (id, org_id, name) VALUES (1, ?, 'a')`, org).Error; err != nil {
		t.Fatal(err)
	}
	where := map[string]any{"org_id": org, "id": 1}

	v, err := db.UpdateVersioned(tx, "ver_doc", where, 1, map[string]any{"name": "b"})
	if err != nil || v != 2 {
		t.Fatalf("v=%d err=%v", v, err)
	}
	var row struct {
		Name    string
		Version int64
		Fresh   bool
	}
	if err := tx.Raw(`SELECT name, version, updated_at > now() - interval '1 minute' AS fresh FROM ver_doc WHERE id = 1`).Scan(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.Name != "b" || row.Version != 2 || !row.Fresh {
		t.Fatalf("row = %+v", row)
	}

	// Хуучин version → ErrConflict, мөр өөрчлөгдөхгүй.
	if _, err := db.UpdateVersioned(tx, "ver_doc", where, 1, map[string]any{"name": "c"}); !errors.Is(err, db.ErrConflict) {
		t.Fatalf("conflict хүлээсэн: %v", err)
	}
	// Өөр org-ийн мөр = олдохгүй (tenant тусгаарлалт).
	if _, err := db.UpdateVersioned(tx, "ver_doc", map[string]any{"org_id": other, "id": 1}, 2, map[string]any{"name": "x"}); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("not found хүлээсэн: %v", err)
	}
	// Байхгүй id.
	if _, err := db.UpdateVersioned(tx, "ver_doc", map[string]any{"org_id": org, "id": 99}, 1, map[string]any{"name": "x"}); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("not found хүлээсэн: %v", err)
	}
	// Дараалсан амжилттай update.
	if v, err := db.UpdateVersioned(tx, "ver_doc", where, 2, map[string]any{"name": "c"}); err != nil || v != 3 {
		t.Fatalf("v=%d err=%v", v, err)
	}
}
