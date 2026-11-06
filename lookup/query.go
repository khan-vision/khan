package lookup

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/khan-vision/khan/tenant"
	"github.com/khan-vision/khan/validate"
	"gorm.io/gorm"
)

// params — Handler-ийн задалсан хүсэлт.
type params struct {
	search string
	parent string
	ids    []any // Kind-аар задалсан
	byIDs  bool
	limit  int
}

func (s Spec) labelExpr() string {
	return "concat_ws(' ', " + strings.Join(s.Label, ", ") + ")"
}

func (s Spec) orderBy() string {
	order := s.Order
	if order == "" {
		order = s.Label[0]
	}
	return order + ", " + s.valueCol() // тогтвортой эрэмбэ (tie-break)
}

// escapeLike — ILIKE-ийн wildcard-г (\ % _) escape хийнэ.
func escapeLike(v string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(v)
}

// scoped — table + org + soft delete + Where нөхцөлтэй query (SELECT-гүй).
func (s Spec) scoped(db *gorm.DB, orgID string) (*gorm.DB, error) {
	if err := s.requireOrg(orgID); err != nil {
		return nil, err
	}
	q := db.Table(s.Table)
	switch {
	case s.tenant() && s.Shared:
		q = q.Where(tenant.SharedCond(""), orgID)
	case s.tenant():
		q = q.Where("org_id = ?", orgID)
	}
	if s.SoftDelete {
		q = q.Where("deleted_at IS NULL")
	}
	if s.Where != "" {
		q = q.Where("(" + s.Where + ")")
	}
	return q, nil
}

// query — сонголтын SELECT (гүйцэтгээгүй). Тестэд DryRun-аар SQL-ийг шалгана.
func (s Spec) query(db *gorm.DB, orgID string, p params) (*gorm.DB, error) {
	q, err := s.scoped(db, orgID)
	if err != nil {
		return nil, err
	}
	sel := s.valueCol() + " AS value, " + s.labelExpr() + " AS label"
	if s.Parent != "" {
		sel += ", " + s.Parent + " AS parent_value"
	}
	q = q.Select(sel)

	switch {
	case p.byIDs:
		if len(p.ids) == 0 {
			return q.Where("1 = 0"), nil
		}
		q = q.Where(s.inValues(len(p.ids)), p.ids...)
	default:
		if p.parent != "" {
			q = q.Where("CAST("+s.Parent+" AS text) = ?", p.parent)
		}
		if p.search != "" {
			like := "%" + escapeLike(p.search) + "%"
			if len(s.Search) == 0 {
				q = q.Where(s.labelExpr()+` ILIKE ? ESCAPE '\'`, like)
			} else {
				conds := make([]string, len(s.Search))
				args := make([]any, len(s.Search))
				for i, c := range s.Search {
					conds[i] = "CAST(" + c + ` AS text) ILIKE ? ESCAPE '\'`
					args[i] = like
				}
				q = q.Where("("+strings.Join(conds, " OR ")+")", args...)
			}
		}
	}
	return q.Order(s.orderBy()).Limit(p.limit), nil
}

// coerce — утгыг Spec.Kind-аар задална; буруу бол ok=false (DB-д очихгүй).
func (s Spec) coerce(v any) (any, bool) {
	var txt string
	switch t := v.(type) {
	case nil:
		return nil, false
	case string:
		txt = t
	case []byte:
		txt = string(t)
	default:
		txt = fmt.Sprint(t)
	}
	switch s.Kind {
	case validate.UUID, validate.Int:
		raw, _ := json.Marshal(txt)
		out, present, msg := validate.Parse(s.Kind, raw, validate.Rules{}, "")
		if msg != "" || !present || out == nil {
			return nil, false
		}
		return out, true
	}
	if txt == "" {
		return nil, false
	}
	return txt, true
}

// normalize — драйверийн утгыг JSON/харьцуулалтад тохиромжтой болгоно.
func normalize(v any) any {
	switch t := v.(type) {
	case []byte:
		return string(t)
	case [16]byte:
		return fmt.Sprintf("%x-%x-%x-%x-%x", t[0:4], t[4:6], t[6:8], t[8:10], t[10:16])
	}
	return v
}

// key — харьцуулах түлхүүр (coerce хийсэн утга ба DB-ээс уншсан утга хоёрт ижил).
func key(v any) string { return fmt.Sprint(normalize(v)) }

func clampLimit(n, def int) int {
	if n <= 0 {
		n = def
	}
	if n > MaxLimit {
		n = MaxLimit
	}
	return n
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n])
}

// inValues — `value IN (...)` нөхцөл (утга бүр тусдаа placeholder). Int үед утга бүрийг
// bigint-ээр cast хийнэ: int4 баганад int4-өөс их утга pgx encode алдаа (500) биш
// «олдсонгүй» болно.
func (s Spec) inValues(n int) string {
	ph := "?"
	if s.Kind == validate.Int {
		ph = "CAST(? AS bigint)"
	}
	return s.valueCol() + " IN (" + strings.TrimSuffix(strings.Repeat(ph+",", n), ",") + ")"
}
