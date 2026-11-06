package gen

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/khan-vision/khan/crud"
	"github.com/khan-vision/khan/lookup"
	"github.com/khan-vision/khan/schema"
	"github.com/khan-vision/khan/validate"
)

// scope — талбар parent-д үү, child-д уу (child-д Unique/Password боломжгүй).
type scope struct {
	tbl      *schema.Table
	key      func(field string) string // тайлбарын түлхүүр
	child    bool
	fkCol    string          // child-ийн эцэг заасан багана (талбар болохгүй)
	uniq     map[string]bool // org_id-тай composite unique-ийн багана
	globalU  map[string]bool // глобал single-column unique
	protect  map[string]bool
	formName string // todo-д бичигдэх угтвар (child)
}

// Лэмбдагийн дүрмийн regex-үүд (front_end lambdaRules.ts-тэй ижил).
var (
	reRegister = `^[а-яөүёА-ЯӨҮЁ0]{2}[0-9]{8}$`
	reEnglish  = `^[a-zA-Z0-9!@#$%^&*\s*)(+=._,-]+$`
	reCyrillic = `^[а-яөүёА-ЯӨҮЁ0-9!@#$%^&*\s*)(+=.,_-]+$`
	reNumeric  = `^-?[0-9]+(\.[0-9]+)?$`
)

// simpleFormTypes — backend-д нөлөөгүй, тусгай TODO шаардахгүй form элементүүд.
var simpleFormTypes = map[string]bool{
	"": true, "text": true, "textarea": true, "number": true, "select": true, "radio": true, "switch": true,
	"checkbox": true, "date": true, "datetime": true, "time": true, "email": true, "password": true, "money": true,
	"register": true, "image": true, "file": true, "newsimageupload": true, "otherimage": true, "ck": true,
	"colorpicker": true, "subform": true,
}

// inputHints — formType → Field.Input (front_end KvField төрөл). Зөвхөн баталгаатай registry нэрс.
var inputHints = map[string]string{
	"textarea": "textarea", "radio": "radio", "colorpicker": "color", "register": "register",
	"image": "upload", "file": "upload", "newsimageupload": "upload", "otherimage": "upload",
	"ck": "richText", "employeeselector": "employee", "checkbox": "checkbox",
}

// pend — талбарын түр TODO (талбар хасагдвал resource-ийн түвшинд очно).
type pend struct{ code, msg string }

// fieldOut — buildField-ийн үр дүн.
type fieldOut struct {
	f     crud.Field
	notes []string
	ps    []pend
	skip  bool // талбар write allowlist-д ороогүй (шалтгаан ps-д)
}

func (b *builder) cleanLabel(lf *LegacyField, col schema.Column) (label string, fromLegacy bool) {
	if lf != nil {
		l := strings.Join(strings.Fields(string(lf.Label)), " ")
		if l != "" && l != col.Name {
			return l, true
		}
	}
	if c := strings.Join(strings.Fields(col.Comment), " "); c != "" {
		return c, true
	}
	return col.Name, false
}

