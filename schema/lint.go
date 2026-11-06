package schema

import (
	"fmt"
	"strings"
)

// Issue — Khan Vision-ий хүснэгтийн дүрмийн зөрчил.
type Issue struct {
	Table  string `json:"table"`
	Column string `json:"column,omitempty"`
	Rule   string `json:"rule"`
	Msg    string `json:"message"`
}

func (i Issue) String() string {
	if i.Column != "" {
		return fmt.Sprintf("%s.%s [%s] %s", i.Table, i.Column, i.Rule, i.Msg)
	}
	return fmt.Sprintf("%s [%s] %s", i.Table, i.Rule, i.Msg)
}

// Lint rule нэрс.
const (
	RuleOrgID      = "org-id"
	RuleTimestamps = "timestamps"
	RuleMoney      = "money"
	RulePrimaryKey = "primary-key"
)

// moneyHints — мөнгөн дүн гэж үзэх баганын нэрийн хэсгүүд.
var moneyHints = []string{"amount", "price", "total", "balance", "salary", "cost", "fee", "tax", "paid", "debit", "credit"}

// Lint — AGENTS.md-ийн хүснэгтийн дүрмүүд (зөвхөн энгийн хүснэгтэд):
//   - org_id uuid NOT NULL (multi-tenant)
//   - created_at / updated_at / deleted_at
//   - мөнгөн дүн numeric(18,2) (float4/float8 хэзээ ч биш)
//   - primary key заавал
func Lint(t Table) []Issue {
	if t.Kind != KindTable {
		return nil
	}
	name := t.QualifiedName()
	var out []Issue
	add := func(col, rule, msg string) { out = append(out, Issue{Table: name, Column: col, Rule: rule, Msg: msg}) }

	if len(t.PrimaryKey) == 0 {
		add("", RulePrimaryKey, "primary key алга")
	}
	switch org := t.Column("org_id"); {
	case org == nil:
		add("org_id", RuleOrgID, "org_id багана алга (multi-tenant заавал)")
	case !strings.EqualFold(org.UDT, "uuid"):
		add("org_id", RuleOrgID, "org_id нь uuid байх ёстой ("+org.Type+")")
	case org.Nullable:
		add("org_id", RuleOrgID, "org_id NOT NULL байх ёстой")
	}
	for _, ts := range []string{"created_at", "updated_at", "deleted_at"} {
		if !t.HasColumn(ts) {
			add(ts, RuleTimestamps, ts+" багана алга")
		}
	}
	for _, c := range t.Columns {
		udt := strings.ToLower(c.UDT)
		if udt == "float4" || udt == "float8" || udt == "money" {
			if looksLikeMoney(c.Name) {
				add(c.Name, RuleMoney, "мөнгөн дүн numeric(18,2) байх ёстой ("+c.Type+")")
			}
			continue
		}
		if udt == "numeric" && looksLikeMoney(c.Name) && !IsMoney(c) {
			add(c.Name, RuleMoney, "мөнгөн дүн numeric(18,2) байх ёстой ("+c.Type+")")
		}
	}
	return out
}

func looksLikeMoney(col string) bool {
	col = strings.ToLower(col)
	for _, h := range moneyHints {
		if strings.Contains(col, h) {
			return true
		}
	}
	return false
}
