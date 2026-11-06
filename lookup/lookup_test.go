package lookup

import (
	"strings"
	"testing"

	"github.com/khan-vision/khan/validate"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const orgA = "11111111-1111-1111-1111-111111111111"

func dryDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(
		postgres.New(postgres.Config{DSN: "host=/nonexistent-khan-lookup user=x dbname=x", WithoutQuotingCheck: true}),
		&gorm.Config{DryRun: true, DisableAutomaticPing: true, SkipDefaultTransaction: true, Logger: logger.Discard},
	)
	if err != nil {
		t.Skipf("dry-run DB бэлдэх боломжгүй: %v", err)
	}
	return db
}

func dryQuery(t *testing.T, s Spec, p params) (string, []any) {
	t.Helper()
	q, err := s.query(dryDB(t), orgA, p)
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	tx := q.Find(&rows)
	return tx.Statement.SQL.String(), tx.Statement.Vars
}

func empSpec() Spec {
	return Spec{Table: "organization.employee", Label: []string{"last_name", "first_name"}, SoftDelete: true}
}

func TestQueryShape(t *testing.T) {
	sqlStr, vars := dryQuery(t, empSpec(), params{limit: 50})
	for _, want := range []string{
		`SELECT id AS value, concat_ws(' ', last_name, first_name) AS label FROM organization.employee`,
		`org_id = $1`, `deleted_at IS NULL`, `ORDER BY last_name, id`, `LIMIT $2`,
	} {
		if !strings.Contains(sqlStr, want) {
			t.Errorf("SQL-д %q алга:\n%s", want, sqlStr)
		}
	}
	if len(vars) != 2 || vars[0] != orgA {
		t.Errorf("vars=%v", vars)
	}
}

func TestQuerySearchEscapesWildcards(t *testing.T) {
	_, vars := dryQuery(t, empSpec(), params{limit: 10, search: `100%_\x`})
	if len(vars) < 2 || vars[1] != `%100\%\_\\x%` {
		t.Fatalf("wildcard escape хийгдээгүй: %v", vars)
	}
	sqlStr, _ := dryQuery(t, empSpec(), params{limit: 10, search: "a"})
	if !strings.Contains(sqlStr, `concat_ws(' ', last_name, first_name) ILIKE $2 ESCAPE '\'`) {
		t.Errorf("Search nil → label нэгдсэн мөрөөр хайх ёстой:\n%s", sqlStr)
	}
}

func TestQuerySearchColumns(t *testing.T) {
	s := empSpec()
	s.Search = []string{"register", "phone"}
	sqlStr, vars := dryQuery(t, s, params{limit: 10, search: "9"})
	if !strings.Contains(sqlStr, `(CAST(register AS text) ILIKE $2 ESCAPE '\' OR CAST(phone AS text) ILIKE $3 ESCAPE '\')`) {
		t.Errorf("sql:\n%s", sqlStr)
	}
	if len(vars) != 4 {
		t.Errorf("vars=%v", vars)
	}
}

func TestQueryParentTenantOffWhereOrder(t *testing.T) {
	s := Spec{
		Table: "public.lookup", Label: []string{"name"}, Parent: "group_id", Tenant: TenantOff(),
		Where: "status = 'active' AND NOT is_hidden", Order: "sort_no desc, name",
	}
	sqlStr, vars := dryQuery(t, s, params{limit: 5, parent: "7"})
	for _, want := range []string{
		`group_id AS parent_value`, `(status = 'active' AND NOT is_hidden)`, `CAST(group_id AS text) = $1`,
		`ORDER BY sort_no desc, name, id`,
	} {
		if !strings.Contains(sqlStr, want) {
			t.Errorf("SQL-д %q алга:\n%s", want, sqlStr)
		}
	}
	if strings.Contains(sqlStr, "org_id") {
		t.Errorf("Tenant:false үед org_id нөхцөл байх ёсгүй:\n%s", sqlStr)
	}
	if vars[0] != "7" {
		t.Errorf("vars=%v", vars)
	}
}

