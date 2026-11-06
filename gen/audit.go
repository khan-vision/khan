package gen

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/khan-vision/khan/schema"
)

// AuditStatus — нэг формын үр дүн.
type AuditStatus string

const (
	StatusClean   AuditStatus = "clean"   // TODO-гүй
	StatusMinor   AuditStatus = "minor"   // зөвхөн cosmetic TODO (шошго, UI харагдах байдал)
	StatusTodo    AuditStatus = "todo"    // логикт нөлөөтэй TODO-той (hook, relation, custom элемент …)
	StatusRefused AuditStatus = "refused" // lint (org_id/PK)-ийн улмаас татгалзсан
	StatusError   AuditStatus = "error"   // үүсгэж чадсангүй (хүснэгт олдсонгүй …)
)

// AuditOptions — олон формыг нэг дор (dry-run) шалгах тохиргоо.
type AuditOptions struct {
	Catalog *schema.Catalog
	// FormsDir — `<id>.json` form spec-үүдийн хавтас (main/lambda/schemas/form).
	FormsDir string
	// ModelsDir — Lambda-ийн formModels (`<Name><id>.go`): form id → `TableName()` ("schema.table").
	// Хоосон бол формын bare `model`-ыг catalog-оос хайна.
	ModelsDir string
	// GridsDir — grid spec-үүд; формын model-той mainTable/model таарсныг холбоно (заавал биш).
	GridsDir string
	Force    bool
	// OutDir — өгвөл үүссэн эх кодыг `<id>_<table>.go` болгон бичнэ (нэг package; функцийн нэр давхцвал form id залгана).
	OutDir  string
	Package string
}

// AuditItem — нэг формын дүн.
type AuditItem struct {
	Form     string
	Model    string
	Table    string
	Grid     string
	Status   AuditStatus
	Err      string
	Todos    []Todo
	Fields   int
	Children int
}

// AuditReport — бүх формын дүн.
type AuditReport struct{ Items []AuditItem }

var tableNameRe = regexp.MustCompile(`TableName\(\)\s*string\s*\{\s*return\s*"([^"]+)"`)

// ModelTables — formModels хавтаснаас form id → "schema.table".
func ModelTables(dir string) (map[string]string, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	idRe := regexp.MustCompile(`^[A-Za-z_]+?([0-9]+)\.go$`)
	out := map[string]string{}
	for _, e := range ents {
		m := idRe.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		src, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		if t := tableNameRe.FindSubmatch(src); t != nil {
			out[m[1]] = string(t[1])
		}
	}
	return out, nil
}

// Audit — FormsDir доторх бүх формыг generate хийж (файл бичихгүй) дүн гаргана.
func Audit(opts AuditOptions) (*AuditReport, error) {
	if opts.Catalog == nil {
		return nil, errors.New("gen: Catalog заавал")
	}
	ents, err := filepath.Glob(filepath.Join(opts.FormsDir, "*.json"))
	if err != nil {
		return nil, err
	}
	sort.Strings(ents)
	if len(ents) == 0 {
		return nil, fmt.Errorf("gen: %s-д *.json алга", opts.FormsDir)
	}
	var models map[string]string
	if opts.ModelsDir != "" {
		if models, err = ModelTables(opts.ModelsDir); err != nil {
			return nil, err
		}
	}
	grids, err := loadGrids(opts.GridsDir)
	if err != nil {
		return nil, err
	}

	rep := &AuditReport{}
	used := map[string]bool{}
	if opts.OutDir != "" {
		if err := os.MkdirAll(opts.OutDir, 0o755); err != nil {
			return nil, err
		}
	}
	for _, path := range ents {
		id := strings.TrimSuffix(filepath.Base(path), ".json")
		it := AuditItem{Form: id}
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		form, perr := ParseForm(f)
		f.Close()
		if perr != nil {
			it.Status, it.Err = StatusError, perr.Error()
			rep.Items = append(rep.Items, it)
			continue
		}
		it.Model = form.Model
		table := form.Model
		if q := models[id]; q != "" {
			if bare := q[strings.LastIndex(q, ".")+1:]; bare == form.Model {
				table = q
			}
		}
		it.Table = table
		var grid *LegacyGrid
		if g := grids.match(form.Model); g != nil {
			grid, it.Grid = g.grid, g.id
		}
		gopts := Options{Catalog: opts.Catalog, Table: table, Form: form, Grid: grid, Force: opts.Force, Package: opts.Package}
		if opts.OutDir != "" {
			gopts.Name = pascal(table[strings.LastIndex(table, ".")+1:])
			if used[gopts.Name] {
				gopts.Name += "F" + id
			}
		}
		res, err := Generate(gopts)
		var refused *RefusedError
		switch {
		case errors.As(err, &refused):
			it.Status, it.Err = StatusRefused, refused.Error()
		case err != nil:
			it.Status, it.Err = StatusError, err.Error()
		default:
			it.Table, it.Todos, it.Fields, it.Children = res.Table, res.Todos, res.Fields, res.Children
			it.Status = StatusClean
			for _, t := range res.Todos {
				if !t.Cosmetic() {
					it.Status = StatusTodo
					break
				}
				it.Status = StatusMinor
			}
			if opts.OutDir != "" {
				used[strings.TrimSuffix(res.Func, "Resource")] = true
				name := fmt.Sprintf("%s_%s.go", id, strings.ReplaceAll(res.Table, ".", "_"))
				if err := os.WriteFile(filepath.Join(opts.OutDir, name), res.Source, 0o644); err != nil {
					return nil, err
				}
			}
		}
		rep.Items = append(rep.Items, it)
	}
	return rep, nil
}

