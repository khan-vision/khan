// Package lookup нь relation-validated сонголт (select/autocomplete) ба FK
// шалгалтын нэг эх сурвалж юм. Нэг `Spec` хоёр зүйлд үйлчилнэ:
//
//   - Handler — `GET /options/:field` (`?search=&parent=&ids=a,b&limit=`) →
//     `{"data":[{"value","label","parent_value"?}]}`;
//   - Exists — create/update үед FK утга нь тухайн org-д (Where-ийн хүрээнд)
//     байгааг батлах (өөр org-ийн id-г зааж холбохоос сэргийлнэ).
//
// Бүх идентификатор Validate-д regex-ээр, Where нь жижиг grammar-аар шалгагдана;
// хэрэглэгчийн өгөгдөл SQL-д зөвхөн parameter-ээр орно.
package lookup

import (
	"errors"
	"fmt"
	"regexp"

	"github.com/khan-vision/khan/tenant"
	"github.com/khan-vision/khan/validate"
)

// Хязгаарууд.
const (
	DefaultLimit = 50
	MaxLimit     = 200
	MaxIDs       = 200
	maxSearchLen = 100 // rune
	existsChunk  = 1000
)

var (
	identRe = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)
	tableRe = regexp.MustCompile(`^([a-z_][a-z0-9_]*\.)?[a-z_][a-z0-9_]*$`)
	orderRe = regexp.MustCompile(`^[a-z_][a-z0-9_]*( (?i:asc|desc))?(, ?[a-z_][a-z0-9_]*( (?i:asc|desc))?)*$`)
)

// Spec — нэг lookup хүснэгтийн тодорхойлолт.
type Spec struct {
	Table  string   // "organization.employee" (schema заавал биш)
	Value  string   // утгын багана; хоосон бол "id"
	Label  []string // ["last_name","first_name"] → concat_ws(' ', ...)
	Search []string // ILIKE баганууд; nil → Label-ийн нэгдсэн мөр ("Овог Нэр" гэж хайж болно)
	Parent string   // optional parent багана → parent_value, `?parent=`
	Where  string   // тогтмол нөхцөл (параметргүй; Validate-д grammar-аар шалгагдана; ж: "status = 'active'")
	Tenant *bool    // nil=true: org_id = ? нөхцөл
	// SoftDelete — deleted_at IS NULL.
	SoftDelete bool
	Order      string // "col [asc|desc], ..."; хоосон бол Label[0]
	Limit      int    // `?limit` өгөөгүй үеийн хэмжээ (анхдагч 50, дээд 200)
	// Kind — Value баганын төрөл: validate.UUID, validate.Int, эсвэл хоосон/String.
	// ids/Exists-ийн утгыг энэ төрлөөр задлах тул uuid/int багана дээр `abc` гэх мэт
	// буруу утга DB алдаа (500) биш "олдсонгүй" болно. crud нь Field.Kind-аас автоматаар тавина.
	Kind validate.Kind
}

// Option — нэг сонголт. JSON: value, label, parent_value.
type Option struct {
	Value       any    `json:"value"`
	Label       string `json:"label"`
	ParentValue any    `json:"parent_value,omitempty"`
}

func (s Spec) valueCol() string {
	if s.Value == "" {
		return "id"
	}
	return s.Value
}

func (s Spec) tenant() bool { return s.Tenant == nil || *s.Tenant }

func (s Spec) defaultLimit() int {
	if s.Limit > 0 {
		return s.Limit
	}
	return DefaultLimit
}

// TenantOff — Tenant: false болгох туслах (`Tenant: lookup.TenantOff()`).
func TenantOff() *bool { b := false; return &b }

// Validate — Spec-ийн програмчлалын алдааг илрүүлнэ (crud.Mount, Handler дуудахаас өмнө).
func (s Spec) Validate() error {
	if !tableRe.MatchString(s.Table) {
		return fmt.Errorf("khan/lookup: Table буруу: %q", s.Table)
	}
	if !identRe.MatchString(s.valueCol()) {
		return fmt.Errorf("khan/lookup: Value буруу: %q", s.Value)
	}
	if len(s.Label) == 0 {
		return errors.New("khan/lookup: Label хоосон")
	}
	for _, group := range []struct {
		name string
		cols []string
	}{{"Label", s.Label}, {"Search", s.Search}} {
		for _, c := range group.cols {
			if !identRe.MatchString(c) {
				return fmt.Errorf("khan/lookup: %s багана буруу: %q", group.name, c)
			}
		}
	}
	if s.Parent != "" && !identRe.MatchString(s.Parent) {
		return fmt.Errorf("khan/lookup: Parent буруу: %q", s.Parent)
	}
	if s.Order != "" && !orderRe.MatchString(s.Order) {
		return fmt.Errorf("khan/lookup: Order буруу: %q", s.Order)
	}
	if s.Where != "" {
		if err := ValidateWhere(s.Where); err != nil {
			return err
		}
	}
	if s.Limit < 0 || s.Limit > MaxLimit {
		return fmt.Errorf("khan/lookup: Limit 0..%d байх ёстой", MaxLimit)
	}
	switch s.Kind {
	case "", validate.String, validate.UUID, validate.Int:
	default:
		return fmt.Errorf("khan/lookup: Kind %q дэмжигдэхгүй (UUID/Int/String)", s.Kind)
	}
	return nil
}

func (s Spec) requireOrg(orgID string) error {
	if s.tenant() && (orgID == "" || orgID == tenant.ZeroUUID) {
		return tenant.ErrNoOrg
	}
	return nil
}
