package gen

import (
	"errors"
	"strings"
	"testing"

	"github.com/khan-vision/khan/query"
	"github.com/khan-vision/khan/schema"
	"github.com/khan-vision/khan/validate"
)

func TestBaseKindTable(t *testing.T) {
	cases := []struct {
		col      schema.Column
		kind     validate.Kind
		maxLen   int
		scale    int
		hasBound bool
		bad      bool
	}{
		{c("a", "uuid"), validate.UUID, 0, 0, false, false},
		{c("a", "text"), validate.Text, 0, 0, false, false},
		{c("a", "citext"), validate.Text, 0, 0, false, false},
		{c("a", "varchar", maxLen(40)), validate.String, 40, 0, false, false},
		{c("a", "varchar"), validate.String, 0, 0, false, false},
		{c("a", "bpchar", maxLen(3)), validate.String, 3, 0, false, false},
		{c("a", "int2"), validate.Int, 0, 0, true, false},
		{c("a", "int4"), validate.Int, 0, 0, true, false},
		{c("a", "int8"), validate.Int, 0, 0, false, false},
		{c("a", "numeric", numeric(18, 2)), validate.Money, 0, 0, false, false},
		{c("a", "numeric", numeric(12, 4)), validate.Number, 0, 4, false, false},
		{c("a", "numeric", numeric(10, 0)), validate.Int, 0, 0, false, false},
		{c("a", "numeric"), validate.Number, 0, 0, false, false},
		{c("a", "numeric", numeric(38, 30)), validate.Number, 0, validate.MaxNumberScale, false, false},
		{c("a", "float4"), validate.Number, 0, 0, false, false},
		{c("a", "float8"), validate.Number, 0, 0, false, false},
		{c("a", "bool"), validate.Bool, 0, 0, false, false},
		{c("a", "date"), validate.Date, 0, 0, false, false},
		{c("a", "timestamp"), validate.DateTime, 0, 0, false, false},
		{c("a", "timestamptz"), validate.DateTime, 0, 0, false, false},
		{c("a", "time"), validate.Time, 0, 0, false, false},
		{c("a", "json"), validate.JSON, 0, 0, false, false},
		{c("a", "jsonb"), validate.JSON, 0, 0, false, false},
		{c("a", "my_enum", enum("x", "y")), validate.Enum, 0, 0, false, false},
		{c("a", "text", array()), "", 0, 0, false, true},
		{c("a", "geometry"), "", 0, 0, false, true},
		{c("a", "bytea"), "", 0, 0, false, true},
		{c("a", "tsvector"), "", 0, 0, false, true},
		{c("a", "interval"), "", 0, 0, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.col.UDT+"/"+tc.col.Type, func(t *testing.T) {
			ki, why := baseKind(tc.col)
			if tc.bad {
				if why == "" {
					t.Fatalf("дэмжигдэхгүй байх ёстой, kind=%s", ki.Kind)
				}
				return
			}
			if why != "" {
				t.Fatal(why)
			}
			if ki.Kind != tc.kind || ki.Rules.MaxLen != tc.maxLen || ki.Rules.Scale != tc.scale {
				t.Errorf("got kind=%s maxLen=%d scale=%d", ki.Kind, ki.Rules.MaxLen, ki.Rules.Scale)
			}
			if (ki.Rules.Min != nil) != tc.hasBound {
				t.Errorf("bound = %v", ki.Rules.Min)
			}
			if err := ki.Rules.Check(ki.Kind); err != nil {
				t.Errorf("Rules.Check: %v", err)
			}
		})
	}
}

func TestMoneyIsMoneyOnly(t *testing.T) {
	// numeric(18,2) л Money; бусад numeric нь Number (AGENTS.md: мөнгө = numeric(18,2)).
	if ki, _ := baseKind(c("a", "numeric", numeric(19, 2))); ki.Kind != validate.Number {
		t.Fatalf("numeric(19,2) = %s", ki.Kind)
	}
}

