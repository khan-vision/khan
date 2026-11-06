package tenant

import (
	"errors"
	"testing"

	"gorm.io/gorm"
)

func TestSharedCond(t *testing.T) {
	if got := SharedCond(""); got != "(org_id = ? OR org_id IS NULL)" {
		t.Fatalf("SharedCond = %q", got)
	}
	if got := SharedCond("t."); got != "(t.org_id = ? OR t.org_id IS NULL)" {
		t.Fatalf("SharedCond(t.) = %q", got)
	}
}

func TestGuardSharedCond(t *testing.T) {
	db := guardDB(t, GuardStrict, nil)
	var rows []guardInvoice
	ok := map[string]*gorm.DB{
		"shared":        db.Where(SharedCond(""), "org-A").Find(&rows),
		"shared alias":  db.Table("finance.invoice i").Where(SharedCond("i."), "org-A").Find(&rows),
		"shared + more": db.Where("code = ?", "x").Where(SharedCond(""), "org-A").Find(&rows),
		"reverse order": db.Where("(org_id IS NULL OR org_id = ?)", "org-A").Find(&rows),
		"upper/spaces":  db.Where("( ORG_ID = $1  or  ORG_ID is null )", "org-A").Find(&rows),
		"system row":    SystemRow(db).Where("org_id IS NULL AND id = ?", "1").Find(&rows),
		"system update": SystemRow(db.Model(&guardInvoice{})).Where("org_id IS NULL AND id = ?", "1").Update("code", "y"),
		"system create": SystemRow(db.Table("finance.invoice")).Create(map[string]any{"id": "1", "org_id": nil}),
	}
	for name, res := range ok {
		if res.Error != nil {
			t.Errorf("%s: %v", name, res.Error)
		}
	}
	bad := map[string]*gorm.DB{
		"empty org":       db.Where(SharedCond(""), "").Find(&rows),
		"zero org":        db.Where(SharedCond(""), ZeroUUID).Find(&rows),
		"other or":        db.Where("(org_id = ? OR 1=1)", "org-A").Find(&rows),
		"trailing or":     db.Where("(org_id = ? OR org_id IS NULL OR 1=1)", "org-A").Find(&rows),
		"mixed prefix":    db.Where("(a.org_id = ? OR b.org_id IS NULL)", "org-A").Find(&rows),
		"is null only":    db.Where("org_id IS NULL").Find(&rows),
		"not anchored":    db.Where("1=1 AND (org_id = ? OR org_id IS NULL)", "org-A").Find(&rows),
		"plain system":    db.Where("org_id IS NULL").Delete(&guardInvoice{}),
		"create null org": db.Table("finance.invoice").Create(map[string]any{"id": "1", "org_id": nil}),
	}
	for name, res := range bad {
		if !errors.Is(res.Error, ErrUnscoped) {
			t.Errorf("%s: ErrUnscoped хүлээсэн, авсан %v", name, res.Error)
		}
	}
}