// buildField — нэг баганаас crud.Field үүсгэнэ (lf = хуучин формын талбар, байхгүй бол nil).
func (b *builder) buildField(sc scope, col schema.Column, lf *LegacyField) fieldOut {
	var o fieldOut
	fail := func(code, msg string) fieldOut {
		o.ps = append(o.ps, pend{code, msg})
		o.skip = true
		return o
	}
	ki, why := baseKind(col)
	if why != "" {
		return fail(CodeColumnType, why+" — талбар хасагдсан, гараар нэм")
	}
	if ki.Note != "" {
		o.notes = append(o.notes, ki.Note)
	}
	f := crud.Field{Name: col.Name, Kind: ki.Kind, Rules: ki.Rules}
	f.Label, _ = b.cleanLabel(lf, col)

	// Nullable / Required (DB-ээс).
	switch {
	case col.Nullable:
		f.Rules.Nullable = true
	case col.Default == nil && !col.Identity:
		f.Rules.Required = true
	}
	if sc.globalU[col.Name] {
		o.notes = append(o.notes, "DB-д глобал unique (org-оор хязгаарлагдаагүй) — давхцвал 409")
	}
	if !sc.child && sc.uniq[col.Name] {
		f.Unique = true
	}

	if lf != nil {
		if lf.FillByUser != "" || lf.HasUserID {
			return fail(CodeFillUser, fmt.Sprintf("fillByUserField=%q — сервер (JWT) тавина; Hooks.BeforeCreate-д x.UserID/x.OrgID-оос ол", lf.FillByUser))
		}
		if b.opts.HiddenMode == HiddenExclude && lf.Hidden {
			return fail(CodeHidden, "hidden талбар — write allowlist-д оруулаагүй (HiddenExclude); хэрэгтэй бол Hooks-оор тавина")
		}
		b.applyFormType(&f, lf, col, &o)
		b.applyRules(&f, lf, col, &o)
		b.applyOptions(&f, lf, col, &o)
		b.applyDefault(&f, lf, col, &o)
		if lf.Trigger != "" {
			o.ps = append(o.ps, pend{CodeFieldTrigger, fmt.Sprintf("талбарын trigger %q — client талын дараалсан дуудлага; native form-д гараар", string(lf.Trigger))})
		}
		if lf.HasEquation && strings.TrimSpace(string(lf.Equations)) != "" {
			o.ps = append(o.ps, pend{CodeFormula, fmt.Sprintf("equation %q — Validate/BeforeCreate hook эсвэл front_end-д", string(lf.Equations))})
		}
		if strings.TrimSpace(string(lf.IfShowHide)) != "" {
			o.ps = append(o.ps, pend{CodeShowHide, fmt.Sprintf("ifshowhide %q — зөвхөн UI (KvField visible)", string(lf.IfShowHide))})
		}
		if lf.Hidden {
			if b.opts.HiddenMode == HiddenCreateOnly {
				f.CreateOnly = true
				o.notes = append(o.notes, "legacy: hidden — зөвхөн create үед (UI контекстээс ирнэ)")
			} else {
				o.notes = append(o.notes, "legacy: hidden")
			}
		}
		if lf.Disabled {
			f.CreateOnly = true
			o.notes = append(o.notes, "legacy: disabled (readonly) — зөвхөн create үед")
		}
	}
	b.applyRef(sc, &f, col, lf, &o)

	// Unique нь JSON/Password-д, мөн child-д боломжгүй.
	if f.Unique && (f.Kind == validate.JSON || f.Kind == validate.Password) {
		f.Unique = false
		o.ps = append(o.ps, pend{CodeRule, "unique " + string(f.Kind) + " төрөлд боломжгүй"})
	}
	if f.Unique && sc.child {
		f.Unique = false
		o.ps = append(o.ps, pend{CodeRule, "unique child талбарт дэмжигдэхгүй — Validate hook-д"})
	}
	if sc.child && f.Kind == validate.Password {
		return fail(CodeSubform, "Password төрөл child-д зөвшөөрөгдөхгүй")
	}
	if err := f.Rules.Check(f.Kind); err != nil {
		return fail(CodeRule, "дүрмийн зөрчил: "+err.Error())
	}
	o.f = f
	return o
}

// applyFormType — formType-аас Kind/Input шийднэ.
func (b *builder) applyFormType(f *crud.Field, lf *LegacyField, col schema.Column, o *fieldOut) {
	ft := strings.ToLower(string(lf.FormType))
	switch ft {
	case "email":
		if textual(f.Kind) && f.Kind != validate.Password {
			f.Kind = validate.Email
		}
	case "password":
		if f.Kind == validate.String || f.Kind == validate.Text {
			f.Kind = validate.Password
			f.Rules.MaxLen = 0
			f.Rules.Nullable = f.Rules.Nullable && col.Nullable
			o.notes = append(o.notes, "Password → bcrypt hash; Password талбартай resource-д ListQuery hash-ийг сонгохгүй байх ёстой")
		}
	case "ck":
		if f.Kind == validate.Text && f.Rules.MaxLen == 0 {
			f.Rules.MaxLen = 100000
			o.notes = append(o.notes, "rich text (CK) — MaxLen 100000")
		}
	case "register":
		if textual(f.Kind) {
			b.pattern(f, reRegister, "Регистрийн дугаар буруу (2 үсэг + 8 орон)")
		}
	case "image", "file", "newsimageupload", "otherimage":
		if lf.File.IsMultiple {
			o.notes = append(o.notes, "legacy: file.isMultiple — олон файлын утга JSON мөр хэлбэрээр хадгалагддаг; Input-ийг шалга")
		}
	}
	if hint, ok := inputHints[ft]; ok {
		switch {
		case ft == "textarea" && f.Kind == validate.Text:
			// Text нь өөрөө textarea
		case ft == "radio" && f.Kind != validate.Enum:
			// options → Enum болсон тохиолдолд л radio
		case ft == "checkbox" && f.Kind != validate.Bool:
		default:
			f.Input = hint
		}
	}
	if !simpleFormTypes[ft] && ft != "employeeselector" {
		o.ps = append(o.ps, pend{CodeCustom, fmt.Sprintf("custom элемент %q — front_end registry editor (Field.Input) сонго; backend утга: %s", string(lf.FormType), col.Type)})
	}
}