func TestPKWithoutOrgAndFK(t *testing.T) {
	tb := schema.Table{PrimaryKey: []string{"org_id", "id"}}
	if got := pkWithoutOrg(&tb); len(got) != 1 || got[0] != "id" {
		t.Fatalf("pk = %v", got)
	}
	tb.PrimaryKey = []string{"id"}
	if got := pkWithoutOrg(&tb); len(got) != 1 {
		t.Fatalf("pk = %v", got)
	}
	// нийлмэл FK (org_id,pid)→(org_id,id) нь нэг баганынх
	tb.ForeignKeys = []schema.ForeignKey{{Columns: []string{"org_id", "pid"}, RefSchema: "s", RefTable: "p", RefColumns: []string{"org_id", "id"}}}
	if _, ref, ok := fkOf(&tb, "pid"); !ok || ref != "id" {
		t.Fatalf("fkOf = %v %v", ref, ok)
	}
	if _, _, ok := fkOf(&tb, "org_id"); ok {
		t.Fatal("org_id FK биш")
	}
	// жинхэнэ нийлмэл FK → Ref болохгүй
	tb.ForeignKeys = []schema.ForeignKey{{Columns: []string{"a", "b"}, RefSchema: "s", RefTable: "p", RefColumns: []string{"x", "y"}}}
	if _, _, ok := fkOf(&tb, "a"); ok {
		t.Fatal("нийлмэл FK нэг баганынх биш")
	}
}

func TestUniqueSets(t *testing.T) {
	scoped, global := uniqueSets(&schema.Table{Uniques: [][]string{{"org_id", "code"}, {"name", "org_id"}, {"slug"}, {"a", "b"}}})
	if !scoped["code"] || !scoped["name"] || scoped["slug"] || scoped["a"] {
		t.Fatalf("scoped = %v", scoped)
	}
	if !global["slug"] || global["code"] {
		t.Fatalf("global = %v", global)
	}
}

func TestGuessLabel(t *testing.T) {
	mk := func(cols ...schema.Column) *schema.Table { tb := tbl("s", "t", cols...); return &tb }
	cases := []struct {
		name string
		tb   *schema.Table
		want []string
	}{
		{"lastname+firstname", mk(c("firstname", "varchar"), c("lastname", "varchar")), []string{"lastname", "firstname"}},
		{"last_name+first_name", mk(c("first_name", "text"), c("last_name", "text")), []string{"last_name", "first_name"}},
		{"name", mk(c("code", "text"), c("name", "text")), []string{"name"}},
		{"title", mk(c("title", "text"), c("code", "text")), []string{"title"}},
		{"suffix _name", mk(c("flow_name", "varchar")), []string{"flow_name"}},
		{"code", mk(c("code", "varchar"), c("x", "int4")), []string{"code"}},
		{"first text", mk(c("id", "uuid"), c("priority", "varchar")), []string{"priority"}},
	}
	for _, tc := range cases {
		got, ok := guessLabel(tc.tb, "id")
		if !ok || strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Errorf("%s: got %v %v", tc.name, got, ok)
		}
	}
	if _, ok := guessLabel(mk(c("id", "uuid"), c("n", "int4")), "id"); ok {
		t.Error("текст багана байхгүй бол ok=false")
	}
}

func TestJSRegexSource(t *testing.T) {
	for in, want := range map[string]string{`/^a+$/`: `^a+$`, `/abc/i`: `(?i)abc`, `^x$`: `^x$`, ` /a/ `: `a`} {
		if got := jsRegexSource(in); got != want {
			t.Errorf("%q → %q, want %q", in, got, want)
		}
	}
}

