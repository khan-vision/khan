package gen

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/khan-vision/khan/crud"
	"github.com/khan-vision/khan/lookup"
	"github.com/khan-vision/khan/query"
	"github.com/khan-vision/khan/validate"
)

// emitter — Go эх бичигч (import-ыг ашигласнаар нь тооцно).
type emitter struct {
	b      *builder
	w      strings.Builder
	lookup bool
	regexp bool
}

func (e *emitter) p(format string, a ...any) { fmt.Fprintf(&e.w, format, a...) }

func (e *emitter) comments(key string) {
	for _, c := range e.b.comments[key] {
		e.p("// %s\n", c)
	}
}

// emit — төгс Go файлын эх (gofmt-ээс өмнө).
func (b *builder) emit(fn string, r *crud.Resource[map[string]any]) ([]byte, error) {
	e := &emitter{b: b}
	e.resource(fn, r)

	var out strings.Builder
	out.WriteString("// khan gen resource-оор үүсгэсэн НОРООГ (docs/GEN.md): гараар засна — `TODO(khan-gen)` тайлбаруудыг\n")
	out.WriteString("// шийдэж, hook нэмээд crudtest.Isolation-аар шалга. Дахин generate хийвэл гар засварыг дарна.\n")
	fmt.Fprintf(&out, "package %s\n\nimport (\n", b.opts.Package)
	if e.regexp {
		out.WriteString("\t\"regexp\"\n\n")
	}
	out.WriteString("\t\"github.com/gofiber/fiber/v3\"\n\t\"github.com/khan-vision/khan/crud\"\n")
	if e.lookup {
		out.WriteString("\t\"github.com/khan-vision/khan/lookup\"\n")
	}
	out.WriteString("\t\"github.com/khan-vision/khan/query\"\n\t\"github.com/khan-vision/khan/validate\"\n\t\"gorm.io/gorm\"\n)\n\n")
	out.WriteString(e.w.String())
	return []byte(out.String()), nil
}

func (e *emitter) resource(fn string, r *crud.Resource[map[string]any]) {
	b := e.b
	e.p("// %sResource — %s (%s).\n", fn, oneLine(r.Label), r.Table)
	if len(b.issues) > 0 {
		e.p("//\n// schema.Lint зөрчил:\n")
		for _, i := range b.issues {
			e.p("//   - %s\n", oneLine(i.String()))
		}
	}
	e.p("func %sResource(db func() *gorm.DB, can func(fiber.Ctx, crud.Action) bool) *crud.Resource[map[string]any] {\n", fn)
	e.p("\treturn &crud.Resource[map[string]any]{\n")
	e.comments("")
	e.p("Name: %s,\nLabel: %s,\nEndpoint: %s,\nTable: %s,\n", q(r.Name), q(r.Label), q(r.Endpoint), q(r.Table))
	if r.ID.Column != "id" {
		e.p("ID: crud.IDSpec{Column: %s, Kind: crud.%s},\n", q(r.ID.Column), idConst(r.ID.Kind))
	} else {
		e.p("ID: crud.IDSpec{Kind: crud.%s},\n", idConst(r.ID.Kind))
	}
	e.p("\nFields: []crud.Field{\n")
	for _, f := range r.Fields {
		e.field(f, f.Name)
	}
	e.p("},\n")
	e.list(r)
	if len(r.Children) > 0 {
		e.p("\nChildren: []crud.Child{\n")
		for _, ch := range r.Children {
			e.child(ch)
		}
		e.p("},\n")
	}
	for _, k := range sortedKeys(b.comments) {
		if strings.HasPrefix(k, "child:") && !hasChild(r, strings.TrimPrefix(k, "child:")) {
			e.p("\n")
			e.comments(k)
		}
	}
	e.p("\n")
	if r.SoftDelete {
		e.p("SoftDelete: true,\n")
	}
	if r.Version != "" {
		e.p("Version: %s,\n", q(r.Version))
	}
	if r.Timestamps != nil && !*r.Timestamps {
		e.p("Timestamps: crud.Off(),\n")
	}
	if r.TrackUser {
		e.p("TrackUser: true,\n")
	}
	e.p("\nCan: can,\nDB: db,\n")
	e.p("}\n}\n")
}

