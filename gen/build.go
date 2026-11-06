package gen

import (
	"errors"
	"fmt"
	"strings"

	"github.com/khan-vision/khan/crud"
	"github.com/khan-vision/khan/schema"
	"github.com/khan-vision/khan/validate"
)

var protectedNames = []string{"id", "org_id", "created_at", "updated_at", "deleted_at", "created_by", "updated_by"}

// pkWithoutOrg — PK-аас org_id-г хасна ((org_id,id) нийлмэл PK → id).
func pkWithoutOrg(t *schema.Table) []string {
	pk := t.PrimaryKey
	if len(pk) > 1 {
		var out []string
		for _, c := range pk {
			if c != "org_id" {
				out = append(out, c)
			}
		}
		return out
	}
	return pk
}

// resolveID — ID багана ба төрөл.
func (b *builder) resolveID(t *schema.Table, key string) (col string, kind crud.IDKind, err error) {
	pk := pkWithoutOrg(t)
	if len(pk) != 1 || pk[0] == "org_id" {
		if c := t.Column("id"); c != nil && b.opts.Force {
			pk = []string{"id"}
			b.todoAt(key, CodeID, "id", fmt.Sprintf("PK %v ганц баганатай биш — `id` баганыг ID гэж таамаглав", t.PrimaryKey))
		} else {
			return "", "", fmt.Errorf("gen: %s: PK %v — crud ганц багана (эсвэл org_id + нэг багана) шаарддаг", t.QualifiedName(), t.PrimaryKey)
		}
	}
	c := t.Column(pk[0])
	if c == nil {
		return "", "", fmt.Errorf("gen: %s: PK багана %q алга", t.QualifiedName(), pk[0])
	}
	switch strings.ToLower(c.UDT) {
	case "uuid":
		kind = crud.IDUUID
	case "int2", "int4", "int8", "serial", "serial4", "bigserial", "serial8":
		kind = crud.IDInt
		if c.Default == nil && !c.Identity {
			b.todoAt(key, CodeID, c.Name, "int PK-д DB default/identity алга — INSERT ... RETURNING ажиллахгүй (sequence нэм)")
		}
	default:
		return "", "", fmt.Errorf("gen: %s: PK %q төрөл %s — crud зөвхөн uuid/int ID дэмждэг", t.QualifiedName(), c.Name, c.Type)
	}
	return c.Name, kind, nil
}

// uniqueSets — org_id-тай composite unique (org_id,col) → col; глобал single-column unique.
func uniqueSets(t *schema.Table) (scoped, global map[string]bool) {
	scoped, global = map[string]bool{}, map[string]bool{}
	for _, u := range t.Uniques {
		switch {
		case len(u) == 2 && u[0] == "org_id":
			scoped[u[1]] = true
		case len(u) == 2 && u[1] == "org_id":
			scoped[u[0]] = true
		case len(u) == 1 && u[0] != "org_id":
			global[u[0]] = true
		}
	}
	return
}

