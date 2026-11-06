package schema

import (
	"fmt"
	"sort"
)

// ChangeKind — snapshot хоорондын өөрчлөлтийн төрөл.
type ChangeKind string

const (
	TableAdded    ChangeKind = "table_added"
	TableRemoved  ChangeKind = "table_removed"
	ColumnAdded   ChangeKind = "column_added"
	ColumnRemoved ChangeKind = "column_removed"
	ColumnChanged ChangeKind = "column_changed"
)

// Change — нэг өөрчлөлт.
type Change struct {
	Kind   ChangeKind `json:"kind"`
	Table  string     `json:"table"`
	Column string     `json:"column,omitempty"`
	From   string     `json:"from,omitempty"`
	To     string     `json:"to,omitempty"`
}

func (c Change) String() string {
	switch c.Kind {
	case ColumnChanged:
		return fmt.Sprintf("~ %s.%s: %s → %s", c.Table, c.Column, c.From, c.To)
	case ColumnAdded:
		return fmt.Sprintf("+ %s.%s %s", c.Table, c.Column, c.To)
	case ColumnRemoved:
		return fmt.Sprintf("- %s.%s %s", c.Table, c.Column, c.From)
	case TableAdded:
		return "+ " + c.Table
	default:
		return "- " + c.Table
	}
}

// Diff — хоёр snapshot-ийн ялгаа (хүснэгт/багана нэмэгдсэн, хасагдсан,
// төрөл/nullable/default өөрчлөгдсөн). Үр дүн тогтвортой эрэмбэтэй.
func Diff(from, to *Catalog) []Change {
	a, b := tableMap(from), tableMap(to)
	var out []Change
	for name, ta := range a {
		tb, ok := b[name]
		if !ok {
			out = append(out, Change{Kind: TableRemoved, Table: name})
			continue
		}
		ca, cb := columnMap(ta), columnMap(tb)
		for col, x := range ca {
			y, ok := cb[col]
			switch {
			case !ok:
				out = append(out, Change{Kind: ColumnRemoved, Table: name, Column: col, From: describe(x)})
			case describe(x) != describe(y):
				out = append(out, Change{Kind: ColumnChanged, Table: name, Column: col, From: describe(x), To: describe(y)})
			}
		}
		for col, y := range cb {
			if _, ok := ca[col]; !ok {
				out = append(out, Change{Kind: ColumnAdded, Table: name, Column: col, To: describe(y)})
			}
		}
	}
	for name := range b {
		if _, ok := a[name]; !ok {
			out = append(out, Change{Kind: TableAdded, Table: name})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Table != out[j].Table {
			return out[i].Table < out[j].Table
		}
		if out[i].Column != out[j].Column {
			return out[i].Column < out[j].Column
		}
		return out[i].Kind < out[j].Kind
	})
	return out
}

func describe(c *Column) string {
	s := c.Type
	if !c.Nullable {
		s += " not null"
	}
	if c.Default != nil {
		s += " default " + *c.Default
	}
	return s
}

func tableMap(c *Catalog) map[string]*Table {
	m := map[string]*Table{}
	if c == nil {
		return m
	}
	for i := range c.Tables {
		m[c.Tables[i].QualifiedName()] = &c.Tables[i]
	}
	return m
}

func columnMap(t *Table) map[string]*Column {
	m := map[string]*Column{}
	for i := range t.Columns {
		m[t.Columns[i].Name] = &t.Columns[i]
	}
	return m
}
