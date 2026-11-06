package tenant

import (
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"

	"github.com/khan-vision/khan/schema"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Guard — org_id баганатай (tenant) хүснэгтэд org нөхцөлгүй query орж ирэхийг
// илрүүлэх GORM plugin. Scope-ийг мартсан кодыг prod-д хүрэхээс өмнө (тест,
// dev) барина; prod-д Warn горимоор эхэлж, лог цэвэр болсны дараа Strict.
//
//	gdb.Use(tenant.NewGuard(tenant.TenantTables(cat), tenant.GuardStrict, nil))
//
// Query-г scope-тэй гэж тооцох нөхцөл (аль нэг нь):
//   - WHERE-ийн ДЭЭД түвшний AND нөхцөлүүдийн нэг нь `org_id = ?` / `org_id IN (?)`
//     (`<alias>.org_id` ч болно). OR дотор байгаа org_id тооцогдохгүй.
//   - Query tenant.Tx (app.org_id тавьсан RLS транзакц) эсвэл SystemTx дотор.
//   - INSERT: мөр бүрт org_id утгатай (Create — struct/map-ийн org_id хоосон биш).
//
// Raw/Exec SQL-ийг шалгахгүй (string parse найдваргүй) — тэдгээрийг RLS хамгаална.
type Guard struct {
	tables map[string]bool // "schema.name" ба "name"
	mode   GuardMode
	logf   func(format string, args ...any)
}

// GuardMode — зөрчилд хэрхэн хандах.
type GuardMode int

const (
	// GuardWarn — зөрчлийг логлоод query-г үргэлжлүүлнэ.
	GuardWarn GuardMode = iota
	// GuardStrict — query-г ErrUnscoped алдаагаар зогсооно.
	GuardStrict
)

// ErrUnscoped — tenant хүснэгтэд org нөхцөлгүй query.
var ErrUnscoped = errors.New("tenant: org_id нөхцөлгүй query (бусад байгууллагын мөр алдагдах эрсдэл)")

// NewGuard — tables нь TenantTables(cat)-ийн үр дүн. logf nil бол лог хийхгүй.
func NewGuard(tables []schema.Table, mode GuardMode, logf func(format string, args ...any)) *Guard {
	g := &Guard{tables: map[string]bool{}, mode: mode, logf: logf}
	for _, t := range tables {
		g.tables[t.QualifiedName()] = true
		g.tables[t.Name] = true
	}
	return g
}

// NewGuardFor — хүснэгтийн нэрсээр ("schema.name").
func NewGuardFor(names []string, mode GuardMode, logf func(format string, args ...any)) *Guard {
	g := &Guard{tables: map[string]bool{}, mode: mode, logf: logf}
	for _, n := range names {
		g.tables[n] = true
		if i := strings.LastIndexByte(n, '.'); i >= 0 {
			g.tables[n[i+1:]] = true
		}
	}
	return g
}

// Name — gorm.Plugin.
func (g *Guard) Name() string { return "khan:tenant-guard" }

// Initialize — gorm.Plugin: query/update/delete/create/row callback-ийн өмнө шалгана.
func (g *Guard) Initialize(db *gorm.DB) error {
	cb := db.Callback()
	if err := cb.Query().Before("gorm:query").Register("khan:tenant_guard_query", g.check("SELECT")); err != nil {
		return err
	}
	if err := cb.Row().Before("gorm:row").Register("khan:tenant_guard_row", g.check("SELECT")); err != nil {
		return err
	}
	if err := cb.Update().Before("gorm:update").Register("khan:tenant_guard_update", g.check("UPDATE")); err != nil {
		return err
	}
	if err := cb.Delete().Before("gorm:delete").Register("khan:tenant_guard_delete", g.check("DELETE")); err != nil {
		return err
	}
	return cb.Create().Before("gorm:create").Register("khan:tenant_guard_create", g.checkCreate)
}

func (g *Guard) check(op string) func(*gorm.DB) {
	return func(db *gorm.DB) {
		if db.Error != nil || db.Statement == nil {
			return
		}
		table := statementTable(db.Statement)
		if !g.tables[table] || g.trusted(db) {
			return
		}
		if whereHasOrg(db.Statement) {
			return
		}
		g.violation(db, op, table)
	}
}

func (g *Guard) checkCreate(db *gorm.DB) {
	if db.Error != nil || db.Statement == nil {
		return
	}
	table := statementTable(db.Statement)
	if !g.tables[table] || g.trusted(db) {
		return
	}
	if createHasOrg(db.Statement) {
		return
	}
	g.violation(db, "INSERT", table)
}

func (g *Guard) trusted(db *gorm.DB) bool {
	ctx := db.Statement.Context
	if isBypass(ctx) {
		return true
	}
	_, ok := OrgFromContext(ctx)
	return ok
}

func (g *Guard) violation(db *gorm.DB, op, table string) {
	if g.logf != nil {
		g.logf("[tenant-guard] %s %s: org_id нөхцөлгүй", op, table)
	}
	if g.mode == GuardStrict {
		_ = db.AddError(fmt.Errorf("%w: %s %s", ErrUnscoped, op, table))
	}
}

// statementTable — statement-ийн үндсэн хүснэгт ("schema.name" эсвэл "name").
func statementTable(st *gorm.Statement) string {
	if st.TableExpr != nil && st.TableExpr.SQL != "" {
		return normalizeTable(st.TableExpr.SQL)
	}
	if st.Table != "" {
		return normalizeTable(st.Table)
	}
	if st.Schema != nil {
		return normalizeTable(st.Schema.Table)
	}
	return ""
}

func normalizeTable(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, " \t\n"); i >= 0 {
		s = s[:i]
	}
	s = strings.NewReplacer(`"`, "", "`", "").Replace(s)
	return strings.ToLower(s)
}

