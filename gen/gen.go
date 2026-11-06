// Package gen нь schema.Catalog (DB-ийн бүтэц) + (заавал биш) хуучин Lambda form/grid spec-ээс
// crud.Resource тодорхойлолтын Go эх код (gofmt хийгдсэн ноорог) үүсгэдэг generator юм.
// P4 шатанд (120 Lambda form + 101 grid → native crud resource) гар ажлыг багасгах зорилготой.
//
//	cat, _ := schema.ReadJSON(f)                       // `khan schema dump`-ийн snapshot
//	form, _ := gen.ParseForm(formFile)                 // main/lambda/schemas/form/<id>.json (заавал биш)
//	res, err := gen.Generate(gen.Options{Catalog: cat, Table: "attendance.lut_period", Form: form})
//	os.WriteFile("lut_period.go", res.Source, 0o644)   // res.Todos = гараар шийдэх зүйлс
//
// Үүссэн файл нь НОРООГ: `TODO(khan-gen)` тайлбаруудыг гараар шийдэж (hook, custom элемент, formula …),
// `crud.Resource.Check()`-ээр шалгагддаг (generator өөрөө үүсгэсэн бүтцээ Check хийж баталгаажуулна).
// Гаралт нь тогтвортой (огноо/random байхгүй): ижил оролт → ижил текст. Гэрээ: docs/GEN.md.
package gen

import (
	"errors"
	"fmt"
	"go/format"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/khan-vision/khan/crud"
	"github.com/khan-vision/khan/schema"
)

// HiddenMode — формд `hidden: true` талбарыг яах вэ.
type HiddenMode string

const (
	// HiddenCreateOnly (анхдагч) — бичигдэх талбар хэвээр, гэхдээ зөвхөн create үед (UI-ийн контекстээс
	// ирдэг project_id, parent_id гэх мэт; дараа нь өөрчлөгдөхгүй).
	HiddenCreateOnly HiddenMode = "createonly"
	// HiddenKeep — энгийн талбар шиг (тайлбартай).
	HiddenKeep HiddenMode = "keep"
	// HiddenExclude — write allowlist-д оруулахгүй (hook-оор тавина) + TODO.
	HiddenExclude HiddenMode = "exclude"
)

// Options — generator-ийн оролт.
type Options struct {
	// Catalog — DB-ийн бүтэц (заавал).
	Catalog *schema.Catalog
	// Table — "schema.name" эсвэл (давхцалгүй бол) "name" (заавал).
	Table string
	// Form / Grid — хуучин Lambda spec (заавал биш). Form өгвөл талбарын дараалал, шошго, дүрэм, relation,
	// subform-ыг түүнээс авч, формд ороогүй баганыг write allowlist-ээс хасна.
	Form *LegacyForm
	Grid *LegacyGrid

	// Name — Go функцийн нэрийн угтвар: <Name>Resource. Хоосон бол хүснэгтийн нэрийн PascalCase.
	Name string
	// Label — Монгол нэр. Хоосон бол хүснэгтийн comment, үгүй бол нэрээс (TODO-той).
	Label string
	// Endpoint — анхдагч "/api/<schema>/<table-kebab>".
	Endpoint string
	// Package — гаралтын package нэр (анхдагч "resources").
	Package string
	// RefLabels — FK талбар (нэр) → lookup Label баганууд (таамаглалыг дарна).
	RefLabels map[string][]string
	// HiddenMode — анхдагч HiddenCreateOnly.
	HiddenMode HiddenMode
	// Force — lint-ийн хаах зөрчил (org_id алга, PK алга/нийлмэл) байсан ч үүсгэнэ (TODO-той).
	Force bool
}

// Todo — гараар шийдэх зүйл (гаралтад `// TODO(khan-gen) [code] ...` болж орно).
type Todo struct {
	Code  string // тогтвортой код (доорх Code* тогтмолууд)
	Field string // холбогдох талбар/багана (хоосон = resource-ийн түвшин)
	Msg   string
}

func (t Todo) String() string {
	if t.Field != "" {
		return fmt.Sprintf("TODO(khan-gen) [%s] %s: %s", t.Code, t.Field, t.Msg)
	}
	return fmt.Sprintf("TODO(khan-gen) [%s] %s", t.Code, t.Msg)
}