func TestQueryByIDs(t *testing.T) {
	s := empSpec()
	s.Kind = validate.UUID
	good := "123e4567-e89b-12d3-a456-426614174000"
	p := params{limit: 50, byIDs: true, ids: []any{good}, search: "ignored", parent: "x"}
	sqlStr, _ := dryQuery(t, s, p)
	if !strings.Contains(sqlStr, "id IN ($2)") || strings.Contains(sqlStr, "ILIKE") || strings.Contains(sqlStr, "CAST(") {
		t.Errorf("ids үед search/parent үл тооно:\n%s", sqlStr)
	}
	sqlStr, _ = dryQuery(t, s, params{limit: 50, byIDs: true})
	if !strings.Contains(sqlStr, "1 = 0") {
		t.Errorf("хоосон ids → юу ч буцаахгүй:\n%s", sqlStr)
	}
}

func TestQueryRequiresOrg(t *testing.T) {
	for _, org := range []string{"", "00000000-0000-0000-0000-000000000000"} {
		if _, err := empSpec().query(dryDB(t), org, params{limit: 1}); err == nil {
			t.Errorf("org=%q үед алдаа гарах ёстой", org)
		}
	}
	s := empSpec()
	s.Tenant = TenantOff()
	if _, err := s.query(dryDB(t), "", params{limit: 1}); err != nil {
		t.Errorf("Tenant:false үед org шаардахгүй: %v", err)
	}
}

func TestSpecValidate(t *testing.T) {
	ok := Spec{Table: "organization.employee", Label: []string{"name"}}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	mod := func(f func(*Spec)) Spec { s := ok; f(&s); return s }
	bad := map[string]Spec{
		"table":         mod(func(s *Spec) { s.Table = "a;b" }),
		"table upper":   mod(func(s *Spec) { s.Table = "Public.Employee" }),
		"table 3 parts": mod(func(s *Spec) { s.Table = "a.b.c" }),
		"table empty":   mod(func(s *Spec) { s.Table = "" }),
		"value":         mod(func(s *Spec) { s.Value = "id; drop" }),
		"no label":      mod(func(s *Spec) { s.Label = nil }),
		"label":         mod(func(s *Spec) { s.Label = []string{"a b"} }),
		"search":        mod(func(s *Spec) { s.Search = []string{"x)--"} }),
		"parent":        mod(func(s *Spec) { s.Parent = "p.q" }),
		"order":         mod(func(s *Spec) { s.Order = "name; drop table x" }),
		"order fn":      mod(func(s *Spec) { s.Order = "lower(name)" }),
		"where":         mod(func(s *Spec) { s.Where = "1=1; drop table x" }),
		"limit neg":     mod(func(s *Spec) { s.Limit = -1 }),
		"limit big":     mod(func(s *Spec) { s.Limit = 201 }),
		"kind":          mod(func(s *Spec) { s.Kind = validate.Money }),
	}
	for name, s := range bad {
		if err := s.Validate(); err == nil {
			t.Errorf("%s: алдаа хүлээсэн", name)
		}
	}
	good := []Spec{
		mod(func(s *Spec) { s.Table = "employee" }),
		mod(func(s *Spec) { s.Order = "name asc, code DESC" }),
		mod(func(s *Spec) { s.Where = "status = 'active'" }),
		mod(func(s *Spec) { s.Limit = 200; s.Kind = validate.Int }),
	}
	for i, s := range good {
		if err := s.Validate(); err != nil {
			t.Errorf("good[%d]: %v", i, err)
		}
	}
}

