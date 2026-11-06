package tenant_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/khan-vision/khan/tenant"
	"github.com/khan-vision/khan/testkit"
	"gorm.io/gorm"
)

const (
	orgA = "11111111-1111-1111-1111-111111111111"
	orgB = "22222222-2222-2222-2222-222222222222"
)

// rlsFixture — транзакц дотор: хүснэгт + 2 org-ийн мөр + policy + энгийн role.
// Superuser-т RLS үйлчилдэггүй тул SET LOCAL ROLE-оор энгийн role болно.
// Бүх зүйл (CREATE ROLE ч) тест дуусахад rollback.
func rlsFixture(t *testing.T, mode tenant.PolicyMode) *gorm.DB {
	t.Helper()
	tx := testkit.TxDB(t)
	policy, err := tenant.PolicySQL("khan_rls.doc", tenant.PolicyOptions{Mode: mode})
	if err != nil {
		t.Fatal(err)
	}
	stmts := []string{
		`CREATE SCHEMA khan_rls`,
		`CREATE TABLE khan_rls.doc (id serial PRIMARY KEY, org_id uuid NOT NULL, title text)`,
		fmt.Sprintf(`INSERT INTO khan_rls.doc (org_id, title) VALUES ('%s','a1'),('%s','a2'),('%s','b1')`, orgA, orgA, orgB),
		policy,
		`CREATE ROLE khan_rls_app NOLOGIN NOSUPERUSER NOBYPASSRLS`,
		`GRANT USAGE ON SCHEMA khan_rls TO khan_rls_app`,
		`GRANT SELECT, INSERT, UPDATE, DELETE ON khan_rls.doc TO khan_rls_app`,
		`GRANT USAGE ON SEQUENCE khan_rls.doc_id_seq TO khan_rls_app`,
		`SET LOCAL ROLE khan_rls_app`,
	}
	for _, s := range stmts {
		if err := tx.Exec(s).Error; err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	return tx
}

func count(t *testing.T, tx *gorm.DB) int64 {
	t.Helper()
	var n int64
	if err := tx.Raw(`SELECT count(*) FROM khan_rls.doc`).Scan(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRLSSoft(t *testing.T) {
	tx := rlsFixture(t, tenant.PolicySoft)
	if n := count(t, tx); n != 3 {
		t.Fatalf("org тавиагүй soft горим: %d мөр, want 3 (хуучин код эвдрэхгүй)", n)
	}
	if err := tenant.WithOrg(tx, orgA); err != nil {
		t.Fatal(err)
	}
	// WHERE org_id мартсан ч зөвхөн org A.
	if n := count(t, tx); n != 2 {
		t.Fatalf("org A: %d мөр, want 2", n)
	}
	err := tx.Exec(`INSERT INTO khan_rls.doc (org_id, title) VALUES (?, 'x')`, orgB).Error
	if err == nil || !strings.Contains(err.Error(), "row-level security") {
		t.Fatalf("org A-аас org B-д бичих = %v, RLS алдаа хүлээсэн", err)
	}
}

func TestRLSStrict(t *testing.T) {
	tx := rlsFixture(t, tenant.PolicyStrict)
	if n := count(t, tx); n != 0 {
		t.Fatalf("strict, org тавиагүй: %d мөр, want 0", n)
	}
	if err := tenant.WithOrg(tx, orgB); err != nil {
		t.Fatal(err)
	}
	if n := count(t, tx); n != 1 {
		t.Fatalf("org B: %d мөр, want 1", n)
	}
	if res := tx.Exec(`UPDATE khan_rls.doc SET title = 'hacked'`); res.Error != nil || res.RowsAffected != 1 {
		t.Fatalf("WHERE-гүй UPDATE зөвхөн org B-ийн 1 мөрийг өөрчлөх ёстой: %v, %d", res.Error, res.RowsAffected)
	}
	if err := tenant.WithBypass(tx); err != nil {
		t.Fatal(err)
	}
	if n := count(t, tx); n != 3 {
		t.Fatalf("bypass: %d мөр, want 3", n)
	}
}

func TestRLSSystemRows(t *testing.T) {
	tx := testkit.TxDB(t)
	policy, err := tenant.PolicySQL("khan_rls.doc", tenant.PolicyOptions{Mode: tenant.PolicyStrict, SystemRows: true})
	if err != nil {
		t.Fatal(err)
	}
	stmts := []string{
		`CREATE SCHEMA khan_rls`,
		`CREATE TABLE khan_rls.doc (id serial PRIMARY KEY, org_id uuid, title text)`,
		fmt.Sprintf(`INSERT INTO khan_rls.doc (org_id, title) VALUES ('%s','a1'),('%s','b1'),(NULL,'sys')`, orgA, orgB),
		policy,
		`CREATE ROLE khan_rls_app NOLOGIN NOSUPERUSER NOBYPASSRLS`,
		`GRANT USAGE ON SCHEMA khan_rls TO khan_rls_app`,
		`GRANT SELECT, INSERT, UPDATE, DELETE ON khan_rls.doc TO khan_rls_app`,
		`GRANT USAGE ON SEQUENCE khan_rls.doc_id_seq TO khan_rls_app`,
		`SET LOCAL ROLE khan_rls_app`,
	}
	for _, s := range stmts {
		if err := tx.Exec(s).Error; err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	if err := tenant.WithOrg(tx, orgA); err != nil {
		t.Fatal(err)
	}
	// унших: өөрийн + системийн (B-ийнх биш)
	if n := count(t, tx); n != 2 {
		t.Fatalf("org A: %d мөр, want 2 (a1 + sys)", n)
	}
	// системийн мөрийг өөрчлөх/устгах/өөртөө авах боломжгүй (0 мөр нөлөөлнө)
	if res := tx.Exec(`UPDATE khan_rls.doc SET title = 'hacked' WHERE org_id IS NULL`); res.Error != nil || res.RowsAffected != 0 {
		t.Fatalf("системийн мөр UPDATE: %v, %d", res.Error, res.RowsAffected)
	}
	if res := tx.Exec(`UPDATE khan_rls.doc SET org_id = ? WHERE org_id IS NULL`, orgA); res.Error != nil || res.RowsAffected != 0 {
		t.Fatalf("системийн мөрийг өөртөө авах: %v, %d", res.Error, res.RowsAffected)
	}
	if res := tx.Exec(`DELETE FROM khan_rls.doc WHERE org_id IS NULL`); res.Error != nil || res.RowsAffected != 0 {
		t.Fatalf("системийн мөр DELETE: %v, %d", res.Error, res.RowsAffected)
	}
	if err := tx.Exec(`INSERT INTO khan_rls.doc (org_id, title) VALUES (NULL, 'x')`).Error; err == nil {
		t.Fatal("tenant системийн мөр INSERT хийж чадлаа")
	}
	// DropPolicySQL хоёр policy-г хоёуланг нь устгана
	drop, _ := tenant.DropPolicySQL("khan_rls.doc")
	if !strings.Contains(drop, tenant.SystemPolicyName) {
		t.Fatalf("drop SQL: %s", drop)
	}
}
