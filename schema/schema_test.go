package schema

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

func ptr[T any](v T) *T { return &v }

// numericTypmod — numeric(p,s)-ийн atttypmod.
func numericTypmod(p, s int) int { return (p<<16 | s) + 4 }

func fixture(t *testing.T, f Filter) *Catalog {
	t.Helper()
	rels := []relationRow{
		{"finance", "invoice", "r", "Нэхэмжлэх"},
		{"finance", "invoice_line", "r", ""},
		{"public", "invoice", "r", ""},
		{"finance", "v_invoice", "v", ""},
		{"finance", "mv_totals", "m", ""},
		{"audit", "log", "p", ""},
	}
	cols := []columnRow{
		{"finance", "invoice", 2, "org_id", "uuid", "uuid", "pg_catalog", false, false, nil, false, false, -1, ""},
		{"finance", "invoice", 1, "id", "uuid", "uuid", "pg_catalog", false, false, ptr("gen_random_uuid()"), false, false, -1, ""},
		{"finance", "invoice", 3, "total_amount", "numeric(18,2)", "numeric", "pg_catalog", false, false, ptr("0"), false, false, numericTypmod(18, 2), "Нийт"},
		{"finance", "invoice", 4, "status", "finance.invoice_status", "invoice_status", "finance", false, false, nil, false, false, -1, ""},
		{"finance", "invoice", 5, "code", "character varying(32)", "varchar", "pg_catalog", false, true, nil, false, false, 36, ""},
		{"finance", "invoice", 6, "tags", "text[]", "text", "pg_catalog", true, true, nil, false, false, -1, ""},
		{"finance", "invoice", 7, "created_at", "timestamp with time zone", "timestamptz", "pg_catalog", false, true, nil, false, false, -1, ""},
		{"finance", "invoice", 8, "updated_at", "timestamp with time zone", "timestamptz", "pg_catalog", false, true, nil, false, false, -1, ""},
		{"finance", "invoice", 9, "deleted_at", "timestamp with time zone", "timestamptz", "pg_catalog", false, true, nil, false, false, -1, ""},
		{"finance", "invoice_line", 1, "id", "bigint", "int8", "pg_catalog", false, false, nil, true, false, -1, ""},
		{"finance", "invoice_line", 2, "invoice_id", "uuid", "uuid", "pg_catalog", false, false, nil, false, false, -1, ""},
		{"finance", "invoice_line", 3, "price", "double precision", "float8", "pg_catalog", false, true, nil, false, false, -1, ""},
		{"public", "invoice", 1, "id", "integer", "int4", "pg_catalog", false, false, ptr("nextval('invoice_id_seq'::regclass)"), false, false, -1, ""},
		{"finance", "v_invoice", 1, "id", "uuid", "uuid", "pg_catalog", false, true, nil, false, false, -1, ""},
		{"ghost", "missing", 1, "id", "uuid", "uuid", "pg_catalog", false, false, nil, false, false, -1, ""},
	}
	cons := []constraintRow{
		{"finance", "invoice", "invoice_pkey", "p", `["id"]`, "", "", "[]", " "},
		{"finance", "invoice", "invoice_org_code_key", "u", `["org_id","code"]`, "", "", "[]", " "},
		{"finance", "invoice_line", "invoice_line_pkey", "p", `["id"]`, "", "", "[]", " "},
		{"finance", "invoice_line", "invoice_line_invoice_fk", "f", `["invoice_id"]`, "finance", "invoice", `["id"]`, "c"},
	}
	enums := []enumRow{{"finance", "invoice_status", `["draft","posted"]`}}
	cat, err := build("khan_test", rels, cols, cons, enums, f)
	if err != nil {
		t.Fatal(err)
	}
	return cat
}

