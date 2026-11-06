package gen

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/khan-vision/khan/crud"
	"github.com/khan-vision/khan/query"
	"github.com/khan-vision/khan/schema"
	"github.com/khan-vision/khan/validate"
	"gorm.io/gorm"
)

const alias = "t"

var identRe = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// listKind — жагсаалтын sort/filter-д тохирох баганын Kind (JSON, дэмжигдээгүй, Password-ийг хасна).
func (b *builder) listKind(c schema.Column) (validate.Kind, bool) {
	ki, why := baseKind(c)
	if why != "" || ki.Kind == validate.JSON || b.isPassword(c.Name) {
		return "", false
	}
	return ki.Kind, true
}

func (b *builder) isPassword(col string) bool {
	for _, p := range b.pwCols {
		if p == col {
			return true
		}
	}
	return false
}

// buildList — List (query.Spec), Totals, Export, Password-ийн ListQuery-г grid/catalog-оос бүрдүүлнэ.
func (b *builder) buildList(r *crud.Resource[map[string]any]) {
	t := b.t
	g := b.grid
	spec := &r.List
	spec.Sort = map[string]string{}
	spec.Filters = map[string]query.FilterCol{}
	ref := func(col string) string { return alias + "." + col }

	// Grid-ийн баганууд хүснэгтэд байгаа эсэх.
	gridCols := map[string]*LegacyGridColumn{}
	anySortable := false
	if g != nil {
		var missing []string
		for i := range g.Columns {
			gc := &g.Columns[i]
			if gc.Virtual || t.Column(gc.Model) == nil {
				missing = append(missing, gc.Model)
				continue
			}
			gridCols[gc.Model] = gc
			anySortable = anySortable || gc.Sortable
		}
		b.gridTodos(g, t, missing)
	}

	var searchCols []string
	for _, c := range t.Columns {
		kind, ok := b.listKind(c)
		if !ok {
			continue
		}
		gc := gridCols[c.Name]
		if g != nil && gc == nil {
			continue
		}
		// Grid-д sortable огт тэмдэглээгүй бол бүх баганаар эрэмбэлэхийг зөвшөөрнө (tenant/аудитын багана биш).
		if sortable := !b.internalCol(c.Name, r.ID.Column); sortable &&
			(g == nil || gc.Sortable || c.Name == g.Sort || !anySortable) {
			spec.Sort[c.Name] = ref(c.Name)
		}
		if fk := filterKind(kind); fk != "" {
			want := g != nil && gc.Filterable
			if g == nil {
				want = !b.internalCol(c.Name, r.ID.Column) && (!protectedName(c.Name) || c.Name == "created_at" || c.Name == "updated_at")
			}
			if want {
				if g != nil && kind == validate.String && strings.EqualFold(string(gc.Filter.Type), "select") {
					fk = query.KindEnum
				}
				spec.Filters[c.Name] = query.FilterCol{Col: ref(c.Name), Kind: fk}
			}
		}
		if searchable(c) && !protectedName(c.Name) && (g == nil || gc.Searchable || (g.IsGlobalSearch && !gc.Hide)) {
			searchCols = append(searchCols, c.Name)
		}
	}
	for _, c := range rankSearch(searchCols) {
		if len(spec.Search) >= 8 {
			break
		}
		spec.Search = append(spec.Search, ref(c))
	}
	if len(spec.Filters) == 0 {
		spec.Filters = nil
	}

	idCol := r.ID.Column
	var order string
	switch {
	case g != nil && g.Sort != "" && t.HasColumn(g.Sort) && identRe.MatchString(g.Sort):
		dir := "ASC"
		if strings.EqualFold(g.SortOrder, "desc") {
			dir = "DESC"
		}
		order = ref(g.Sort) + " " + dir
		if g.Sort != idCol {
			order += ", " + ref(idCol) + " " + dir
		}
	case t.HasColumn("created_at"):
		order = ref("created_at") + " DESC, " + ref(idCol) + " DESC"
	default:
		order = ref(idCol) + " ASC"
	}
	spec.DefaultOrder = order
	if g != nil && g.Paging > 0 && g.Paging <= 500 {
		spec.DefaultLimit = g.Paging
	}
	if _, ok := spec.Sort[idCol]; !ok {
		spec.Sort[idCol] = ref(idCol)
	}

	if g != nil {
		b.buildTotals(spec, g)
		if g.IsExcel {
			b.buildExport(r, g)
		}
	}

	// Password талбартай бол hash-ийг сонгохгүй ListQuery.
	if len(b.pwCols) > 0 {
		for _, c := range t.Columns {
			if !b.isPassword(c.Name) {
				b.listSelect = append(b.listSelect, ref(c.Name))
			}
		}
		b.extra.listQuery = true
		// Check() ListQuery-г шаардана; бодит код emit-д гарна (энд зөвхөн placeholder).
		r.ListQuery = func(db *gorm.DB, _ string) *gorm.DB { return db }
		b.todoAt("", CodeFormFeature, strings.Join(b.pwCols, ","), "Password талбар — ListQuery нь hash-ийг SELECT-ээс хассан; шалга")
	}
}