func (b *builder) pattern(f *crud.Field, expr, msg string) {
	if re, err := regexp.Compile(expr); err == nil && (f.Kind == validate.String || f.Kind == validate.Text) {
		f.Rules.Pattern, f.Rules.PatternMsg = re, msg
	}
}

// applyRules — хуучин rules[] → Rules/Kind/Unique. Үл мэдэгдэх нь TODO.
func (b *builder) applyRules(f *crud.Field, lf *LegacyField, col schema.Column, o *fieldOut) {
	rules := append([]LegacyRule(nil), lf.Rules...)
	if lf.CustomRegex != "" && lf.CustomMsg != "" {
		rules = append(rules, LegacyRule{Type: "custom", Regex: lf.CustomRegex, Msg: lf.CustomMsg})
	}
	for _, r := range rules {
		msg := strings.TrimSpace(string(r.Msg))
		switch strings.ToLower(r.Type) {
		case "required":
			f.Rules.Required, f.Rules.Nullable = true, false
		case "email":
			if textual(f.Kind) && f.Kind != validate.Password {
				f.Kind = validate.Email
			} else {
				o.ps = append(o.ps, pend{CodeRule, "email дүрэм " + string(f.Kind) + " төрөлд хэрэглэгдэхгүй"})
			}
		case "unique":
			f.Unique = true // child / JSON-ийн хязгаарлалтыг buildField дараа нь шалгана
		case "mongoliannumber", "mongolianmobilenumber", "mobile":
			if f.Kind == validate.String || f.Kind == validate.Text {
				f.Kind = validate.Phone
			} else {
				o.ps = append(o.ps, pend{CodeRule, "mongolianMobileNumber дүрэм " + string(f.Kind) + " төрөлд хэрэглэгдэхгүй"})
			}
		case "number":
			if textual(f.Kind) {
				b.patternOrTodo(f, reNumeric, "Тоон утга оруулна уу", o)
			} // тоон төрөлд өөрөө шалгагдана
		case "register":
			if textual(f.Kind) {
				b.patternOrTodo(f, reRegister, "Регистрийн дугаар буруу (2 үсэг + 8 орон)", o)
			}
		case "englishalphabet":
			b.patternOrTodo(f, reEnglish, firstNonEmpty(msg, "Зөвхөн англи үсэг оруулна уу"), o)
		case "mongoliancyrillic", "cyrillic":
			b.patternOrTodo(f, reCyrillic, firstNonEmpty(msg, "Зөвхөн кирилл үсэг оруулна уу"), o)
		case "min", "max":
			n := atoi(string(r.Val))
			isMin := strings.EqualFold(r.Type, "min")
			switch {
			case f.Kind == validate.String || f.Kind == validate.Text:
				if isMin {
					f.Rules.MinLen = n
				} else if n > 0 {
					f.Rules.MaxLen = n
				}
			case numericKind(f.Kind):
				v := float64(n)
				if isMin {
					f.Rules.Min = &v
				} else {
					f.Rules.Max = &v
				}
			default:
				o.ps = append(o.ps, pend{CodeRule, fmt.Sprintf("%s=%s дүрэм %s төрөлд хэрэглэгдэхгүй", r.Type, r.Val, f.Kind)})
			}
		case "custom":
			expr := jsRegexSource(string(r.Regex))
			if _, err := regexp.Compile(expr); err != nil || expr == "" {
				o.ps = append(o.ps, pend{CodeRule, fmt.Sprintf("custom regex %q Go (RE2)-д хөрвөхгүй — гараар", string(r.Regex))})
			} else {
				b.patternOrTodo(f, expr, firstNonEmpty(msg, "Утга буруу байна"), o)
			}
		default:
			o.ps = append(o.ps, pend{CodeRule, fmt.Sprintf("«%s» дүрэм хөрвүүлэгдээгүй", r.Type)})
		}
	}
}