func TestBuildCatalog(t *testing.T) {
	cat := fixture(t, Filter{})
	if cat.Version != FormatVersion || cat.Database != "khan_test" {
		t.Fatalf("header = %d %q", cat.Version, cat.Database)
	}
	var names []string
	for _, tb := range cat.Tables {
		names = append(names, tb.QualifiedName()+":"+string(tb.Kind))
	}
	want := []string{"audit.log:table", "finance.invoice:table", "finance.invoice_line:table",
		"finance.mv_totals:matview", "finance.v_invoice:view", "public.invoice:table"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("tables = %v\nwant %v", names, want)
	}

	inv, err := cat.Table("finance.invoice")
	if err != nil {
		t.Fatal(err)
	}
	if inv.Comment != "Нэхэмжлэх" || !reflect.DeepEqual(inv.PrimaryKey, []string{"id"}) ||
		!reflect.DeepEqual(inv.Uniques, [][]string{{"org_id", "code"}}) {
		t.Fatalf("invoice = %+v", inv)
	}
	if inv.Columns[0].Name != "id" || !inv.Columns[0].PK || inv.Columns[1].PK {
		t.Fatalf("PK/эрэмбэ буруу: %+v", inv.Columns[:2])
	}
	amt := inv.Column("total_amount")
	if *amt.Precision != 18 || *amt.Scale != 2 || !IsMoney(*amt) || amt.Comment != "Нийт" {
		t.Fatalf("total_amount = %+v", amt)
	}
	if st := inv.Column("status"); !reflect.DeepEqual(st.Enum, []string{"draft", "posted"}) {
		t.Fatalf("enum = %v", st.Enum)
	}
	if code := inv.Column("code"); *code.MaxLength != 32 || !code.Nullable {
		t.Fatalf("code = %+v", code)
	}

	line, _ := cat.Table("invoice_line")
	fk := line.ForeignKeys[0]
	if fk.RefSchema != "finance" || fk.RefTable != "invoice" || fk.OnDelete != "cascade" || !line.Columns[0].Identity {
		t.Fatalf("line = %+v", line)
	}
	// public.invoice: nextval default байгаа ч PK constraint-гүй → PK биш (lambda таамагладаг байсан).
	pub, _ := cat.Table("public.invoice")
	if pub.Columns[0].PK || len(pub.PrimaryKey) != 0 {
		t.Fatalf("default-оос PK таамаглав: %+v", pub)
	}
	if lg, _ := cat.Table("audit.log"); !lg.Partitioned {
		t.Fatal("partitioned тэмдэглэгээ алга")
	}
}

func TestTableLookupAmbiguous(t *testing.T) {
	cat := fixture(t, Filter{})
	if _, err := cat.Table("invoice"); err == nil || !strings.Contains(err.Error(), "олон schema") {
		t.Fatalf("давхцал = %v", err)
	}
	if _, err := cat.Table("nope"); err == nil {
		t.Fatal("байхгүй хүснэгтэд алдаа хүлээсэн")
	}
}

func TestFilter(t *testing.T) {
	cat := fixture(t, Filter{Schemas: []string{"finance"}, Ignore: []string{"invoice_line"}, SkipViews: true})
	if len(cat.Tables) != 1 || cat.Tables[0].QualifiedName() != "finance.invoice" {
		t.Fatalf("filter = %v", cat.Tables)
	}
	cat = fixture(t, Filter{ExcludeSchemas: []string{"finance", "audit"}, Ignore: []string{"public.invoice"}})
	if len(cat.Tables) != 0 {
		t.Fatalf("exclude = %v", cat.Tables)
	}
}

func TestJSONRoundTrip(t *testing.T) {
	cat := fixture(t, Filter{})
	var buf bytes.Buffer
	if err := cat.WriteJSON(&buf); err != nil {
		t.Fatal(err)
	}
	back, err := ReadJSON(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cat, back) {
		t.Fatal("roundtrip ялгаатай")
	}
	if len(Diff(cat, back)) != 0 {
		t.Fatal("ижил snapshot-д diff гарав")
	}
	if _, err := ReadJSON(strings.NewReader(`{"version":99}`)); err == nil {
		t.Fatal("буруу version зөвшөөрөгдөв")
	}
}