func TestUIOnlyFormula(t *testing.T) {
	mk := func(props ...string) LegacyFormula {
		var f LegacyFormula
		for _, p := range props {
			f.Targets = append(f.Targets, struct {
				Field string `json:"field"`
				Prop  string `json:"prop"`
			}{"x", p})
		}
		return f
	}
	if !uiOnlyFormula(mk("hidden", "disabled")) || uiOnlyFormula(mk("hidden", "value")) || uiOnlyFormula(mk()) {
		t.Fatal("uiOnlyFormula буруу")
	}
}

// ---------------------------------------------------------------------------------------------
// Хуучин дүрмүүд

func legacyForm(t *testing.T, model, fields string) *LegacyForm {
	t.Helper()
	js := `{"model":"` + model + `","schema":[` + fields + `],"ui":{"schema":[]},"triggers":{"namespace":"lambda/x","insert":{"before":"","after":"controllers.AfterX"},"update":{"before":"controllers.BeforeY","after":""},"email":{"to":["a"]}},"formula":[{"targets":[{"field":"a","prop":"hidden"}],"template":"'{b}' == 1"},{"targets":[{"field":"a","prop":"value"}],"template":"{b} * 2"}],"extraButtons":[{"x":1}]}`
	f, err := ParseForm(strings.NewReader(js))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func fieldByName(t *testing.T, r *Result, name string) string {
	t.Helper()
	src := squash(string(r.Source))
	i := strings.Index(src, `Name: "`+name+`"`)
	if i < 0 {
		t.Fatalf("талбар %q алга:\n%s", name, r.Source)
	}
	rest := src[i:]
	if j := strings.Index(rest, "}, {"); j > 0 {
		rest = rest[:j]
	}
	return rest
}

func TestLegacyRulesMapping(t *testing.T) {
	cat := &schema.Catalog{Version: schema.FormatVersion, Tables: []schema.Table{
		withPK(tbl("core", "person", std(
			c("email", "varchar", maxLen(100), nullable()),
			c("phone", "varchar", maxLen(20), nullable()),
			c("reg", "varchar", maxLen(10), nullable()),
			c("latin", "varchar", maxLen(10), nullable()),
			c("note", "varchar", maxLen(500), nullable()),
			c("score", "int4", nullable()),
			c("code", "varchar", maxLen(20)),
			c("custom", "varchar", maxLen(20), nullable()),
			c("badre", "varchar", maxLen(20), nullable()),
			c("pass", "varchar", maxLen(100), nullable()),
			c("kind", "text", nullable()),
			c("level", "int4", nullable()),
			c("secret_ref", "uuid", nullable()),
			c("fav", "varchar", maxLen(20), nullable()),
			c("start", "date", nullable()),
			c("flag", "bool", nullable()),
			c("pwd", "varchar", maxLen(100), nullable()),
		)...), "id"),
	}}
	fields := strings.Join([]string{
		`{"model":"email","label":"И-мэйл","formType":"Text","rules":[{"type":"email"},{"type":"required"}]}`,
		`{"model":"phone","label":"Утас","formType":"Text","rules":[{"type":"mongolianMobileNumber"}]}`,
		`{"model":"reg","label":"Регистр","formType":"Register","rules":[{"type":"register"}]}`,
		`{"model":"latin","label":"Латин","formType":"Text","rules":[{"type":"englishAlphabet","msg":"Зөвхөн латин"}]}`,
		`{"model":"note","label":"Тэмдэглэл","formType":"Textarea","rules":[{"type":"min","val":"3"},{"type":"max","val":"200"}]}`,
		`{"model":"score","label":"Оноо","formType":"Number","rules":[{"type":"min","val":"0"},{"type":"max","val":"100"},{"type":"number"}]}`,
		`{"model":"code","label":"Код","formType":"Text","rules":[{"type":"unique"},{"type":"required"}],"default":"A1"}`,
		`{"model":"custom","label":"Custom","formType":"Text","custom_regex":"/^[A-Z]{2}$/i","custom_regex_msg":"2 үсэг"}`,
		`{"model":"badre","label":"Bad","formType":"Text","rules":[{"type":"custom","regex":"(?<=a)b","msg":"x"},{"type":"strange"}]}`,
		`{"model":"pass","label":"Нууц үг","formType":"Password","rules":[]}`,
		`{"model":"kind","label":"Төрөл","formType":"Select","options":[{"label":"Нэг","value":"one"},{"label":"Хоёр","value":"two"}],"rules":[]}`,
		`{"model":"level","label":"Түвшин","formType":"Select","options":[{"label":"Бага","value":"1"},{"label":"Их","value":"2"}],"rules":[]}`,
		`{"model":"secret_ref","label":"Ref","formType":"Text","fillByUserField":"id","rules":[]}`,
		`{"model":"fav","label":"Дуртай","formType":"employeeSelector","rules":[],"trigger":"https://x/y","hasEquation":true,"equations":"{a}+1","ifshowhide":"{kind}==1"}`,
		`{"model":"start","label":"Эхлэх","formType":"Date","default":"2026-01-02","rules":[]}`,
		`{"model":"flag","label":"Идэвхтэй","formType":"Switch","default":"1","hidden":true,"rules":[]}`,
		`{"model":"pwd","label":"Hidden","formType":"Text","hidden":true,"rules":[]}`,
	}, ",")
	form := legacyForm(t, "person", fields)

	r := mustGen(t, Options{Catalog: cat, Table: "core.person", Form: form})
	cases := map[string][]string{
		"email":  {"validate.Email", "Required: true"},
		"phone":  {"validate.Phone"},
		"reg":    {"validate.String", "Pattern: regexp.MustCompile", `Input: "register"`},
		"latin":  {"Pattern: regexp.MustCompile", `PatternMsg: "Зөвхөн латин"`},
		"note":   {"MinLen: 3", "MaxLen: 200"},
		"score":  {"Min: validate.Bound(0)", "Max: validate.Bound(100)"},
		"code":   {"Unique: true", "Required: true", `Default: "A1"`},
		"custom": {"(?i)^[A-Z]{2}$", `PatternMsg: "2 үсэг"`},
		"pass":   {"validate.Password"},
		"kind":   {"validate.Enum", `Enum: []string{"one", "two"}`, "options: one=Нэг, two=Хоёр"},
		"level":  {"validate.Int", "options: 1=Бага, 2=Их"},
		"fav":    {`Input: "employee"`},
		"start":  {`Default: "2026-01-02"`},
		"flag":   {"CreateOnly: true"},
	}
	for name, wants := range cases {
		blk := fieldByName(t, r, name)
		for _, w := range wants {
			hay := blk
			if strings.HasPrefix(w, "options:") { // тайлбар нь талбарын өмнө (блокоос гадна)
				hay = squash(string(r.Source))
			}
			if !strings.Contains(hay, w) {
				t.Errorf("%s: %q алга", name, w)
			}
		}
	}
	src := squash(string(r.Source))
	if strings.Contains(src, `Name: "secret_ref"`) {
		t.Error("fillByUserField талбар write allowlist-д орсон")
	}
	// Todo-ууд
	codes := map[string]int{}
	for _, td := range r.Todos {
		codes[td.Code]++
	}
	for _, want := range []string{CodeRule, CodeFillUser, CodeTrigger, CodeFormula, CodeShowHide, CodeFieldTrigger, CodeFormFeature} {
		if codes[want] == 0 {
			t.Errorf("TODO код %q алга: %v", want, codes)
		}
	}
	// Хөрвөхгүй custom regex + үл мэдэгдэх дүрэм нь талбарыг устгахгүй, TODO болно.
	if !strings.Contains(fieldByName(t, r, "badre"), "validate.String") {
		t.Error("badre талбар алга")
	}
	// hidden: createonly (анхдагч), exclude, keep.
	if ex := mustGen(t, Options{Catalog: cat, Table: "core.person", Form: form, HiddenMode: HiddenExclude}); strings.Contains(squash(string(ex.Source)), `Name: "pwd"`) {
		t.Error("HiddenExclude: hidden талбар орсон")
	}
	if kp := mustGen(t, Options{Catalog: cat, Table: "core.person", Form: form, HiddenMode: HiddenKeep}); strings.Contains(fieldByName(t, kp, "pwd"), "CreateOnly") {
		t.Error("HiddenKeep: CreateOnly орсон")
	}
	if cr := fieldByName(t, r, "pwd"); !strings.Contains(cr, "CreateOnly: true") {
		t.Error("HiddenCreateOnly анхдагч биш")
	}
}

func TestPasswordGetsListQuery(t *testing.T) {
	form := legacyForm(t, "account", `{"model":"username","label":"Нэр","formType":"Text","rules":[]},{"model":"password_hash","label":"Нууц үг","formType":"Password","rules":[]}`)
	r := mustGen(t, Options{Table: "core.account", Form: form})
	src := squash(string(r.Source))
	if !strings.Contains(src, "ListQuery: func(db *gorm.DB, _ string) *gorm.DB") {
		t.Fatalf("Password-д ListQuery алга:\n%s", r.Source)
	}
	sel := src[strings.Index(src, ".Select(")+8:]
	sel = sel[:strings.Index(sel, ")")]
	if strings.Contains(sel, "password_hash") || !strings.Contains(sel, "t.username") {
		t.Fatalf("SELECT буруу: %s", sel)
	}
	if strings.Contains(src, `"t.password_hash"`) {
		t.Error("password sort/filter/search-д орсон")
	}
}

func TestGridMapping(t *testing.T) {
	g, err := ParseGrid(strings.NewReader(`{"model":"invoice","isView":false,"sort":"total_amount","sortOrder":"desc","paging":25,"isExcel":true,"isGlobalSearch":false,
	 "condition":"status = 1","columnAggregations":[{"aggregation":"SUM","column":"total_amount"},{"aggregation":"AVG","column":"rate"},{"aggregation":"MEDIAN","column":"rate"},{"aggregation":"SUM","column":"notes"}],
	 "triggers":{"beforeFetch":"controllers.X"},"isPivot":true,"isExcelUpload":true,
	 "schema":[
	  {"model":"invoice_number","label":"Дугаар","hide":false,"sortable":true,"searchable":true,"filterable":true,"filter":{"type":"Select"}},
	  {"model":"total_amount","label":"Дүн","hide":false,"sortable":true,"filterable":true},
	  {"model":"party_name","label":"Харилцагч","virtualColumn":true},
	  {"model":"notes","label":"Тэмдэглэл","hide":true,"searchable":true}]}`))
	if err != nil {
		t.Fatal(err)
	}
	r := mustGen(t, Options{Table: "finance.invoice", Grid: g})
	src := squash(string(r.Source))
	for _, want := range []string{
		`DefaultOrder: "t.total_amount DESC, t.id DESC"`, "DefaultLimit: 25",
		`"total_amount": "COALESCE(SUM(t.total_amount),0)"`, `"avg_rate": "COALESCE(AVG(t.rate),0)"`,
		`"invoice_number": query.Enum("t.invoice_number")`, `"total_amount": query.Number("t.total_amount")`,
		`Search: []string{"t.invoice_number", "t.notes"}`,
		"Export: &query.ExportSpec{", `{Key: "total_amount", Header: "Дүн", Expr: "t.total_amount", Numeric: true}`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("%q алга", want)
		}
	}
	if strings.Contains(src, "party_name") && !strings.Contains(src, "TODO(khan-gen) [grid] grid баганууд") {
		t.Error("virtual багана TODO-гүй")
	}
	if strings.Contains(src, `Key: "notes"`) {
		t.Error("hide=true багана export-д орсон")
	}
	for _, code := range []string{CodeGrid, CodeTrigger} {
		found := false
		for _, td := range r.Todos {
			found = found || td.Code == code
		}
		if !found {
			t.Errorf("grid TODO %q алга", code)
		}
	}
}