type gridEntry struct {
	id   string
	grid *LegacyGrid
}
type gridIndex map[string]*gridEntry

func (g gridIndex) match(model string) *gridEntry { return g[model] }

func loadGrids(dir string) (gridIndex, error) {
	idx := gridIndex{}
	if dir == "" {
		return idx, nil
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	var all []*gridEntry
	for _, p := range files {
		f, err := os.Open(p)
		if err != nil {
			return nil, err
		}
		g, perr := ParseGrid(f)
		f.Close()
		if perr != nil {
			continue
		}
		all = append(all, &gridEntry{id: strings.TrimSuffix(filepath.Base(p), ".json"), grid: g})
	}
	// Эхлээд хүснэгтийн өөрийнх нь grid (model), дараа нь view-ийн grid (mainTable).
	for _, e := range all {
		if _, ok := idx[e.grid.Model]; !ok {
			idx[e.grid.Model] = e
		}
	}
	for _, e := range all {
		if mt := e.grid.MainTable; mt != "" {
			if _, ok := idx[mt]; !ok {
				idx[mt] = e
			}
		}
	}
	return idx, nil
}

// Counts — статус тус бүрийн тоо.
func (r *AuditReport) Counts() map[AuditStatus]int {
	out := map[AuditStatus]int{}
	for _, it := range r.Items {
		out[it.Status]++
	}
	return out
}

// CodeStat — todo кодын статистик.
type CodeStat struct {
	Code        string
	Occurrences int
	Forms       int
}

// TopTodos — todo кодоор (давтамжаар буурах).
func (r *AuditReport) TopTodos() []CodeStat {
	occ, forms := map[string]int{}, map[string]int{}
	for _, it := range r.Items {
		seen := map[string]bool{}
		for _, t := range it.Todos {
			occ[t.Code]++
			if !seen[t.Code] {
				seen[t.Code] = true
				forms[t.Code]++
			}
		}
	}
	var out []CodeStat
	for c, n := range occ {
		out = append(out, CodeStat{c, n, forms[c]})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Forms != out[j].Forms {
			return out[i].Forms > out[j].Forms
		}
		return out[i].Code < out[j].Code
	})
	return out
}

// ErrorReasons — refused/error шалтгаанууд (давтамжаар).
func (r *AuditReport) ErrorReasons() []CodeStat {
	m := map[string]int{}
	for _, it := range r.Items {
		if it.Status != StatusRefused && it.Status != StatusError {
			continue
		}
		m[errReason(it)]++
	}
	var out []CodeStat
	for c, n := range m {
		out = append(out, CodeStat{Code: c, Occurrences: n, Forms: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Forms != out[j].Forms {
			return out[i].Forms > out[j].Forms
		}
		return out[i].Code < out[j].Code
	})
	return out
}

func errReason(it AuditItem) string {
	e := it.Err
	switch {
	case it.Status == StatusRefused:
		switch {
		case strings.Contains(e, "org_id"):
			return "refused: org_id алга/буруу"
		case strings.Contains(e, "primary key"):
			return "refused: PK алга"
		}
		return "refused"
	case strings.Contains(e, "хүснэгт олдсонгүй"):
		return "хүснэгт catalog-д олдсонгүй"
	case strings.Contains(e, "олон schema-д"):
		return "bare нэр олон schema-д давхцсан"
	case strings.Contains(e, "PK"):
		return "PK нийлмэл/дэмжигдээгүй"
	case strings.Contains(e, "бичигдэх талбар үлдсэнгүй"):
		return "бичигдэх талбар үлдсэнгүй"
	case strings.Contains(e, "crud.Check"):
		return "generator → crud.Check унасан"
	}
	if len(e) > 90 {
		e = e[:90]
	}
	return e
}

// Write — хураангуй хүснэгт. verbose үед форм тус бүрийн мөр + TODO.
func (r *AuditReport) Write(w io.Writer, verbose bool) {
	c := r.Counts()
	fmt.Fprintf(w, "формууд: %d | clean: %d | зөвхөн cosmetic TODO: %d | логик TODO-той: %d | refused: %d | error: %d\n",
		len(r.Items), c[StatusClean], c[StatusMinor], c[StatusTodo], c[StatusRefused], c[StatusError])
	if reasons := r.ErrorReasons(); len(reasons) > 0 {
		fmt.Fprintln(w, "\nүүсээгүй шалтгаан:")
		for _, s := range reasons {
			fmt.Fprintf(w, "  %3d  %s\n", s.Forms, s.Code)
		}
	}
	if top := r.TopTodos(); len(top) > 0 {
		fmt.Fprintln(w, "\nTODO код (формын тоо / нийт):")
		for _, s := range top {
			fmt.Fprintf(w, "  %3d / %4d  %s\n", s.Forms, s.Occurrences, s.Code)
		}
	}
	if verbose {
		fmt.Fprintln(w)
		for _, it := range r.Items {
			fmt.Fprintf(w, "%-6s %-8s %s (%s) талбар=%d child=%d todo=%d", it.Form, it.Status, it.Table, it.Model, it.Fields, it.Children, len(it.Todos))
			if it.Err != "" {
				fmt.Fprintf(w, " — %s", it.Err)
			}
			fmt.Fprintln(w)
			for _, t := range it.Todos {
				fmt.Fprintf(w, "         %s\n", t)
			}
		}
	}
}