func TestValidateWhere(t *testing.T) {
	good := []string{
		`status = 'active'`,
		`status <> 'x' AND kind != 'y'`,
		`a = 1 OR b >= 2.5 OR c < -3`,
		`NOT (a = 1 OR b = 2) AND c IS NOT NULL`,
		`deleted_at IS NULL`,
		`status IN ('a', 'b', 'c')`,
		`status NOT IN ('a')`,
		`is_active = true AND is_hidden = FALSE`,
		`name = 'O''Brien'`,
		`name = 'Бат-Эрдэнэ'`,
		`((a = 1))`,
		`a=1`,
		`a=-1`,
		`type_id in (1,2)`,
		`NOT NOT a = 1`,
	}
	for _, w := range good {
		if err := ValidateWhere(w); err != nil {
			t.Errorf("%q зөвшөөрөгдөх ёстой: %v", w, err)
		}
	}
	bad := []string{
		``, `   `,
		`a = 1; DROP TABLE x`,
		`a = 1 -- comment`,
		`a = 1 /* x */`,
		`a = 'x' */`,
		`a = (SELECT 1)`,
		`a IN (SELECT id FROM x)`,
		`lower(a) = 'x'`,
		`now() > a`,
		`a = now()`,
		`a::text = 'x'`,
		`a = 'x'::text`,
		`a = $1`,
		`a = ?`,
		`a = 'x?'`,
		`a = 'x\'`,
		`a = 'unterminated`,
		`a = b`,
		`a = NULL`,
		`a IS 1`,
		`a`,
		`a = 1 AND`,
		`AND a = 1`,
		`a = 1 b = 2`,
		`(a = 1`,
		`a = 1)`,
		`a LIKE 'x'`,
		`a = 1 UNION SELECT 1`,
		`A = 1`,
		`a.b = 1`,
		`"a" = 1`,
		`a = 1 OR`,
		`a == 1`,
		`a ! 1`,
		`a = 1x`,
		`a = 0x10`,
		`a IN ()`,
		`a IN (1,)`,
		`a IN ('a' 'b')`,
		`a = 1e5`,
		`a = 'x' || 'y'`,
		`a = ` + strings.Repeat("1", 600),
		strings.Repeat("(", 20) + "a = 1" + strings.Repeat(")", 20),
		strings.Repeat("NOT ", 20) + "a = 1",
		"a = 1\x00",
		`a = 'ө'; --`,
	}
	for _, w := range bad {
		if err := ValidateWhere(w); err == nil {
			t.Errorf("%q татгалзах ёстой", w)
		}
	}
	long := "a IN (" + strings.TrimSuffix(strings.Repeat("1,", 60), ",") + ")"
	if err := ValidateWhere(long); err == nil {
		t.Errorf("IN жагсаалт 50-аас хэтрэхэд татгалзах ёстой")
	}
}

func TestCoerce(t *testing.T) {
	u := Spec{Kind: validate.UUID}
	if v, ok := u.coerce("123E4567-E89B-12D3-A456-426614174000"); !ok || v != "123e4567-e89b-12d3-a456-426614174000" {
		t.Errorf("uuid lower: %v %v", v, ok)
	}
	for _, bad := range []any{"abc", "", nil, 5, "123e4567-e89b-12d3-a456-42661417400"} {
		if _, ok := u.coerce(bad); ok {
			t.Errorf("uuid %v татгалзах ёстой", bad)
		}
	}
	i := Spec{Kind: validate.Int}
	if v, ok := i.coerce("42"); !ok || v != int64(42) {
		t.Errorf("int: %v", v)
	}
	if v, ok := i.coerce(int32(7)); !ok || v != int64(7) {
		t.Errorf("int32: %v", v)
	}
	for _, bad := range []any{"x", "1.5", "", "99999999999999999999"} {
		if _, ok := i.coerce(bad); ok {
			t.Errorf("int %v татгалзах ёстой", bad)
		}
	}
	str := Spec{}
	if v, ok := str.coerce("abc"); !ok || v != "abc" {
		t.Error("string")
	}
	if _, ok := str.coerce(""); ok {
		t.Error("хоосон string")
	}
}

func TestNormalizeAndHelpers(t *testing.T) {
	if normalize([]byte("x")) != "x" {
		t.Error("[]byte")
	}
	var u [16]byte
	u[0], u[15] = 0xab, 0xcd
	if normalize(u) != "ab000000-0000-0000-0000-0000000000cd" {
		t.Errorf("[16]byte: %v", normalize(u))
	}
	if clampLimit(0, 50) != 50 || clampLimit(-3, 7) != 7 || clampLimit(1000, 50) != 200 || clampLimit(9, 50) != 9 {
		t.Error("clampLimit")
	}
	if truncateRunes("абвгд", 3) != "абв" || truncateRunes("аб", 3) != "аб" {
		t.Error("truncateRunes")
	}
	if escapeLike(`a%b_c\d`) != `a\%b\_c\\d` {
		t.Error("escapeLike")
	}
}