// ---------------------------------------------------------------------------------------------
// Алдаа / татгалзлын замууд

func TestGenerateErrors(t *testing.T) {
	cat := testCatalog()
	var refused *RefusedError
	_, err := Generate(Options{Catalog: cat, Table: "public.lut_noorg"})
	if !errors.As(err, &refused) || len(refused.Issues) == 0 {
		t.Fatalf("org_id-гүй хүснэгт татгалзах ёстой: %v", err)
	}
	// Force → үүснэ, lint TODO + comment-той.
	r, err := Generate(Options{Catalog: cat, Table: "public.lut_noorg", Force: true})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, td := range r.Todos {
		found = found || td.Code == CodeLint
	}
	if !found || !strings.Contains(string(r.Source), "schema.Lint зөрчил") {
		t.Errorf("Force-ийн lint TODO/comment алга:\n%s", r.Source)
	}

	for name, o := range map[string]Options{
		"nil catalog":      {Table: "a.b"},
		"table хоосон":     {Catalog: cat},
		"олдсонгүй":        {Catalog: cat, Table: "nope.nope"},
		"view":             {Catalog: cat, Table: "finance.v_invoice"},
		"Name буруу":       {Catalog: cat, Table: "finance.party", Name: "lower"},
		"Package буруу":    {Catalog: cat, Table: "finance.party", Package: "a-b"},
		"HiddenMode буруу": {Catalog: cat, Table: "finance.party", HiddenMode: "x"},
	} {
		if _, err := Generate(o); err == nil {
			t.Errorf("%s: алдаа хүлээсэн", name)
		}
	}
}