func (b *builder) buildTotals(spec *query.Spec, g *LegacyGrid) {
	for _, a := range g.Aggregations {
		c := b.t.Column(a.Column)
		if c == nil {
			continue
		}
		ki, why := baseKind(*c)
		if why != "" || !numericKind(ki.Kind) {
			b.todoAt("", CodeGrid, a.Column, "columnAggregations: тоон биш/дэмжигдээгүй багана")
			continue
		}
		fn := strings.ToUpper(a.Aggregation)
		switch fn {
		case "SUM", "AVG", "MIN", "MAX", "COUNT":
		default:
			b.todoAt("", CodeGrid, a.Column, fmt.Sprintf("columnAggregations %q дэмжигдээгүй", a.Aggregation))
			continue
		}
		key := a.Column
		if fn != "SUM" {
			key = strings.ToLower(fn) + "_" + a.Column
		}
		if spec.Totals == nil {
			spec.Totals = map[string]string{}
		}
		if fn == "COUNT" {
			spec.Totals[key] = fmt.Sprintf("COUNT(%s.%s)", alias, a.Column)
		} else {
			spec.Totals[key] = fmt.Sprintf("COALESCE(%s(%s.%s),0)", fn, alias, a.Column)
		}
	}
}

// internalCol — sort-д ашиггүй сервер/tenant баганууд (id, created_at, updated_at-ээс бусад хамгаалагдсан).
func (b *builder) internalCol(name, idCol string) bool {
	if name == b.versionCol && name != "" {
		return true
	}
	if name == idCol || name == "created_at" || name == "updated_at" {
		return false
	}
	return protectedName(name)
}

// rankSearch — нэр-төст баганыг түрүүнд (name, title, code …) гаргана; тогтвортой.
func rankSearch(cols []string) []string {
	score := func(c string) int {
		switch {
		case c == "name" || c == "title":
			return 0
		case strings.HasSuffix(c, "_name") || c == "label" || c == "code" || c == "number":
			return 1
		case strings.HasSuffix(c, "_no") || strings.HasSuffix(c, "_number") || strings.HasSuffix(c, "_code"):
			return 2
		}
		return 3
	}
	out := append([]string(nil), cols...)
	sort.SliceStable(out, func(i, j int) bool { return score(out[i]) < score(out[j]) })
	return out
}

func (b *builder) gridTodos(g *LegacyGrid, t *schema.Table, missing []string) {
	if (g.Model != "" && g.Model != t.Name) || g.IsView {
		b.todoAt("", CodeGrid, g.Model, fmt.Sprintf("grid %q (view=%v, mainTable=%q) — List үндсэн хүснэгтээс; view-ийн JOIN баганыг ListQuery-д бич", g.Model, g.IsView, g.MainTable))
	}
	if len(missing) > 0 {
		b.todoAt("", CodeGrid, "", "grid баганууд хүснэгтэд байхгүй (virtual/JOIN): "+strings.Join(missing, ", "))
	}
	if c := strings.TrimSpace(string(g.Condition)); c != "" {
		b.todoAt("", CodeGrid, "", fmt.Sprintf("grid condition %q — ListQuery/DetailQuery-д (org_id нь автоматаар)", c))
	}
	if nonEmptyJSON(g.Formula) || nonEmptyJSON(g.AggFormul) {
		b.todoAt("", CodeFormula, "", "grid formula/columnAggregationsFormula — тооцоолсон багана ListQuery-д")
	}
	if nonEmptyJSON(g.Filter) {
		b.todoAt("", CodeGrid, "", "grid-ийн тогтмол filter[] — List.Filters/ListQuery-д")
	}
	for _, h := range []struct{ n, v string }{
		{"beforeFetch", string(g.Triggers.BeforeFetch)}, {"afterFetch", string(g.Triggers.AfterFetch)},
		{"beforeDelete", string(g.Triggers.BeforeDelete)}, {"afterDelete", string(g.Triggers.AfterDelete)},
		{"beforePrint", string(g.Triggers.BeforePrint)},
	} {
		if strings.TrimSpace(h.v) != "" {
			b.todoAt("", CodeTrigger, "", fmt.Sprintf("grid trigger %s = %s", h.n, h.v))
		}
	}
	if g.IsExcelUpl {
		b.todoAt("", CodeGrid, "", "isExcelUpload (Excel импорт) — jobs/тусгай endpoint")
	}
	if g.IsPivot {
		b.todoAt("", CodeGrid, "", "isPivot — front_end")
	}
}

func (b *builder) buildExport(r *crud.Resource[map[string]any], g *LegacyGrid) {
	ex := &query.ExportSpec{FileName: b.t.Name}
	for _, gc := range g.Columns {
		if gc.Hide || gc.Virtual || !identRe.MatchString(gc.Model) {
			continue
		}
		c := b.t.Column(gc.Model)
		if c == nil {
			continue
		}
		ki, why := baseKind(*c)
		if why != "" || b.isPassword(c.Name) || ki.Kind == validate.JSON {
			continue
		}
		hdr := strings.Join(strings.Fields(string(gc.Label)), " ")
		if hdr == "" {
			hdr = c.Name
		}
		ex.Columns = append(ex.Columns, query.ExportColumn{
			Key: c.Name, Header: hdr, Expr: alias + "." + c.Name,
			Numeric: strings.EqualFold(c.UDT, "numeric"),
		})
	}
	if len(ex.Columns) > 0 {
		r.Export = ex
	}
}