// Cosmetic — гараар шийдэх ч гэсэн логикт нөлөөгүй (шошго, UI харагдах байдал) TODO эсэх.
func (t Todo) Cosmetic() bool { return t.Code == CodeLabel || t.Code == CodeShowHide }

// Todo-ийн кодууд.
const (
	CodeLint          = "lint"           // schema.Lint зөрчил (Force-оор үргэлжилсэн)
	CodeLabel         = "label"          // Монгол шошго олдсонгүй
	CodeColumnType    = "column-type"    // дэмжигдээгүй баганын төрөл (талбар хасагдсан)
	CodeColumnMissing = "column-missing" // хуучин формын талбар DB-д байхгүй
	CodeFillUser      = "fill-by-user"   // fillByUserField: сервер (hook) тавина
	CodeHidden        = "hidden"         // hidden талбар хасагдсан (HiddenExclude)
	CodeRule          = "rule"           // хөрвүүлэгдээгүй/буруу дүрэм
	CodeDefault       = "default"        // хөрвүүлэгдээгүй default
	CodeRelation      = "relation"       // relation/FK-г Ref болгож чадсангүй эсвэл хэсэгчлэн
	CodeOptions       = "options"        // статик options-ийг Enum болгож чадсангүй
	CodeCustom        = "custom-element" // custom form элемент (front_end registry editor)
	CodeTrigger       = "trigger"        // form-ийн before/after trigger → Hooks
	CodeFieldTrigger  = "field-trigger"  // талбарын trigger (URL/функц)
	CodeFormula       = "formula"        // форм/талбарын formula, equation
	CodeShowHide      = "show-hide"      // ifshowhide
	CodeFormFeature   = "form-feature"   // extraButtons, step, email, cache_clear_url …
	CodeSubform       = "subform"        // subform → Children хөрвүүлэлтийн асуудал
	CodeGrid          = "grid"           // grid-ийн condition/formula/trigger/view …
	CodeID            = "id"             // ID/PK-ийн асуудал
	CodeVersion       = "version"        // version/created_by зэрэг тусгай баганын асуудал
)

// Result — generator-ийн гаралт.
type Result struct {
	Source   []byte         // gofmt хийгдсэн Go эх
	Table    string         // "schema.name"
	Func     string         // <Name>Resource
	Todos    []Todo         // гараар шийдэх зүйлс (гаралтын TODO тайлбартай нэгэн адил)
	Issues   []schema.Issue // schema.Lint зөрчил
	Fields   int            // гарсан талбарын тоо
	Children int            // гарсан child-ийн тоо
}

// RefusedError — lint-ийн хаах зөрчил байгаа тул үүсгээгүй (Force: true-аар дарна).
type RefusedError struct {
	Table  string
	Issues []schema.Issue
}

func (e *RefusedError) Error() string {
	var parts []string
	for _, i := range e.Issues {
		parts = append(parts, i.String())
	}
	return fmt.Sprintf("gen: %s: crud-д тохирохгүй (org_id/PK) — -force гэж үүсгэж болно: %s", e.Table, strings.Join(parts, "; "))
}

// Resource — Generate-ийн богино хувилбар: зөвхөн эх кодыг буцаана.
func Resource(opts Options) ([]byte, error) {
	r, err := Generate(opts)
	if err != nil {
		return nil, err
	}
	return r.Source, nil
}