func humanize(s string) string {
	s = strings.ReplaceAll(s, "_", " ")
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// build — crud.Resource-ийг (талбар, жагсаалт, child …) бүрдүүлнэ.
func (b *builder) build() (*crud.Resource[map[string]any], error) {
	t := b.t
	r := &crud.Resource[map[string]any]{Name: t.QualifiedName(), Table: t.QualifiedName()}

	idCol, idKind, err := b.resolveID(t, "")
	if err != nil {
		return nil, err
	}
	r.ID = crud.IDSpec{Column: idCol, Kind: idKind}
	b.extra.idCol, b.extra.idKind = idCol, idKind
	if len(t.PrimaryKey) == 2 && pkWithoutOrg(t)[0] == idCol {
		b.note("", fmt.Sprintf("PK (%s) нийлмэл — org_id хасч %s-ийг ID болгов", strings.Join(t.PrimaryKey, ","), idCol))
	}

	protect := map[string]bool{idCol: true}
	for _, n := range protectedNames {
		protect[n] = true
	}

	// Тусгай баганууд.
	if vc := t.Column("version"); vc != nil {
		switch strings.ToLower(vc.UDT) {
		case "int2", "int4", "int8":
			r.Version, b.versionCol = "version", "version"
			protect["version"] = true
		default:
			b.todoAt("", CodeVersion, "version", "version багана int биш ("+vc.Type+") — optimistic lock-д ашиглагдахгүй")
		}
	}
	hasCreated, hasUpdated := t.HasColumn("created_at"), t.HasColumn("updated_at")
	if !(hasCreated && hasUpdated) {
		r.Timestamps = crud.Off()
		b.note("", "created_at/updated_at бүрэн биш тул Timestamps: crud.Off()")
	}
	cb, ub := t.HasColumn("created_by"), t.HasColumn("updated_by")
	switch {
	case cb && ub:
		r.TrackUser = true
	case cb || ub:
		b.todoAt("", CodeVersion, "created_by/updated_by", "зөвхөн нэг нь байна — TrackUser: true хоёуланг шаарддаг; багана нэм")
	}
	r.SoftDelete = t.HasColumn("deleted_at")

	scoped, global := uniqueSets(t)
	sc := scope{tbl: t, key: func(f string) string { return f }, uniq: scoped, globalU: global, protect: protect}

	// Талбарууд.
	type cand struct {
		col schema.Column
		lf  *LegacyField
	}
	var cands []cand
	var notInForm []string
	if b.form != nil {
		inForm := map[string]bool{}
		for _, m := range b.form.Order {
			lf := b.form.Field(m)
			if lf == nil || lf.IsSubForm() {
				continue
			}
			inForm[m] = true
			col := t.Column(m)
			switch {
			case protect[m]: // id/org_id/timestamps/version — сервер удирдана (дуугүй)
			case col == nil:
				b.todoAt("", CodeColumnMissing, m, fmt.Sprintf("хуучин формын талбар %s-д байхгүй (%s) — virtual/өөр хүснэгтийн талбар уу?", t.QualifiedName(), string(lf.FormType)))
			default:
				cands = append(cands, cand{*col, lf})
			}
		}
		for _, c := range t.Columns {
			if !protect[c.Name] && !inForm[c.Name] {
				notInForm = append(notInForm, c.Name)
			}
		}
		if len(notInForm) > 0 {
			b.note("", "Формд ороогүй багана (write allowlist-д ороогүй): "+strings.Join(notInForm, ", "))
		}
	} else {
		for _, c := range t.Columns {
			if !protect[c.Name] {
				cands = append(cands, cand{c, nil})
			}
		}
	}

	labelMiss := []string{}
	for _, c := range cands {
		if c.col.Generated || (c.col.Identity && !c.col.PK) {
			b.note("", fmt.Sprintf("%s: generated/identity багана — бичигдэхгүй", c.col.Name))
			continue
		}
		out := b.buildField(sc, c.col, c.lf)
		if out.skip {
			for _, p := range out.ps {
				b.todoAt("", p.code, c.col.Name, p.msg)
			}
			continue
		}
		if _, ok := b.cleanLabel(c.lf, c.col); !ok && !(c.lf != nil && c.lf.Hidden) {
			labelMiss = append(labelMiss, c.col.Name)
		}
		for _, p := range out.ps {
			b.todoAt(c.col.Name, p.code, c.col.Name, p.msg)
		}
		for _, n := range out.notes {
			b.note(c.col.Name, n)
		}
		if out.f.Kind == validate.Password {
			b.pwCols = append(b.pwCols, c.col.Name)
		}
		r.Fields = append(r.Fields, out.f)
	}
	if len(r.Fields) == 0 {
		msg := "бүх багана хамгаалагдсан/дэмжигдээгүй"
		if b.form != nil {
			msg = fmt.Sprintf("формын %d талбараас ямар ч нь хүснэгтэд таарсангүй — legacy spec хуучирсан байж болно", len(b.form.Order))
		}
		return nil, errors.New("gen: " + t.QualifiedName() + ": бичигдэх талбар үлдсэнгүй (" + msg + ")")
	}
	if len(labelMiss) > 0 {
		b.todoAt("", CodeLabel, "", fmt.Sprintf("Монгол шошго олдсонгүй (баганын нэр хэвээр): %s", strings.Join(labelMiss, ", ")))
	}

	b.buildChildren(r, sc)
	b.buildList(r)
	b.formTodos()

	// Resource мета.
	r.Label = b.opts.Label
	if r.Label == "" {
		if c := strings.Join(strings.Fields(t.Comment), " "); c != "" {
			r.Label = c
		} else {
			r.Label = humanize(t.Name)
			b.todoAt("", CodeLabel, "", "Resource Label-ийг Монголоор бич")
		}
	}
	r.Endpoint = b.opts.Endpoint
	if r.Endpoint == "" {
		r.Endpoint = "/api/" + t.Schema + "/" + strings.ReplaceAll(t.Name, "_", "-")
	}
	b.extra.timestamps = r.Timestamps == nil
	b.extra.softDelete = r.SoftDelete
	return r, nil
}

// buildChildren — формын SubForm-уудыг Children болгоно.
func (b *builder) buildChildren(r *crud.Resource[map[string]any], parent scope) {
	if b.form == nil {
		return
	}
	for i := range b.form.Fields {
		lf := &b.form.Fields[i]
		if !lf.IsSubForm() {
			continue
		}
		b.buildChild(r, parent, lf)
	}
}

func (b *builder) buildChild(r *crud.Resource[map[string]any], parent scope, lf *LegacyField) {
	skip := func(msg string) { b.todoAt("", CodeSubform, lf.Model, "subform → Children болгоогүй: "+msg) }
	ct := b.resolveTable(lf.Model, b.t.Schema)
	if ct == nil {
		skip(fmt.Sprintf("child хүснэгт %q catalog-д олдсонгүй/давхардсан", lf.Model))
		return
	}
	if ct.Kind != schema.KindTable || !ct.HasColumn("org_id") {
		skip(ct.QualifiedName() + ": org_id-тай хүснэгт биш (crud child org_id шаарддаг)")
		return
	}
	if lf.Parent == "" || !ct.HasColumn(lf.Parent) {
		skip(fmt.Sprintf("FK багана %q %s-д алга", lf.Parent, ct.QualifiedName()))
		return
	}
	idCol := ""
	if pk := pkWithoutOrg(ct); len(pk) == 1 && pk[0] != "org_id" {
		idCol = pk[0]
	}
	if idCol == "" {
		skip(ct.QualifiedName() + ": PK ганц баганатай биш")
		return
	}
	if ic := ct.Column(idCol); ic == nil || !strings.EqualFold(ic.UDT, "uuid") {
		skip(ct.QualifiedName() + ": child ID зөвхөн uuid байж болно")
		return
	}
	if r.Timestamps == nil && !(ct.HasColumn("created_at") && ct.HasColumn("updated_at")) {
		skip(ct.QualifiedName() + ": created_at/updated_at алга (crud child-д ч бичдэг)")
		return
	}

	name := lf.Model
	if i := strings.LastIndex(name, "."); i >= 0 {
		name = name[i+1:]
	}
	for _, f := range r.Fields {
		if f.Name == name {
			name += "_rows"
			break
		}
	}
	key := "child:" + name
	protect := map[string]bool{idCol: true, lf.Parent: true}
	for _, n := range protectedNames {
		protect[n] = true
	}
	csc := scope{tbl: ct, child: true, fkCol: lf.Parent, protect: protect, key: func(f string) string { return name + "." + f }}

	ch := crud.Child{
		Name: name, Label: firstNonEmpty(strings.Join(strings.Fields(string(lf.Name)), " "), humanize(name)),
		Table: ct.QualifiedName(), FK: lf.Parent, ID: crud.IDSpec{Column: idCol, Kind: crud.IDUUID},
		SoftDelete: ct.HasColumn("deleted_at"),
	}
	if ct.HasColumn("sort_order") {
		ch.Order = "sort_order"
	}

	var cols []schema.Column
	if len(lf.SubSchema) > 0 {
		for _, sf := range lf.SubSchema {
			col := ct.Column(sf.Model)
			switch {
			case protect[sf.Model]:
			case col == nil:
				b.todoAt(key, CodeColumnMissing, sf.Model, fmt.Sprintf("subform талбар %s-д байхгүй", ct.QualifiedName()))
			default:
				cols = append(cols, *col)
			}
		}
	} else {
		for _, c := range ct.Columns {
			if !protect[c.Name] {
				cols = append(cols, c)
			}
		}
		b.todoAt(key, CodeSubform, lf.Model, "subform-ийн талбарын жагсаалт формд алга (formId дэд форм) — бүх баганыг оруулав, шалга")
	}
	sub := map[string]*LegacyField{}
	for i := range lf.SubSchema {
		sub[lf.SubSchema[i].Model] = &lf.SubSchema[i]
	}
	seenCol := map[string]bool{}
	for _, c := range cols {
		if c.Generated || (c.Identity && !c.PK) || seenCol[c.Name] {
			continue // generated, эсвэл subform schema-д давхардсан талбар
		}
		seenCol[c.Name] = true
		out := b.buildField(csc, c, sub[c.Name])
		for _, p := range out.ps {
			if out.skip {
				b.todoAt(key, p.code, c.Name, p.msg)
			} else {
				b.todoAt(name+"."+c.Name, p.code, c.Name, p.msg)
			}
		}
		if out.skip {
			continue
		}
		for _, n := range out.notes {
			b.note(name+"."+c.Name, n)
		}
		ch.Fields = append(ch.Fields, out.f)
	}
	if len(ch.Fields) == 0 {
		skip(ct.QualifiedName() + ": бичигдэх талбар үлдсэнгүй")
		return
	}
	var feats []string
	for _, x := range []struct {
		on  bool
		msg string
	}{
		{lf.CheckEmpty, "checkEmpty (хоосон бол алдаа) — Validate hook"},
		{lf.AddFromGrid, "addFromGrid (эх grid-ээс сонгож нэмэх)"},
		{lf.UseTableType, "useTableType (тогтмол төрлийн багана)"},
		{lf.DisableCrt || lf.DisableEdt || lf.DisableDel, "disableCreate/Edit/Delete — UI хязгаарлалт (Can-д биш)"},
	} {
		if x.on {
			feats = append(feats, x.msg)
		}
	}
	if len(feats) > 0 {
		b.todoAt(key, CodeSubform, lf.Model, strings.Join(feats, "; "))
	}
	r.Children = append(r.Children, ch)
}

// formTodos — формын түвшний TODO (trigger, formula, ...).
func (b *builder) formTodos() {
	f := b.form
	if f == nil {
		return
	}
	ns := strings.TrimSpace(f.Triggers.Namespace)
	for _, h := range []struct{ name, hook, fn string }{
		{"insert.before", "Hooks.BeforeCreate", string(f.Triggers.Insert.Before)},
		{"insert.after", "Hooks.AfterCreate", string(f.Triggers.Insert.After)},
		{"update.before", "Hooks.BeforeUpdate", string(f.Triggers.Update.Before)},
		{"update.after", "Hooks.AfterUpdate", string(f.Triggers.Update.After)},
	} {
		if strings.TrimSpace(h.fn) == "" {
			continue
		}
		msg := fmt.Sprintf("legacy %s = %s", h.name, h.fn)
		if ns != "" {
			msg += " (namespace " + ns + ")"
		}
		b.todoAt("", CodeTrigger, "", msg+" → "+h.hook)
	}
	if nonEmptyRaw(f.Triggers.Email) {
		b.todoAt("", CodeFormFeature, "", "triggers.email (мэйл илгээх тохиргоо) — AfterCreate/AfterUpdate hook эсвэл jobs")
	}
	if strings.TrimSpace(string(f.Triggers.CacheClearURL)) != "" {
		b.todoAt("", CodeFormFeature, "", "triggers.cache_clear_url — crud.Cache/bump ашигла")
	}
	for _, fm := range f.Formula {
		var targets []string
		for _, t := range fm.Targets {
			targets = append(targets, t.Field+"."+t.Prop)
		}
		code, hint := CodeFormula, "тооцоолсон утга — Validate/Before* hook эсвэл front_end"
		if uiOnlyFormula(fm) {
			code, hint = CodeShowHide, "зөвхөн UI (харагдах/идэвхтэй/заавал) — KvField-д; backend-д нөлөөгүй"
		}
		b.todoAt("", code, "", fmt.Sprintf("formula %s <- %s — %s", strings.Join(targets, ","), string(fm.Template), hint))
	}
	if f.HasExtraButtons {
		b.todoAt("", CodeFormFeature, "", "extraButtons (нэмэлт товч) — front_end/custom endpoint")
	}
	if f.HasSteps {
		b.todoAt("", CodeFormFeature, "", "step (workflow алхам/эрх) — тусгай логик")
	}
}

func nonEmptyRaw(b []byte) bool { return nonEmptyJSON(b) }

// uiOnlyFormula — бүх target нь UI төлөв (hidden/disabled/required …) бол true.
func uiOnlyFormula(f LegacyFormula) bool {
	if len(f.Targets) == 0 {
		return false
	}
	for _, t := range f.Targets {
		switch strings.ToLower(t.Prop) {
		case "hidden", "disabled", "readonly", "required", "visible":
		default:
			return false
		}
	}
	return true
}