func (b *builder) patternOrTodo(f *crud.Field, expr, msg string, o *fieldOut) {
	if f.Kind != validate.String && f.Kind != validate.Text {
		o.ps = append(o.ps, pend{CodeRule, fmt.Sprintf("pattern %q нь %s төрөлд хэрэглэгдэхгүй", expr, f.Kind)})
		return
	}
	if f.Rules.Pattern != nil && f.Rules.Pattern.String() != expr {
		o.ps = append(o.ps, pend{CodeRule, fmt.Sprintf("хоёр дахь pattern %q — Rules.Pattern нэг л байна; Validate hook-д", expr)})
		return
	}
	b.pattern(f, expr, msg)
}

// jsRegexSource — `/abc/i` эсвэл `abc` JS regex-ийг RE2 эх болгоно (флаг i → (?i)).
func jsRegexSource(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && s[0] == '/' {
		if end := strings.LastIndex(s, "/"); end > 0 {
			flags := s[end+1:]
			body := s[1:end]
			if strings.Contains(flags, "i") {
				body = "(?i)" + body
			}
			return body
		}
	}
	return s
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// applyOptions — статик Select/Radio options → Enum.
func (b *builder) applyOptions(f *crud.Field, lf *LegacyField, col schema.Column, o *fieldOut) {
	if len(lf.Options) == 0 {
		return
	}
	if f.Kind == validate.Enum && len(col.Enum) > 0 { // DB enum: утгыг catalog-оос авсан; шошгыг тайлбарт
		o.notes = append(o.notes, optionLabels(lf.Options))
		return
	}
	if f.Kind != validate.String && f.Kind != validate.Text && f.Kind != validate.Enum {
		o.notes = append(o.notes, "options: "+optionLabels(lf.Options))
		return
	}
	var vals []string
	seen := map[string]bool{}
	differ := false
	for _, op := range lf.Options {
		v := string(op.Value)
		if v == "" || seen[v] {
			o.ps = append(o.ps, pend{CodeOptions, "options-ийн утга хоосон/давхардсан — Enum болгосонгүй"})
			return
		}
		seen[v] = true
		vals = append(vals, v)
		if string(op.Label) != v {
			differ = true
		}
	}
	f.Kind = validate.Enum
	f.Rules.Enum = vals
	f.Rules.MaxLen, f.Rules.MinLen = 0, 0
	if differ {
		o.notes = append(o.notes, "options: "+optionLabels(lf.Options))
	}
	return
}

func optionLabels(ops []LegacyOption) string {
	parts := make([]string, 0, len(ops))
	for _, op := range ops {
		parts = append(parts, string(op.Value)+"="+string(op.Label))
	}
	return strings.Join(parts, ", ")
}

// applyDefault — формын literal default → Field.Default (validate.Parse-аар шалгана).
func (b *builder) applyDefault(f *crud.Field, lf *LegacyField, col schema.Column, o *fieldOut) {
	d := strings.TrimSpace(string(lf.Default))
	if d == "" || d == "null" || f.Kind == validate.JSON || f.Kind == validate.Password {
		return
	}
	var v any
	switch f.Kind {
	case validate.Int:
		n, err := strconv.ParseInt(d, 10, 64)
		if err != nil {
			o.ps = append(o.ps, pend{CodeDefault, fmt.Sprintf("default %q Int биш", d)})
			return
		}
		v = n
	case validate.Bool:
		switch strings.ToLower(d) {
		case "true", "1":
			v = true
		case "false", "0":
			v = false
		default:
			o.ps = append(o.ps, pend{CodeDefault, fmt.Sprintf("default %q Bool биш", d)})
			return
		}
	default: // string, Money/Number (decimal мөр), Date, UUID, Enum …
		v = d
	}
	raw, _ := json.Marshal(v)
	if _, _, msg := validate.Parse(f.Kind, raw, f.Rules, f.Label); msg != "" {
		o.ps = append(o.ps, pend{CodeDefault, fmt.Sprintf("default %q хүчингүй: %s", d, msg)})
		return
	}
	f.Default = v
}

// ---------------------------------------------------------------------------------------------
// Ref

// fkOf — col дээрх нэг баганын FK (org_id хос нь орсон нийлмэл FK-г ч нэг баганынх гэж үзнэ).
func fkOf(t *schema.Table, col string) (fk schema.ForeignKey, refCol string, ok bool) {
	for _, k := range t.ForeignKeys {
		var cols, refs []string
		for i := range k.Columns {
			if len(k.Columns) > 1 && k.Columns[i] == "org_id" && i < len(k.RefColumns) && k.RefColumns[i] == "org_id" {
				continue
			}
			cols = append(cols, k.Columns[i])
			if i < len(k.RefColumns) {
				refs = append(refs, k.RefColumns[i])
			}
		}
		if len(cols) == 1 && len(refs) == 1 && cols[0] == col {
			return k, refs[0], true
		}
	}
	return fk, "", false
}

// resolveTable — нэр ("schema.name" эсвэл "name")-ээр catalog-оос хүснэгт олно.
func (b *builder) resolveTable(name, preferSchema string) *schema.Table {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	if strings.Contains(name, ".") {
		for i := range b.cat.Tables {
			if b.cat.Tables[i].QualifiedName() == name {
				return &b.cat.Tables[i]
			}
		}
		return nil
	}
	var found *schema.Table
	for i := range b.cat.Tables {
		t := &b.cat.Tables[i]
		if t.Name != name {
			continue
		}
		if t.Schema == preferSchema {
			return t
		}
		if found != nil {
			return nil // давхардсан → таамаглахгүй
		}
		found = t
	}
	return found
}

// guessLabel — lookup Label багануудыг таамаглана. ok=false бол тохирох багана олдсонгүй.
func guessLabel(rt *schema.Table, value string) (cols []string, ok bool) {
	has := func(n string) bool {
		c := rt.Column(n)
		return c != nil && searchable(*c)
	}
	for _, pair := range [][2]string{{"last_name", "first_name"}, {"lastname", "firstname"}} {
		if has(pair[0]) && has(pair[1]) {
			return []string{pair[0], pair[1]}, true
		}
	}
	for _, n := range []string{"name", "title", "label", "full_name", "display_name", "fullname"} {
		if has(n) {
			return []string{n}, true
		}
	}
	for _, c := range rt.Columns {
		if searchable(c) && strings.HasSuffix(c.Name, "_name") {
			return []string{c.Name}, true
		}
	}
	for _, n := range []string{"firstname", "first_name", "code", "number", "no", "register"} {
		if has(n) {
			return []string{n}, true
		}
	}
	for _, c := range rt.Columns {
		if searchable(c) && c.Name != value && !protectedName(c.Name) {
			return []string{c.Name}, true
		}
	}
	return nil, false
}

func protectedName(n string) bool {
	switch n {
	case "id", "org_id", "created_at", "updated_at", "deleted_at", "created_by", "updated_by":
		return true
	}
	return false
}

// applyRef — FK ба/эсвэл хуучин relation-оос Field.Ref үүсгэнэ.
func (b *builder) applyRef(sc scope, f *crud.Field, col schema.Column, lf *LegacyField, o *fieldOut) {
	fk, fkRef, hasFK := fkOf(sc.tbl, col.Name)
	var rel *LegacyRelation
	if lf != nil && lf.Relation != nil && strings.TrimSpace(lf.Relation.Table) != "" {
		rel = lf.Relation
	}
	if !hasFK && rel == nil {
		return
	}
	switch f.Kind {
	case validate.UUID, validate.Int, validate.String, validate.Text:
	default:
		if hasFK || rel != nil {
			o.ps = append(o.ps, pend{CodeRelation, fmt.Sprintf("%s төрөлтэй баганад Ref боломжгүй", f.Kind)})
		}
		return
	}
	if f.Kind == validate.Text {
		o.ps = append(o.ps, pend{CodeRelation, "Text төрөлтэй баганад Ref боломжгүй (UUID/Int/String байх ёстой)"})
		return
	}
	if rel != nil && rel.Multiple {
		o.ps = append(o.ps, pend{CodeRelation, fmt.Sprintf("relation.multiple (олон сонголт, %s) — crud Ref нэг утгатай; гараар", rel.Table)})
		return
	}

	var rt *schema.Table
	value := "id"
	switch {
	case hasFK:
		rt = b.resolveTable(fk.RefSchema+"."+fk.RefTable, "")
		value = fkRef
		if rt == nil { // catalog-д байхгүй (шүүгдсэн) ч FK нь үнэн
			rt = &schema.Table{Schema: fk.RefSchema, Name: fk.RefTable}
		}
		if rel != nil {
			if lt := b.resolveTable(rel.Table, sc.tbl.Schema); lt != nil && lt.QualifiedName() != rt.QualifiedName() {
				o.ps = append(o.ps, pend{CodeRelation, fmt.Sprintf("legacy relation %s ≠ FK %s — FK-г ашиглав", lt.QualifiedName(), rt.QualifiedName())})
				rel = nil
			}
		}
	default:
		rt = b.resolveTable(rel.Table, sc.tbl.Schema)
		if rt == nil {
			o.ps = append(o.ps, pend{CodeRelation, fmt.Sprintf("relation хүснэгт %q catalog-д олдсонгүй (өөр сервис/давхардсан нэр) — Ref нэмсэнгүй", rel.Table)})
			return
		}
		if rel.Key != "" {
			value = rel.Key
		}
		if rt.Column(value) == nil {
			o.ps = append(o.ps, pend{CodeRelation, fmt.Sprintf("relation key %q %s-д алга", value, rt.QualifiedName())})
			return
		}
	}

	spec := &lookup.Spec{Table: rt.QualifiedName()}
	if value != "id" {
		spec.Value = value
	}
	haveCols := len(rt.Columns) > 0
	if haveCols {
		if !rt.HasColumn("org_id") {
			spec.Tenant = lookup.TenantOff()
		} else if oc := rt.Column("org_id"); oc != nil && oc.Nullable {
			o.notes = append(o.notes, "ref "+rt.QualifiedName()+".org_id NULL байж болно — глобал мөрүүд нь org-оор шүүгдэх тул харагдахгүй")
		}
		spec.SoftDelete = rt.HasColumn("deleted_at")
	}

	// Label: -ref-label override > legacy relation.fields > таамаглал.
	switch {
	case len(b.opts.RefLabels[col.Name]) > 0:
		spec.Label = b.opts.RefLabels[col.Name]
	case rel != nil && len(rel.Fields) > 0 && allCols(rt, rel.Fields):
		spec.Label = append([]string(nil), rel.Fields...)
	default:
		if rel != nil && len(rel.Fields) > 0 && haveCols {
			o.ps = append(o.ps, pend{CodeRelation, fmt.Sprintf("legacy relation.fields %v %s-д байхгүй — Label таамагласан", []string(rel.Fields), rt.QualifiedName())})
		}
		if g, ok := guessLabel(rt, value); ok && haveCols {
			spec.Label = g
		} else {
			spec.Label = []string{value}
			o.ps = append(o.ps, pend{CodeRelation, fmt.Sprintf("%s-ийн Label багана таамаглагдсангүй (%s ашиглав) — Spec.Label-ийг зас", rt.QualifiedName(), value)})
		}
	}
	if rel != nil {
		b.relationExtras(spec, rt, rel, lf, o)
	}
	if err := spec.Validate(); err != nil {
		o.ps = append(o.ps, pend{CodeRelation, "lookup.Spec хүчингүй: " + err.Error()})
		return
	}
	f.Ref = spec
}

func allCols(t *schema.Table, cols []string) bool {
	if len(t.Columns) == 0 {
		return false
	}
	for _, c := range cols {
		if t.Column(c) == nil {
			return false
		}
	}
	return true
}

// relationExtras — filter, sort, parent, filterWithUser.
func (b *builder) relationExtras(spec *lookup.Spec, rt *schema.Table, rel *LegacyRelation, lf *LegacyField, o *fieldOut) {
	if w := strings.TrimSpace(rel.Filter); w != "" {
		if err := lookup.ValidateWhere(w); err != nil {
			o.ps = append(o.ps, pend{CodeRelation, fmt.Sprintf("relation.filter %q lookup Where grammar-д таарахгүй — гараар (%v)", w, err)})
		} else {
			spec.Where = w
		}
	}
	for _, u := range rel.FilterWithUser {
		if u.TableField == "org_id" && u.UserField == "org_id" {
			continue // tenant шүүлт өөрөө хийгдэнэ
		}
		o.ps = append(o.ps, pend{CodeRelation, fmt.Sprintf("filterWithUser %s=user.%s — хэрэглэгчээс хамаарсан шүүлт; Spec.Where/hook-д", u.TableField, u.UserField)})
	}
	if sf := rel.SortField; sf != "" && rt.HasColumn(sf) {
		spec.Order = sf
		if strings.EqualFold(rel.SortOrder, "desc") {
			spec.Order += " desc"
		}
	}
	if p := rel.ParentFieldOfTbl; p != "" && rel.ParentFieldOfForm != "" && p != "org_id" && rel.ParentFieldOfForm != lf.Model && rt.HasColumn(p) {
		spec.Parent = p
		o.notes = append(o.notes, fmt.Sprintf("cascade: GET options?parent=<%s>", rel.ParentFieldOfForm))
	}
}
