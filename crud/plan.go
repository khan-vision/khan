package crud

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/khan-vision/khan/lookup"
	"github.com/khan-vision/khan/query"
	"github.com/khan-vision/khan/tenant"
	"github.com/khan-vision/khan/validate"
	"gorm.io/gorm"
)

var (
	identRe = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)
	tableRe = regexp.MustCompile(`^[a-z_][a-z0-9_]*\.[a-z_][a-z0-9_]*$`)
	nameRe  = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)*$`)
)

// Серверээс удирдагддаг багана — Field болж зарлагдах боломжгүй.
// defaultTimeout — нэг хүсэлтийн DB ажлын анхдагч дээд хугацаа.
const defaultTimeout = 30 * time.Second

var protectedCols = []string{"id", "org_id", "created_at", "updated_at", "deleted_at", "created_by", "updated_by"}

const (
	defaultMaxBody = 1 << 20
	defaultMaxRows = 500
	defaultAlias   = "t"
)

// fieldPlan — Field + анхдагч утга бөглөгдсөн, шалгагдсан хувилбар.
type fieldPlan struct {
	Field
	col    string
	label  string
	def    any // normalize хийгдсэн Default
	hasDef bool
	ref    *lookup.Spec // Kind бөглөгдсөн хувилбар
	key    string       // /options/<key>
}

type childPlan struct {
	Child
	label   string
	idCol   string
	order   string
	maxRows int
	fields  []fieldPlan
	gen     func() string
	byName  map[string]*fieldPlan
	pwCols  map[string]bool
}

// engine — Mount-д compile хийгдсэн, өөрчлөгдөхгүй төлөв. Handler бүр үүнийг л уншина.
type engine struct {
	name, label, endpoint, table, alias string

	idCol  string
	idKind IDKind
	gen    func() string

	fields []fieldPlan
	byName map[string]*fieldPlan

	children []childPlan

	list, detail func(db *gorm.DB, orgID string) *gorm.DB
	listSpec     query.Spec
	export       *query.ExportSpec
	bulk         *query.BulkSpec
	newRows      func() any

	can      func(c fiber.Ctx, a Action) bool
	validate func(x *Ctx, v Values) error
	hooks    Hooks

	softDelete    bool
	versionCol    string
	protected     map[string]bool // hook-ийн дараа Values-аас хасагдах баганууд
	timeout       time.Duration
	timestamps    bool
	trackUser     bool
	rls           bool
	events        bool
	strictUnknown bool
	maxBody       int
	exposeMeta    bool

	db  func() *gorm.DB
	org func(c fiber.Ctx) (string, bool)

	decimals []*fieldPlan    // Money/Number талбарууд (хариунд яг decimal-аар)
	pwCols   map[string]bool // Password төрлийн баганууд (хариу/event-ээс хасна)
	known    map[string]bool // StrictUnknown-д зөвшөөрөгдөх body түлхүүрүүд
	refs     map[string]*fieldPlan
}

// Check — тохиргоог Mount хийхгүйгээр шалгана (codegen, тест). DB/Can шаардахгүй.
func (r *Resource[T]) Check() error {
	_, err := r.build(false)
	return err
}

func (r *Resource[T]) build(runtime bool) (*engine, error) {
	e := &engine{
		name: r.Name, label: r.Label, endpoint: r.Endpoint, table: r.Table, alias: r.Alias,
		list: r.ListQuery, detail: r.DetailQuery, listSpec: r.List, export: r.Export, bulk: r.Bulk,
		can: r.Can, validate: r.Validate, hooks: r.Hooks,
		softDelete: r.SoftDelete, versionCol: r.Version, trackUser: r.TrackUser, rls: r.RLS,
		events: r.Events, strictUnknown: r.StrictUnknown, maxBody: r.MaxBody, exposeMeta: r.ExposeMeta, timeout: r.Timeout,
		db: r.DB, org: r.Org, newRows: r.NewRows,
		timestamps: r.Timestamps == nil || *r.Timestamps,
		byName:     map[string]*fieldPlan{},
		pwCols:     map[string]bool{},
		known:      map[string]bool{},
		refs:       map[string]*fieldPlan{},
	}
	fail := func(format string, a ...any) (*engine, error) {
		return nil, fmt.Errorf("khan/crud %s: %s", r.Name, fmt.Sprintf(format, a...))
	}

	if !nameRe.MatchString(r.Name) {
		return fail("Name буруу: %q (жижиг үсэг, тоо, `_`, цэг)", r.Name)
	}
	if e.label == "" {
		e.label = r.Name
	}
	if !tableRe.MatchString(r.Table) {
		return fail("Table буруу: %q (schema.name байх ёстой)", r.Table)
	}
	if e.alias == "" {
		e.alias = defaultAlias
	}
	if !identRe.MatchString(e.alias) {
		return fail("Alias буруу: %q", e.alias)
	}
	if runtime {
		if r.DB == nil {
			return fail("DB заавал")
		}
		if r.Can == nil {
			return fail("Can заавал (fail-closed)")
		}
	}
	if e.org == nil {
		e.org = tenant.Require
	}
	if e.timeout == 0 {
		e.timeout = defaultTimeout
	}
	if e.maxBody <= 0 {
		e.maxBody = defaultMaxBody
	}
	if e.newRows == nil {
		e.newRows = func() any { return &[]T{} }
	}

	var err error
	if e.idCol, e.idKind, e.gen, err = compileID(r.ID); err != nil {
		return fail("ID: %v", err)
	}

	// idCol тодорхой болсны ДАРАА (өмнө нь "t. ASC" болж жагсаалт 500 өгдөг байсан).
	if e.listSpec.DefaultOrder == "" {
		e.listSpec.DefaultOrder = e.alias + "." + e.idCol + " ASC" // тогтвортой хуудаслалт
	}

	protected := map[string]bool{}
	for _, c := range protectedCols {
		protected[c] = true
	}
	protected[e.idCol] = true
	e.protected = protected
	if e.versionCol != "" {
		if !identRe.MatchString(e.versionCol) {
			return fail("Version буруу: %q", e.versionCol)
		}
		protected[e.versionCol] = true
		e.known[e.versionCol] = true
	}

	if e.fields, err = compileFields(r.Fields, protected, true); err != nil {
		return fail("%v", err)
	}
	for i := range e.fields {
		f := &e.fields[i]
		e.byName[f.Name] = f
		e.known[f.Name] = true
		if f.Kind == validate.Password {
			e.pwCols[f.col] = true
		}
		if f.Kind == validate.Money || f.Kind == validate.Number {
			e.decimals = append(e.decimals, f)
		}
		if f.ref != nil {
			f.key = f.Name
			e.refs[f.key] = f
		}
	}
	if len(e.fields) == 0 {
		return fail("Fields хоосон")
	}
	if len(e.pwCols) > 0 && r.ListQuery == nil {
		return fail("Password төрлийн талбартай resource-д ListQuery (hash-ийг сонгохгүй) заавал")
	}

	childNames := map[string]bool{}
	for _, ch := range r.Children {
		cp, err := compileChild(ch, e)
		if err != nil {
			return fail("child %q: %v", ch.Name, err)
		}
		if childNames[cp.Name] || e.byName[cp.Name] != nil {
			return fail("child нэр давхардсан: %q", cp.Name)
		}
		childNames[cp.Name] = true
		e.known[cp.Name] = true
		for i := range cp.fields {
			f := &cp.fields[i]
			if f.Kind == validate.Password {
				cp.pwCols[f.col] = true
			}
			if f.ref != nil {
				f.key = cp.Name + "." + f.Name
				e.refs[f.key] = f
			}
		}
		e.children = append(e.children, cp)
	}

	if e.events && !nameRe.MatchString(e.name) {
		return fail("Events: Name event topic-д тохирохгүй")
	}
	if err := e.checkBulk(); err != nil {
		return fail("%v", err)
	}
	if e.export != nil || e.bulk != nil {
		if err := e.checkQuerySpecs(); err != nil {
			return fail("%v", err)
		}
	}
	return e, nil
}

func compileID(s IDSpec) (col string, kind IDKind, gen func() string, err error) {
	col = s.Column
	if col == "" {
		col = "id"
	}
	if !identRe.MatchString(col) {
		return "", "", nil, fmt.Errorf("Column буруу: %q", col)
	}
	kind = s.Kind
	switch kind {
	case "":
		kind = IDUUID
	case IDUUID, IDInt:
	default:
		return "", "", nil, fmt.Errorf("Kind буруу: %q", kind)
	}
	if kind == IDInt && s.Generate != nil {
		return "", "", nil, errors.New("IDInt-д Generate байж болохгүй (DB default)")
	}
	if kind == IDUUID {
		gen = s.Generate
		if gen == nil {
			gen = newUUID
		}
	}
	return col, kind, gen, nil
}

// compileFields — талбаруудыг шалгаж fieldPlan болгоно. parent=true үед Unique зөвшөөрнө.
func compileFields(fields []Field, protected map[string]bool, allowUnique bool) ([]fieldPlan, error) {
	names := map[string]bool{}
	cols := map[string]bool{}
	out := make([]fieldPlan, 0, len(fields))
	for _, f := range fields {
		if !identRe.MatchString(f.Name) {
			return nil, fmt.Errorf("Field.Name буруу: %q", f.Name)
		}
		col := f.Column
		if col == "" {
			col = f.Name
		}
		if !identRe.MatchString(col) {
			return nil, fmt.Errorf("Field %q: Column буруу: %q", f.Name, col)
		}
		if protected[f.Name] || protected[col] {
			return nil, fmt.Errorf("Field %q: %q хамгаалагдсан нэр/багана (сервер удирдана)", f.Name, col)
		}
		if names[f.Name] {
			return nil, fmt.Errorf("Field.Name давхардсан: %q", f.Name)
		}
		if cols[col] {
			return nil, fmt.Errorf("Field багана давхардсан: %q", col)
		}
		names[f.Name], cols[col] = true, true
		if !f.Kind.Valid() {
			return nil, fmt.Errorf("Field %q: Kind буруу: %q", f.Name, f.Kind)
		}
		if err := f.Rules.Check(f.Kind); err != nil {
			return nil, fmt.Errorf("Field %q: %v", f.Name, err)
		}
		fp := fieldPlan{Field: f, col: col, label: f.Label}
		if fp.label == "" {
			fp.label = f.Name
		}
		if f.Unique {
			if !allowUnique {
				return nil, fmt.Errorf("Field %q: Unique child-д дэмжигдэхгүй", f.Name)
			}
			if f.Kind == validate.Password || f.Kind == validate.JSON {
				return nil, fmt.Errorf("Field %q: %s төрөлд Unique боломжгүй", f.Name, f.Kind)
			}
		}
		if f.Ref != nil {
			spec := *f.Ref
			switch f.Kind {
			case validate.UUID, validate.Int, validate.String:
			default:
				return nil, fmt.Errorf("Field %q: Ref-тэй талбарын Kind UUID/Int/String байх ёстой", f.Name)
			}
			if spec.Kind == "" {
				spec.Kind = f.Kind
			} else if spec.Kind != f.Kind {
				return nil, fmt.Errorf("Field %q: Ref.Kind (%s) ≠ Field.Kind (%s)", f.Name, spec.Kind, f.Kind)
			}
			if err := spec.Validate(); err != nil {
				return nil, fmt.Errorf("Field %q: %v", f.Name, err)
			}
			fp.ref = &spec
		}
		if f.Default != nil {
			if f.Kind == validate.Password {
				return nil, fmt.Errorf("Field %q: Password-д Default боломжгүй", f.Name)
			}
			raw, err := json.Marshal(f.Default)
			if err != nil {
				return nil, fmt.Errorf("Field %q: Default JSON болгож чадсангүй: %v", f.Name, err)
			}
			v, present, msg := validate.Parse(f.Kind, raw, f.Rules, fp.label)
			if msg != "" {
				return nil, fmt.Errorf("Field %q: Default буруу: %s", f.Name, msg)
			}
			if present {
				fp.def, fp.hasDef = v, true
			}
		}
		out = append(out, fp)
	}
	return out, nil
}

func compileChild(ch Child, e *engine) (childPlan, error) {
	cp := childPlan{Child: ch, label: ch.Label, byName: map[string]*fieldPlan{}, pwCols: map[string]bool{}}
	if !identRe.MatchString(ch.Name) {
		return cp, fmt.Errorf("Name буруу: %q", ch.Name)
	}
	if cp.label == "" {
		cp.label = ch.Name
	}
	if !tableRe.MatchString(ch.Table) {
		return cp, fmt.Errorf("Table буруу: %q", ch.Table)
	}
	if !identRe.MatchString(ch.FK) {
		return cp, fmt.Errorf("FK буруу: %q", ch.FK)
	}
	var err error
	if cp.idCol, _, cp.gen, err = compileID(ch.ID); err != nil {
		return cp, fmt.Errorf("ID: %v", err)
	}
	if ch.ID.Kind == IDInt {
		return cp, errors.New("child ID: зөвхөн UUID дэмжигдэнэ")
	}
	cp.order = ch.Order
	if cp.order == "" {
		cp.order = cp.idCol
	}
	if !identRe.MatchString(cp.order) {
		return cp, fmt.Errorf("Order буруу: %q", ch.Order)
	}
	cp.maxRows = ch.MaxRows
	if cp.maxRows <= 0 {
		cp.maxRows = defaultMaxRows
	}
	protected := map[string]bool{ch.FK: true, cp.idCol: true}
	for _, c := range protectedCols {
		protected[c] = true
	}
	if cp.fields, err = compileFields(ch.Fields, protected, false); err != nil {
		return cp, err
	}
	if len(cp.fields) == 0 {
		return cp, errors.New("Fields хоосон")
	}
	for i := range cp.fields {
		if cp.fields[i].Kind == validate.Password {
			// мөр бүрт bcrypt → нэг хүсэлтэд MaxRows удаа (DoS); child-д нууц үг хадгалах шаардлага үгүй.
			return cp, fmt.Errorf("талбар %q: Password төрөл child-д зөвшөөрөгдөхгүй", cp.fields[i].Name)
		}
		cp.byName[cp.fields[i].Name] = &cp.fields[i]
	}
	return cp, nil
}

// checkBulk — Bulk spec-ийг resource-той тааруулж, ref/unique/password баганыг bulk-аар
// засахыг хориглоно (validation тойрох боломж). Mount-д panic биш error.
func (e *engine) checkBulk() error {
	if e.bulk == nil {
		return nil
	}
	b := *e.bulk
	if b.Table == "" {
		b.Table = e.table
	}
	if b.Table != e.table {
		return fmt.Errorf("Bulk.Table (%s) ≠ Table (%s)", b.Table, e.table)
	}
	if b.IDCol != "" && b.IDCol != e.idCol {
		return fmt.Errorf("Bulk.IDCol (%s) ≠ ID.Column (%s)", b.IDCol, e.idCol)
	}
	b.IDCol = e.idCol
	if e.softDelete {
		b.SoftDelete = true
	} else if b.SoftDelete {
		return errors.New("Bulk.SoftDelete=true боловч resource SoftDelete биш")
	}
	b.IntIDs = e.idKind == IDInt
	cols := map[string]*fieldPlan{}
	for i := range e.fields {
		cols[e.fields[i].col] = &e.fields[i]
	}
	keys := make([]string, 0, len(b.Fields))
	for k := range b.Fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		f := b.Fields[k]
		if e.protected[f.Col] && f.Col != e.versionCol {
			return fmt.Errorf("Bulk талбар %q: хамгаалагдсан багана (%s) bulk-аар засагдахгүй", k, f.Col)
		}
		if e.versionCol != "" && f.Col == e.versionCol {
			return fmt.Errorf("Bulk талбар %q: version багана bulk-аар засагдахгүй", k)
		}
		if fp, ok := cols[f.Col]; ok && (fp.ref != nil || fp.Unique || fp.Kind == validate.Password) {
			return fmt.Errorf("Bulk талбар %q: Ref/Unique/Password талбарыг bulk-аар засаж болохгүй (validation тойрно)", k)
		}
	}
	if len(b.Fields) > 0 && (e.hooks.BeforeUpdate != nil || e.hooks.AfterUpdate != nil || e.validate != nil) {
		// Bulk update нь мөр бүрийн Validate/hook-ийг ажиллуулдаггүй — тэдгээрийг тойрох
		// цоорхой болохоос сэргийлж хориглоно (bulk delete нь deleteOne-оор hook ажиллуулна).
		return errors.New("Bulk.Fields: BeforeUpdate/AfterUpdate/Validate-тэй resource-д bulk update зөвшөөрөгдөхгүй")
	}
	e.bulk = &b
	return nil
}

// checkQuerySpecs — query.ExportSpec/BulkSpec.Validate нь panic хийдэг тул error болгоно.
func (e *engine) checkQuerySpecs() (err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("%v", rec)
		}
	}()
	if e.export != nil {
		e.export.Validate()
	}
	if e.bulk != nil {
		e.bulk.Validate()
	}
	return nil
}

// quote — Mount-д regex-ээр шалгагдсан идентификаторыг "..." дотор хийнэ.
func quote(ident string) string { return `"` + strings.ReplaceAll(ident, `"`, `""`) + `"` }