func TestDiff(t *testing.T) {
	from := fixture(t, Filter{})
	to := fixture(t, Filter{Ignore: []string{"audit.log"}})
	inv, _ := to.Table("finance.invoice")
	inv.Column("code").Nullable = false
	inv.Columns = append(inv.Columns, Column{Name: "note", Position: 10, Type: "text", UDT: "text", Nullable: true})
	inv.Columns = inv.Columns[1:] // id хасагдав

	got := []string{}
	for _, c := range Diff(from, to) {
		got = append(got, c.String())
	}
	want := []string{
		"- audit.log",
		"~ finance.invoice.code: character varying(32) → character varying(32) not null",
		"- finance.invoice.id uuid not null default gen_random_uuid()",
		"+ finance.invoice.note text",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("diff =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestLint(t *testing.T) {
	cat := fixture(t, Filter{})
	inv, _ := cat.Table("finance.invoice")
	if issues := Lint(*inv); len(issues) != 0 {
		t.Fatalf("finance.invoice зөрчилгүй байх ёстой: %v", issues)
	}
	line, _ := cat.Table("finance.invoice_line")
	rules := map[string]bool{}
	for _, i := range Lint(*line) {
		rules[i.Rule+":"+i.Column] = true
	}
	for _, want := range []string{"org-id:org_id", "timestamps:created_at", "timestamps:deleted_at", "money:price"} {
		if !rules[want] {
			t.Errorf("%s зөрчил алга: %v", want, rules)
		}
	}
	pub, _ := cat.Table("public.invoice")
	if !strings.Contains(Lint(*pub)[0].String(), RulePrimaryKey) {
		t.Fatal("PK-гүй хүснэгт илрээгүй")
	}
	view, _ := cat.Table("finance.v_invoice")
	if Lint(*view) != nil {
		t.Fatal("view-д lint хийхгүй")
	}
}

func TestGoType(t *testing.T) {
	cases := []struct {
		col  Column
		opt  GoTypeOptions
		want string
		imp  string
	}{
		{Column{UDT: "uuid"}, GoTypeOptions{}, "string", ""},
		{Column{UDT: "uuid", Nullable: true}, GoTypeOptions{}, "*string", ""},
		{Column{UDT: "int8"}, GoTypeOptions{}, "int64", ""},
		{Column{UDT: "int4", Nullable: true}, GoTypeOptions{}, "*int32", ""},
		{Column{UDT: "numeric"}, GoTypeOptions{}, "float64", ""},
		{Column{UDT: "numeric", Nullable: true}, GoTypeOptions{Numeric: "decimal.Decimal"}, "*decimal.Decimal", "github.com/shopspring/decimal"},
		{Column{UDT: "timestamptz", Nullable: true}, GoTypeOptions{}, "*time.Time", "time"},
		{Column{UDT: "jsonb", Nullable: true}, GoTypeOptions{}, "datatypes.JSON", "gorm.io/datatypes"},
		{Column{UDT: "bytea", Nullable: true}, GoTypeOptions{}, "[]byte", ""},
		{Column{UDT: "text", Array: true, Nullable: true}, GoTypeOptions{}, "pq.StringArray", "github.com/lib/pq"},
		{Column{UDT: "int4", Array: true}, GoTypeOptions{}, "pq.Int64Array", "github.com/lib/pq"},
		{Column{UDT: "invoice_status", Enum: []string{"a"}}, GoTypeOptions{}, "string", ""},
		{Column{UDT: "geometry", Nullable: true}, GoTypeOptions{}, "*string", ""},
		{Column{UDT: "bool"}, GoTypeOptions{}, "bool", ""},
	}
	for _, tc := range cases {
		typ, imps := GoType(tc.col, tc.opt)
		imp := ""
		if len(imps) > 0 {
			imp = imps[0]
		}
		if typ != tc.want || imp != tc.imp {
			t.Errorf("GoType(%+v) = %s %q, want %s %q", tc.col, typ, imp, tc.want, tc.imp)
		}
	}
}
