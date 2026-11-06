package tenant

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/khan-vision/khan/schema"
	"gorm.io/gorm"
)

// Postgres Row-Level Security (RLS) — tenant тусгаарлалтын ХОЁР ДАХЬ хамгаалалт.
//
// Үндсэн хамгаалалт нь query бүрийн `WHERE org_id = ?` (Scope). Нэг л газар
// мартвал бусад байгууллагын мөр алдагдана (KHA-272: 1512 мөр). RLS-тэй бол
// транзакц бүрт `app.org_id` тавигдаж, Postgres өөрөө зөвхөн тухайн
// байгууллагын мөрийг харуулж/бичүүлнэ — WHERE мартсан ч хамгаалагдана.
//
//	err := tenant.Tx(ctx, db, orgID, func(tx *gorm.DB) error {
//	    return tx.Table("finance.invoice").Find(&rows).Error // RLS + Scope
//	})
//
// АНХААР: superuser болон BYPASSRLS эрхтэй role-д RLS ҮЙЛЧЛЭХГҮЙ. Хүснэгтийн
// эзэнд (owner) зөвхөн FORCE ROW LEVEL SECURITY-тэй үед үйлчилнэ — PolicySQL
// үүнийг анхдагчаар тавина.

// SettingOrgID — транзакцын org_id-ийг хадгалах Postgres тохиргоо.
const SettingOrgID = "app.org_id"

// SettingBypass — системийн ажил (cron, migration)-д RLS-ийг ил тод алгасах тохиргоо.
const SettingBypass = "app.bypass_rls"

// PolicyName — khan-ийн үүсгэдэг RLS policy-ийн нэр.
const PolicyName = "khan_tenant_isolation"

// SystemPolicyName — PolicyOptions.SystemRows-ийн нэмэлт (унших) policy-ийн нэр.
const SystemPolicyName = "khan_system_rows_read"

// ErrNotInTx — WithOrg/WithBypass-ийг транзакцгүй дуудав (тохиргоо SET LOCAL тул
// транзакцгүй бол дараагийн хүсэлтэд connection-оор дамжин алдагдана).
var ErrNotInTx = errors.New("tenant: транзакц дотор дуудах ёстой")

type ctxKey int

const (
	ctxOrg ctxKey = iota + 1
	ctxBypass
)

// WithOrg — одоогийн транзакцад `app.org_id`-г тавина (transaction-local).
func WithOrg(tx *gorm.DB, orgID string) error {
	if orgID == "" || orgID == ZeroUUID {
		return ErrNoOrg
	}
	if !inTx(tx) {
		return ErrNotInTx
	}
	return tx.Exec("SELECT set_config(?, ?, true)", SettingOrgID, orgID).Error
}

// WithBypass — одоогийн транзакцад RLS-ийг алгасна. ЗӨВХӨН системийн ажилд
// (бүх байгууллагаар гүйдэг cron, migration, тайлангийн нэгтгэл); HTTP
// хүсэлтэд хэзээ ч бүү ашигла. Policy нь Strict горимд үүнийг хүлээн авна.
func WithBypass(tx *gorm.DB) error {
	if !inTx(tx) {
		return ErrNotInTx
	}
	return tx.Exec("SELECT set_config(?, 'on', true)", SettingBypass).Error
}

// Tx — orgID-тай транзакц: `app.org_id`-г тавьж, context-д org-ийг
// тэмдэглэнэ (Guard энэ транзакцын query-г scope-тэй гэж тооцно).
func Tx(ctx context.Context, db *gorm.DB, orgID string, fn func(tx *gorm.DB) error) error {
	if db == nil {
		return errors.New("tenant: db nil")
	}
	if orgID == "" || orgID == ZeroUUID {
		return ErrNoOrg
	}
	ctx = context.WithValue(ctx, ctxOrg, orgID)
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := WithOrg(tx, orgID); err != nil {
			return err
		}
		return fn(tx)
	})
}

// SystemTx — RLS-ийг алгассан системийн транзакц (Guard ч алгасна).
func SystemTx(ctx context.Context, db *gorm.DB, fn func(tx *gorm.DB) error) error {
	if db == nil {
		return errors.New("tenant: db nil")
	}
	ctx = context.WithValue(ctx, ctxBypass, true)
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := WithBypass(tx); err != nil {
			return err
		}
		return fn(tx)
	})
}

// OrgFromContext — Tx-ийн тэмдэглэсэн org_id.
func OrgFromContext(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	v, ok := ctx.Value(ctxOrg).(string)
	return v, ok && v != ""
}

func isBypass(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, _ := ctx.Value(ctxBypass).(bool)
	return v
}

func inTx(tx *gorm.DB) bool {
	if tx == nil || tx.Statement == nil {
		return false
	}
	_, ok := tx.Statement.ConnPool.(gorm.TxCommitter)
	return ok
}

// PolicyMode — RLS policy-ийн хатуу байдал.
type PolicyMode int

const (
	// PolicySoft — `app.org_id` тавигдаагүй бол бүх мөрийг зөвшөөрнө (хуучин код
	// эвдрэхгүй), тавигдсан бол зөвхөн тухайн org. Шилжилтийн үеийн горим.
	PolicySoft PolicyMode = iota
	// PolicyStrict — `app.org_id` тавигдаагүй бол юу ч харагдахгүй (WithBypass-аас бусад).
	PolicyStrict
)

