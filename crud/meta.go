package crud

import (
	"sort"

	"github.com/khan-vision/khan/query"
	"github.com/khan-vision/khan/validate"
)

// Meta — codegen болон GET /meta-д хэрэгтэй бүх мэдээлэл (DB, Can-гүйгээр бүтнэ).
type Meta struct {
	Name       string `json:"name"`
	Label      string `json:"label"`
	Endpoint   string `json:"endpoint"`
	Table      string `json:"-"` // DB-ийн жинхэнэ нэрийг GET /meta-аар ил гаргахгүй
	ID         IDMeta `json:"id"`
	Version    string `json:"version,omitempty"`
	SoftDelete bool   `json:"soft_delete"`

	Fields   []FieldMeta `json:"fields"`
	Children []ChildMeta `json:"children"`

	// Filterable — List.Filters-ийн түлхүүр → төрөл (KvGrid шүүлт).
	Filterable map[string]query.FilterKind `json:"filterable"`
	// Sort — List.Sort-ийн түлхүүрүүд (эрэмблэсэн).
	Sort   []string `json:"sort"`
	Search bool     `json:"search"`
	Totals []string `json:"totals"`

	Export     bool     `json:"export"`
	ExposeMeta bool     `json:"expose_meta"` // GET /meta mount хийгдэх эсэх
	Bulk       bool     `json:"bulk"`
	BulkFields []string `json:"bulk_fields,omitempty"`
	BulkDelete bool     `json:"bulk_delete,omitempty"`

	StrictUnknown bool `json:"strict_unknown"`
}

// IDMeta — id баганын мэдээлэл.
type IDMeta struct {
	Column string `json:"column"`
	Kind   IDKind `json:"kind"`
}

// FieldMeta — нэг талбар.
type FieldMeta struct {
	Name       string        `json:"name"`
	Label      string        `json:"label"`
	Kind       validate.Kind `json:"kind"`
	Input      string        `json:"input,omitempty"`
	Required   bool          `json:"required"`
	Nullable   bool          `json:"nullable"`
	CreateOnly bool          `json:"create_only"`
	Unique     bool          `json:"unique"`
	MinLen     int           `json:"min_len,omitempty"`
	MaxLen     int           `json:"max_len,omitempty"`
	Min        *float64      `json:"min,omitempty"`
	Max        *float64      `json:"max,omitempty"`
	Scale      int           `json:"scale,omitempty"`
	Pattern    string        `json:"pattern,omitempty"`
	PatternMsg string        `json:"pattern_message,omitempty"`
	Enum       []string      `json:"enum,omitempty"`
	Default    any           `json:"default,omitempty"`
	Ref        *RefMeta      `json:"ref,omitempty"`
}

// RefMeta — FK талбарын сонголтын endpoint.
type RefMeta struct {
	// Options — Endpoint + "/options/" + key (Endpoint хоосон бол "/options/<key>" хэсэг).
	Options string `json:"options"`
	Parent  bool   `json:"parent"`
}

// ChildMeta — subform.
type ChildMeta struct {
	Name    string      `json:"name"`
	Label   string      `json:"label"`
	ID      IDMeta      `json:"id"` // child мөрийн id багана (payload-ийн түлхүүр)
	MaxRows int         `json:"max_rows"`
	Fields  []FieldMeta `json:"fields"`
}

// Meta — Resource-ийн meta. Тохиргоо буруу (Check() алдаатай) бол зөвхөн нэр/шошго/endpoint.
// Mount-аас өмнө дуудаж болно (DB, Can шаардахгүй).
func (r *Resource[T]) Meta() Meta {
	e, err := r.build(false)
	if err != nil {
		return Meta{Name: r.Name, Label: r.Label, Endpoint: r.Endpoint, Table: r.Table,
			Fields: []FieldMeta{}, Children: []ChildMeta{}, Filterable: map[string]query.FilterKind{}, Sort: []string{}, Totals: []string{}}
	}
	return e.meta()
}

func (e *engine) meta() Meta {
	m := Meta{
		Name: e.name, Label: e.label, Endpoint: e.endpoint, Table: e.table,
		ID:            IDMeta{Column: e.idCol, Kind: e.idKind},
		Version:       e.versionCol,
		SoftDelete:    e.softDelete,
		Fields:        e.fieldMetas(e.fields),
		Children:      make([]ChildMeta, 0, len(e.children)),
		Filterable:    query.Filterable(e.listSpec.Filters),
		Sort:          sortedKeys(e.listSpec.Sort),
		Search:        len(e.listSpec.Search) > 0,
		Totals:        sortedKeys(e.listSpec.Totals),
		Export:        e.export != nil,
		ExposeMeta:    e.exposeMeta,
		Bulk:          e.bulk != nil,
		StrictUnknown: e.strictUnknown,
	}
	if e.bulk != nil {
		m.BulkFields = sortedKeys(e.bulk.Fields)
		m.BulkDelete = e.bulk.AllowDelete
	}
	for i := range e.children {
		cp := &e.children[i]
		m.Children = append(m.Children, ChildMeta{
			Name: cp.Name, Label: cp.label, MaxRows: cp.maxRows, Fields: e.fieldMetas(cp.fields),
			ID: IDMeta{Column: cp.idCol, Kind: cp.idKind},
		})
	}
	return m
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (e *engine) fieldMetas(fields []fieldPlan) []FieldMeta {
	out := make([]FieldMeta, 0, len(fields))
	for i := range fields {
		f := &fields[i]
		fm := FieldMeta{
			Name: f.Name, Label: f.label, Kind: f.Kind, Input: f.Input,
			Required: f.Rules.Required, Nullable: f.Rules.Nullable, CreateOnly: f.CreateOnly, Unique: f.Unique,
			MinLen: f.Rules.MinLen, MaxLen: f.Rules.EffectiveMaxLen(f.Kind),
			Min: f.Rules.Min, Max: f.Rules.Max,
			PatternMsg: f.Rules.PatternMsg,
		}
		if f.Kind == validate.Number {
			fm.Scale = f.Rules.EffectiveScale(f.Kind)
		}
		if f.Kind == validate.Money {
			fm.Scale = 2
		}
		if f.Kind == validate.Password && fm.MinLen < validate.MinPasswordLen {
			fm.MinLen = validate.MinPasswordLen
		}
		if f.Rules.Pattern != nil {
			fm.Pattern = f.Rules.Pattern.String()
		}
		if len(f.Rules.Enum) > 0 {
			fm.Enum = append([]string(nil), f.Rules.Enum...)
		}
		if f.hasDef {
			fm.Default = f.Default
		}
		if f.ref != nil {
			fm.Ref = &RefMeta{Options: e.endpoint + "/options/" + f.key, Parent: f.ref.Parent != ""}
		}
		out = append(out, fm)
	}
	return out
}
