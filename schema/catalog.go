// Package schema нь PostgreSQL-ийн бүтцийг (schema → table/view/matview →
// column, PK, FK, unique, enum, comment) уншиж, зөөврийн Catalog болгоно.
//
// Lambda-ийн DBSchema-аас авсан санаа: DB-ийн бүтэц = codegen, data dictionary,
// form/grid анхдагч, drift шалгалтын НЭГ эх сурвалж.
//
// Ялгаа (сайжруулалт):
//   - Хүснэгтийг schema-аар qualify хийнэ (`finance.invoice`); lambda зөвхөн
//     хүснэгтийн нэрээр хайдаг тул өөр schema-д ижил нэртэй хүснэгт холилддог.
//   - PK/FK/unique-ийг pg_constraint-оос жинхэнээр нь уншина (lambda `nextval(`
//     эсвэл `gen_random_uuid` default-тай багана бүрийг PK гэж таамагладаг).
//   - Partition child, extension-ий хүснэгт (PostGIS spatial_ref_sys г.м.)-ийг хасна.
//   - Enum-ийг varchar болгож нуухгүй — утгуудыг нь Column.Enum-д өгнө.
//   - Цэвэр: файл бичихгүй, глобал state-гүй; snapshot хадгалах нь caller-ийн ажил.
package schema

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
)

// FormatVersion — Catalog JSON-ий хэлбэрийн хувилбар.
const FormatVersion = 1

// Kind — relation-ий төрөл.
type Kind string

const (
	KindTable   Kind = "table"
	KindView    Kind = "view"
	KindMatView Kind = "matview"
)

// Catalog — DB-ийн бүтцийн snapshot.
type Catalog struct {
	Version  int     `json:"version"`
	Database string  `json:"database,omitempty"`
	Tables   []Table `json:"tables"`
}

// Table — хүснэгт, view эсвэл materialized view.
type Table struct {
	Schema      string       `json:"schema"`
	Name        string       `json:"name"`
	Kind        Kind         `json:"kind"`
	Partitioned bool         `json:"partitioned,omitempty"`
	Comment     string       `json:"comment,omitempty"`
	Columns     []Column     `json:"columns"`
	PrimaryKey  []string     `json:"primary_key,omitempty"`
	ForeignKeys []ForeignKey `json:"foreign_keys,omitempty"`
	Uniques     [][]string   `json:"uniques,omitempty"`
}

// QualifiedName — `schema.name`.
func (t Table) QualifiedName() string { return t.Schema + "." + t.Name }

// Column хайх (байхгүй бол nil).
func (t *Table) Column(name string) *Column {
	for i := range t.Columns {
		if t.Columns[i].Name == name {
			return &t.Columns[i]
		}
	}
	return nil
}

// HasColumn — багана байгаа эсэх.
func (t *Table) HasColumn(name string) bool { return t.Column(name) != nil }

// Column — баганын мета.
type Column struct {
	Name     string `json:"name"`
	Position int    `json:"position"`
	// Type — `format_type()` (ж: "numeric(18,2)", "character varying(255)",
	// "timestamp with time zone", "uuid", "text[]", "finance.invoice_status").
	Type string `json:"type"`
	// UDT — суурь төрлийн нэр (pg_type.typname): numeric, varchar, int4, uuid,
	// timestamptz, jsonb, geometry, enum-ийн нэр… Массив бол элементийн төрөл.
	UDT       string   `json:"udt"`
	Array     bool     `json:"array,omitempty"`
	Nullable  bool     `json:"nullable"`
	Default   *string  `json:"default,omitempty"`
	Identity  bool     `json:"identity,omitempty"`
	Generated bool     `json:"generated,omitempty"`
	PK        bool     `json:"pk,omitempty"`
	Precision *int     `json:"precision,omitempty"`
	Scale     *int     `json:"scale,omitempty"`
	MaxLength *int     `json:"max_length,omitempty"`
	Enum      []string `json:"enum,omitempty"`
	Comment   string   `json:"comment,omitempty"`
}

// ForeignKey — гадаад түлхүүр.
type ForeignKey struct {
	Name       string   `json:"name"`
	Columns    []string `json:"columns"`
	RefSchema  string   `json:"ref_schema"`
	RefTable   string   `json:"ref_table"`
	RefColumns []string `json:"ref_columns"`
	// OnDelete — "no action" | "restrict" | "cascade" | "set null" | "set default".
	OnDelete string `json:"on_delete,omitempty"`
}

// Table хайх: "schema.name" эсвэл (давхцалгүй бол) зөвхөн "name".
func (c *Catalog) Table(name string) (*Table, error) {
	var found *Table
	for i := range c.Tables {
		t := &c.Tables[i]
		if t.QualifiedName() == name {
			return t, nil
		}
		if t.Name == name {
			if found != nil {
				return nil, fmt.Errorf("schema: %q олон schema-д байна (%s, %s) — schema.нэр гэж заа",
					name, found.QualifiedName(), t.QualifiedName())
			}
			found = t
		}
	}
	if found == nil {
		return nil, fmt.Errorf("schema: %q хүснэгт олдсонгүй", name)
	}
	return found, nil
}

// Schemas — catalog доторх schema-уудын эрэмбэлсэн жагсаалт.
func (c *Catalog) Schemas() []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range c.Tables {
		if !seen[t.Schema] {
			seen[t.Schema] = true
			out = append(out, t.Schema)
		}
	}
	sort.Strings(out)
	return out
}

// Sort — хүснэгтийг schema.name-ээр, баганыг байрлалаар эрэмбэлнэ (тогтвортой JSON/diff).
func (c *Catalog) Sort() {
	sort.Slice(c.Tables, func(i, j int) bool {
		return c.Tables[i].QualifiedName() < c.Tables[j].QualifiedName()
	})
	for i := range c.Tables {
		cols := c.Tables[i].Columns
		sort.Slice(cols, func(a, b int) bool { return cols[a].Position < cols[b].Position })
		fks := c.Tables[i].ForeignKeys
		sort.Slice(fks, func(a, b int) bool { return fks[a].Name < fks[b].Name })
	}
}

// WriteJSON — тогтвортой (эрэмбэлсэн, indent-тэй) JSON snapshot.
func (c *Catalog) WriteJSON(w io.Writer) error {
	c.Sort()
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(c)
}

// ReadJSON — snapshot уншина.
func ReadJSON(r io.Reader) (*Catalog, error) {
	var c Catalog
	if err := json.NewDecoder(r).Decode(&c); err != nil {
		return nil, fmt.Errorf("schema: snapshot JSON: %w", err)
	}
	if c.Version != FormatVersion {
		return nil, fmt.Errorf("schema: snapshot version %d дэмжигдэхгүй (хүлээсэн %d)", c.Version, FormatVersion)
	}
	return &c, nil
}