// PolicyOptions — PolicySQL-ийн тохиргоо.
type PolicyOptions struct {
	Mode PolicyMode
	// Column — org баганын нэр (хоосон = org_id).
	Column string
	// ColumnType — баганын Postgres төрөл (хоосон = uuid). Index ашиглагдахын тулд
	// тохиргооны утгыг баганын төрөл рүү cast хийнэ.
	ColumnType string
	// NoForce — FORCE ROW LEVEL SECURITY тавихгүй (эзэнд үйлчлэхгүй болно).
	NoForce bool
	// SystemRows — hybrid (shared) хүснэгт: org_id IS NULL (системийн) мөрийг бүх org УНШИХ боломжтой
	// болгоно (нэмэлт `FOR SELECT` policy, SystemPolicyName). Бичилт (INSERT/UPDATE/DELETE)-д үндсэн
	// policy хэвээр тул tenant системийн мөрийг өөрчилж/өөртөө авч чадахгүй; системийн мөр бичих нь
	// bypass (WithBypass/SystemTx)-ээр л.
	SystemRows bool
}

var (
	qualifiedRe = regexp.MustCompile(`^[a-z_][a-z0-9_]*\.[a-z_][a-z0-9_]*$`)
	typeRe      = regexp.MustCompile(`^[a-z][a-z0-9_ ]*$`)
)

// PolicySQL — хүснэгтэд RLS асааж tenant policy үүсгэх DDL (idempotent).
// table нь "schema.name" хэлбэртэй байна.
func PolicySQL(table string, o PolicyOptions) (string, error) {
	if !qualifiedRe.MatchString(table) {
		return "", fmt.Errorf("tenant: хүснэгтийн нэр schema.name байх ёстой: %q", table)
	}
	col := o.Column
	if col == "" {
		col = ClaimOrgID
	}
	if !identRe.MatchString(col) {
		return "", fmt.Errorf("tenant: баганын нэр буруу: %q", col)
	}
	typ := strings.ToLower(o.ColumnType)
	if typ == "" {
		typ = "uuid"
	}
	if !typeRe.MatchString(typ) {
		return "", fmt.Errorf("tenant: баганын төрөл буруу: %q", o.ColumnType)
	}

	setting := fmt.Sprintf("NULLIF(current_setting('%s', true), '')", SettingOrgID)
	match := fmt.Sprintf("%s = %s::%s", col, setting, typ)
	bypass := fmt.Sprintf("current_setting('%s', true) = 'on'", SettingBypass)
	var pred string
	switch o.Mode {
	case PolicyStrict:
		pred = fmt.Sprintf("(%s OR %s)", bypass, match)
	default:
		pred = fmt.Sprintf("(%s IS NULL OR %s OR %s)", setting, bypass, match)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "ALTER TABLE %s ENABLE ROW LEVEL SECURITY;\n", table)
	if o.NoForce {
		fmt.Fprintf(&b, "ALTER TABLE %s NO FORCE ROW LEVEL SECURITY;\n", table)
	} else {
		fmt.Fprintf(&b, "ALTER TABLE %s FORCE ROW LEVEL SECURITY;\n", table)
	}
	fmt.Fprintf(&b, "DROP POLICY IF EXISTS %s ON %s;\n", PolicyName, table)
	fmt.Fprintf(&b, "CREATE POLICY %s ON %s\n  USING %s\n  WITH CHECK %s;\n", PolicyName, table, pred, pred)
	fmt.Fprintf(&b, "DROP POLICY IF EXISTS %s ON %s;\n", SystemPolicyName, table)
	if o.SystemRows {
		fmt.Fprintf(&b, "CREATE POLICY %s ON %s FOR SELECT\n  USING (%s IS NULL);\n", SystemPolicyName, table, col)
	}
	return b.String(), nil
}

// DropPolicySQL — PolicySQL-ийг буцаах DDL.
func DropPolicySQL(table string) (string, error) {
	if !qualifiedRe.MatchString(table) {
		return "", fmt.Errorf("tenant: хүснэгтийн нэр schema.name байх ёстой: %q", table)
	}
	return fmt.Sprintf("DROP POLICY IF EXISTS %s ON %s;\nDROP POLICY IF EXISTS %s ON %s;\nALTER TABLE %s NO FORCE ROW LEVEL SECURITY;\nALTER TABLE %s DISABLE ROW LEVEL SECURITY;\n",
		PolicyName, table, SystemPolicyName, table, table, table), nil
}

// TenantTables — catalog-оос org_id баганатай энгийн хүснэгтүүд (RLS/Guard-ийн хүрээ).
func TenantTables(cat *schema.Catalog) []schema.Table {
	var out []schema.Table
	if cat == nil {
		return out
	}
	for _, t := range cat.Tables {
		if t.Kind == schema.KindTable && t.HasColumn(ClaimOrgID) {
			out = append(out, t)
		}
	}
	return out
}

// PolicySQLForTable — catalog-ийн хүснэгтээс (org_id-ийн төрлийг автоматаар авч) policy DDL.
func PolicySQLForTable(t schema.Table, mode PolicyMode) (string, error) {
	col := t.Column(ClaimOrgID)
	if col == nil {
		return "", fmt.Errorf("tenant: %s-д org_id алга", t.QualifiedName())
	}
	typ := col.UDT
	if typ == "varchar" || typ == "bpchar" {
		typ = "text"
	}
	return PolicySQL(t.QualifiedName(), PolicyOptions{Mode: mode, ColumnType: typ})
}