func TestFormTableFromModel(t *testing.T) {
	// Table өгөөгүй бол формын model (давхцалгүй bare нэр)-ээс.
	r := mustGen(t, Options{Form: testForm(t, "form_892_beacon_request.json")})
	if r.Table != "attendance.beacon_request" {
		t.Fatalf("table = %s", r.Table)
	}
}

func TestSubformSkips(t *testing.T) {
	cat := testCatalog()
	form := legacyForm(t, "tasks", `{"model":"task","label":"Ажил","formType":"Text","rules":[]},
	 {"model":"ghost","formType":"SubForm","name":"X","parent":"task_id","schema":[]},
	 {"model":"task_members","formType":"SubForm","name":"Нөхөд","parent":"nope","schema":[]},
	 {"model":"task_sub_task_group","formType":"SubForm","name":"Бүлэг","parent":"task_id","checkEmpty":true,"disableDelete":true,"schema":[{"model":"title","label":"Нэр","formType":"Text","rules":[]},{"model":"title","label":"Нэр","formType":"Text","rules":[]},{"model":"missing_col","formType":"Text","rules":[]}]}`)
	r := mustGen(t, Options{Catalog: cat, Table: "planning.tasks", Form: form})
	if r.Children != 1 {
		t.Fatalf("children = %d\n%s", r.Children, r.Source)
	}
	n := 0
	for _, td := range r.Todos {
		if td.Code == CodeSubform {
			n++
		}
	}
	if n < 3 { // ghost, нөхөд(FK алга), checkEmpty/disable
		t.Errorf("subform TODO = %d", n)
	}
	if strings.Count(squash(string(r.Source)), `Name: "title"`) != 1 {
		t.Error("давхардсан subform талбар dedupe хийгдээгүй")
	}
}

func TestExportQueryKinds(t *testing.T) {
	if filterKind(validate.Password) != "" || filterKind(validate.JSON) != "" || filterKind(validate.Time) != "" {
		t.Error("Password/JSON/Time шүүлтгүй байх ёстой")
	}
	if filterKind(validate.Enum) != query.KindEnum || filterKind(validate.UUID) != query.KindUUID {
		t.Error("filterKind буруу")
	}
}