// orgCondRe — `org_id = ?`, `x.org_id = $1`, `"org_id" IN ?`, `org_id IN (...)`.
var orgCondRe = regexp.MustCompile(`(?i)(^|[\s(.,"])org_id"?\s*(=|in\b)`)

// orRe — ДЭЭД түвшний OR (хаалтан доторхыг тооцохгүйгээр энгийн шалгалт).
var orRe = regexp.MustCompile(`(?i)\sor\s`)

func whereHasOrg(st *gorm.Statement) bool {
	c, ok := st.Clauses["WHERE"]
	if !ok {
		return false
	}
	w, ok := c.Expression.(clause.Where)
	if !ok {
		return false
	}
	for _, e := range w.Exprs {
		if exprHasOrg(e) {
			return true
		}
	}
	return false
}

func exprHasOrg(e clause.Expression) bool {
	switch x := e.(type) {
	case clause.Eq:
		return isOrgColumn(x.Column) && x.Value != nil && x.Value != ""
	case clause.IN:
		return isOrgColumn(x.Column) && len(x.Values) > 0
	case clause.Expr:
		return sqlHasOrg(x.SQL) && !hasEmptyOrgVar(x.Vars)
	case clause.NamedExpr:
		return sqlHasOrg(x.SQL) && !hasEmptyOrgVar(x.Vars)
	case clause.AndConditions:
		for _, sub := range x.Exprs {
			if exprHasOrg(sub) {
				return true
			}
		}
	}
	// OrConditions, Not гэх мэт — org_id-г баталгаажуулахгүй.
	return false
}

// hasEmptyOrgVar — хувьсагчдын аль нэг нь хоосон/zero UUID бол (ж: `org_id = ?`, "")
// scope хүчингүй гэж үзнэ (байрлалаар нь тааруулахгүй — болгоомжтой тал руу).
func hasEmptyOrgVar(vars []any) bool {
	for _, v := range vars {
		if s, ok := v.(string); ok && (s == "" || s == ZeroUUID) {
			return true
		}
	}
	return false
}

func sqlHasOrg(sql string) bool {
	return orgCondRe.MatchString(sql) && !orRe.MatchString(sql)
}

func isOrgColumn(col any) bool {
	switch c := col.(type) {
	case string:
		return c == ClaimOrgID || strings.HasSuffix(c, "."+ClaimOrgID)
	case clause.Column:
		return c.Name == ClaimOrgID
	}
	return false
}

// createHasOrg — INSERT-ийн мөр бүр org_id утгатай эсэх.
func createHasOrg(st *gorm.Statement) bool {
	if st.Schema != nil {
		f := st.Schema.LookUpField(ClaimOrgID)
		if f == nil {
			return false
		}
		rv := st.ReflectValue
		switch rv.Kind() {
		case reflect.Slice, reflect.Array:
			if rv.Len() == 0 {
				return false
			}
			for i := 0; i < rv.Len(); i++ {
				if _, zero := f.ValueOf(st.Context, rv.Index(i)); zero {
					return false
				}
			}
			return true
		case reflect.Struct:
			_, zero := f.ValueOf(st.Context, rv)
			return !zero
		}
		return false
	}
	// map[string]any / []map[string]any (Table(...).Create(map))
	switch d := st.Dest.(type) {
	case map[string]any:
		return mapHasOrg(d)
	case *map[string]any:
		return d != nil && mapHasOrg(*d)
	case []map[string]any:
		if len(d) == 0 {
			return false
		}
		for _, m := range d {
			if !mapHasOrg(m) {
				return false
			}
		}
		return true
	}
	return false
}

func mapHasOrg(m map[string]any) bool {
	v, ok := m[ClaimOrgID]
	if !ok || v == nil {
		return false
	}
	if s, isStr := v.(string); isStr {
		return s != "" && s != ZeroUUID
	}
	return true
}