// Generate — crud.Resource-ийн Go эх + TODO жагсаалт үүсгэнэ.
func Generate(opts Options) (*Result, error) {
	if opts.Catalog == nil {
		return nil, errors.New("gen: Catalog заавал")
	}
	if opts.Table == "" && opts.Form != nil {
		opts.Table = opts.Form.Model // хуучин формын model (давхцалгүй нэр) → хүснэгт
	}
	if opts.Table == "" {
		return nil, errors.New("gen: Table заавал (эсвэл Form)")
	}
	t, err := opts.Catalog.Table(opts.Table)
	if err != nil {
		return nil, err
	}
	if t.Kind != schema.KindTable {
		return nil, fmt.Errorf("gen: %s нь %s — зөвхөн хүснэгтээс resource үүсгэнэ", t.QualifiedName(), t.Kind)
	}
	if opts.HiddenMode == "" {
		opts.HiddenMode = HiddenCreateOnly
	}
	switch opts.HiddenMode {
	case HiddenCreateOnly, HiddenKeep, HiddenExclude:
	default:
		return nil, fmt.Errorf("gen: HiddenMode %q буруу (createonly|keep|exclude)", opts.HiddenMode)
	}
	if opts.Package == "" {
		opts.Package = "resources"
	}
	if !goIdentRe.MatchString(opts.Package) {
		return nil, fmt.Errorf("gen: Package %q буруу", opts.Package)
	}
	fn := opts.Name
	if fn == "" {
		fn = pascal(t.Name)
	}
	fn = strings.TrimSuffix(fn, "Resource")
	if !goIdentRe.MatchString(fn) || !unicode.IsUpper(rune(fn[0])) {
		return nil, fmt.Errorf("gen: Name %q Go-ийн экспортлогдох нэр биш (ж: Room) — -name өг", opts.Name)
	}

	b := &builder{opts: opts, cat: opts.Catalog, t: t, form: opts.Form, grid: opts.Grid, comments: map[string][]string{}}
	b.issues = schema.Lint(*t)
	if blocking := blockingIssues(b.issues); len(blocking) > 0 && !opts.Force {
		return nil, &RefusedError{Table: t.QualifiedName(), Issues: blocking}
	}
	for _, i := range blockingIssues(b.issues) {
		b.todoAt("", CodeLint, i.Column, i.Msg+" — Force-оор үүсгэсэн, crud ажиллахын тулд засах ёстой")
	}

	res, err := b.build()
	if err != nil {
		return nil, err
	}
	if err := res.Check(); err != nil {
		return nil, fmt.Errorf("gen: %s: үүссэн тохиргоо crud.Check-ээс унав (generator-ийн алдаа эсвэл catalog): %w", t.QualifiedName(), err)
	}
	src, err := b.emit(fn, res)
	if err != nil {
		return nil, err
	}
	out, err := format.Source(src)
	if err != nil {
		return nil, fmt.Errorf("gen: gofmt: %w\n%s", err, src)
	}
	return &Result{
		Source: out, Table: t.QualifiedName(), Func: fn + "Resource", Todos: b.todos, Issues: b.issues,
		Fields: len(res.Fields), Children: len(res.Children),
	}, nil
}

// blockingIssues — crud-ийг ажиллуулахгүй зөрчлүүд (org_id, PK). Timestamps/money нь мэдээллийн шинжтэй.
func blockingIssues(all []schema.Issue) []schema.Issue {
	var out []schema.Issue
	for _, i := range all {
		if i.Rule == schema.RuleOrgID || i.Rule == schema.RulePrimaryKey {
			out = append(out, i)
		}
	}
	return out
}

var goIdentRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func pascal(s string) string {
	var sb strings.Builder
	for _, p := range regexp.MustCompile(`[^A-Za-z0-9]+`).Split(s, -1) {
		if p == "" {
			continue
		}
		sb.WriteString(strings.ToUpper(p[:1]) + p[1:])
	}
	out := sb.String()
	if out != "" && unicode.IsDigit(rune(out[0])) {
		out = "T" + out
	}
	return out
}

// builder — нэг Generate дуудлагын төлөв.
type builder struct {
	opts   Options
	cat    *schema.Catalog
	t      *schema.Table
	form   *LegacyForm
	grid   *LegacyGrid
	issues []schema.Issue

	todos    []Todo
	comments map[string][]string // түлхүүр → тайлбарын мөрүүд ("" = resource, "f" = талбар, "child:x" / "x.f")

	versionCol string   // optimistic lock багана (жагсаалтын sort/filter-д орохгүй)
	listSelect []string // ListQuery-ийн SELECT (хоосон бол ListQuery үгүй)
	pwCols     []string
	extra      emitExtra
}

// emitExtra — crud.Resource-д шууд багтдаггүй, гэхдээ эх кодод орох зүйлс.
type emitExtra struct {
	listQuery  bool
	listTable  string
	timestamps bool // false үед Timestamps: crud.Off()
	version    string
	trackUser  bool
	softDelete bool
	idKind     crud.IDKind
	idCol      string
}

func (b *builder) todoAt(key, code, field, msg string) {
	msg = oneLine(msg)
	t := Todo{Code: code, Field: field, Msg: msg}
	b.todos = append(b.todos, t)
	b.comments[key] = append(b.comments[key], t.String())
}

func (b *builder) note(key, msg string) {
	b.comments[key] = append(b.comments[key], oneLine(msg))
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(s, "*/", "* /")), " ")
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