func hasChild(r *crud.Resource[map[string]any], name string) bool {
	for _, c := range r.Children {
		if c.Name == name {
			return true
		}
	}
	return false
}

func idConst(k crud.IDKind) string {
	if k == crud.IDInt {
		return "IDInt"
	}
	return "IDUUID"
}

func q(s string) string { return strconv.Quote(s) }

func (e *emitter) field(f crud.Field, key string) {
	e.comments(key)
	e.p("{\n")
	e.p("Name: %s,\n", q(f.Name))
	if f.Column != "" && f.Column != f.Name {
		e.p("Column: %s,\n", q(f.Column))
	}
	e.p("Label: %s,\nKind: validate.%s,\n", q(f.Label), kindConst(f.Kind))
	if rs := e.rules(f.Rules); rs != "" {
		e.p("Rules: %s,\n", rs)
	}
	if f.Default != nil {
		e.p("Default: %s,\n", goLiteral(f.Default))
	}
	if f.CreateOnly {
		e.p("CreateOnly: true,\n")
	}
	if f.Ref != nil {
		e.ref(f.Ref)
	}
	if f.Unique {
		e.p("Unique: true,\n")
	}
	if f.Input != "" {
		e.p("Input: %s,\n", q(f.Input))
	}
	e.p("},\n")
}

func goLiteral(v any) string {
	switch x := v.(type) {
	case string:
		return q(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case bool:
		return strconv.FormatBool(x)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	}
	return q(fmt.Sprint(v))
}

func (e *emitter) rules(r validate.Rules) string {
	var parts []string
	if r.Required {
		parts = append(parts, "Required: true")
	}
	if r.Nullable {
		parts = append(parts, "Nullable: true")
	}
	if r.Trim != nil {
		parts = append(parts, "Trim: "+fmt.Sprintf("func() *bool { b := %v; return &b }()", *r.Trim))
	}
	if r.MinLen > 0 {
		parts = append(parts, "MinLen: "+strconv.Itoa(r.MinLen))
	}
	if r.MaxLen > 0 {
		parts = append(parts, "MaxLen: "+strconv.Itoa(r.MaxLen))
	}
	if r.Min != nil {
		parts = append(parts, "Min: validate.Bound("+strconv.FormatFloat(*r.Min, 'f', -1, 64)+")")
	}
	if r.Max != nil {
		parts = append(parts, "Max: validate.Bound("+strconv.FormatFloat(*r.Max, 'f', -1, 64)+")")
	}
	if r.Scale > 0 {
		parts = append(parts, "Scale: "+strconv.Itoa(r.Scale))
	}
	multi := false
	if r.Pattern != nil {
		e.regexp = true
		parts = append(parts, "Pattern: regexp.MustCompile("+rawOrQuote(r.Pattern.String())+")")
		multi = true
	}
	if r.PatternMsg != "" {
		parts = append(parts, "PatternMsg: "+q(r.PatternMsg))
	}
	if len(r.Enum) > 0 {
		var vs []string
		for _, v := range r.Enum {
			vs = append(vs, q(v))
		}
		parts = append(parts, "Enum: []string{"+strings.Join(vs, ", ")+"}")
	}
	if len(parts) == 0 {
		return ""
	}
	inline := "validate.Rules{" + strings.Join(parts, ", ") + "}"
	if multi || len(inline) > 100 {
		return "validate.Rules{\n" + strings.Join(parts, ",\n") + ",\n}"
	}
	return inline
}

func rawOrQuote(s string) string {
	if !strings.ContainsAny(s, "`\n\r") {
		return "`" + s + "`"
	}
	return q(s)
}

func (e *emitter) ref(s *lookup.Spec) {
	e.lookup = true
	e.p("Ref: &lookup.Spec{\n")
	e.p("Table: %s,\n", q(s.Table))
	if s.Value != "" {
		e.p("Value: %s,\n", q(s.Value))
	}
	e.p("Label: %s,\n", strSlice(s.Label))
	if len(s.Search) > 0 {
		e.p("Search: %s,\n", strSlice(s.Search))
	}
	if s.Parent != "" {
		e.p("Parent: %s,\n", q(s.Parent))
	}
	if s.Where != "" {
		e.p("Where: %s,\n", q(s.Where))
	}
	if s.Tenant != nil && !*s.Tenant {
		e.p("Tenant: lookup.TenantOff(),\n")
	}
	if s.SoftDelete {
		e.p("SoftDelete: true,\n")
	}
	if s.Order != "" {
		e.p("Order: %s,\n", q(s.Order))
	}
	e.p("},\n")
}

func strSlice(ss []string) string {
	qs := make([]string, len(ss))
	for i, s := range ss {
		qs[i] = q(s)
	}
	return "[]string{" + strings.Join(qs, ", ") + "}"
}

func (e *emitter) list(r *crud.Resource[map[string]any]) {
	l := r.List
	e.p("\nList: query.Spec{\n")
	if len(l.Search) > 0 {
		e.p("Search: %s,\n", strSlice(l.Search))
	}
	if len(l.Sort) > 0 {
		e.p("Sort: map[string]string{\n")
		for _, k := range sortedKeys(l.Sort) {
			e.p("%s: %s,\n", q(k), q(l.Sort[k]))
		}
		e.p("},\n")
	}
	if len(l.Filters) > 0 {
		e.p("Filters: map[string]query.FilterCol{\n")
		for _, k := range sortedKeys(l.Filters) {
			fc := l.Filters[k]
			e.p("%s: query.%s(%s),\n", q(k), filterConstructor(fc.Kind), q(fc.Col))
		}
		e.p("},\n")
	}
	if len(l.Totals) > 0 {
		e.p("Totals: map[string]string{\n")
		for _, k := range sortedKeys(l.Totals) {
			e.p("%s: %s,\n", q(k), q(l.Totals[k]))
		}
		e.p("},\n")
	}
	e.p("DefaultOrder: %s,\n", q(l.DefaultOrder))
	if l.DefaultLimit > 0 {
		e.p("DefaultLimit: %d,\n", l.DefaultLimit)
	}
	e.p("},\n")
	if e.b.extra.listQuery {
		e.p("ListQuery: func(db *gorm.DB, _ string) *gorm.DB {\n")
		e.p("// Password hash-ийг SELECT-д оруулахгүй.\n")
		e.p("return db.Table(%s).Select(%s)\n},\n", q(r.Table+" "+alias), q(strings.Join(e.b.listSelect, ", ")))
	}
	if r.Export != nil {
		e.export(r.Export)
	}
}

func (e *emitter) export(x *query.ExportSpec) {
	e.p("Export: &query.ExportSpec{\nFileName: %s,\nColumns: []query.ExportColumn{\n", q(x.FileName))
	for _, c := range x.Columns {
		num := ""
		if c.Numeric {
			num = ", Numeric: true"
		}
		e.p("{Key: %s, Header: %s, Expr: %s%s},\n", q(c.Key), q(c.Header), q(c.Expr), num)
	}
	e.p("},\n},\n")
}

func (e *emitter) child(ch crud.Child) {
	key := "child:" + ch.Name
	e.comments(key)
	e.p("{\n")
	e.p("Name: %s,\nLabel: %s,\nTable: %s,\nFK: %s,\n", q(ch.Name), q(ch.Label), q(ch.Table), q(ch.FK))
	if ch.ID.Column != "id" {
		e.p("ID: crud.IDSpec{Column: %s, Kind: crud.IDUUID},\n", q(ch.ID.Column))
	} else {
		e.p("ID: crud.IDSpec{Kind: crud.IDUUID},\n")
	}
	if ch.Order != "" {
		e.p("Order: %s,\n", q(ch.Order))
	}
	if ch.SoftDelete {
		e.p("SoftDelete: true,\n")
	}
	e.p("Fields: []crud.Field{\n")
	for _, f := range ch.Fields {
		e.field(f, ch.Name+"."+f.Name)
	}
	e.p("},\n},\n")
}
