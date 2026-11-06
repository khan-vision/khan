package gen

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/khan-vision/khan/schema"
)

var update = flag.Bool("update", false, "golden файлуудыг шинэчилнэ")

func sp(s string) *string { return &s }
func ip(i int) *int       { return &i }

type colOpt func(*schema.Column)

func nullable() colOpt        { return func(c *schema.Column) { c.Nullable = true } }
func def(d string) colOpt     { return func(c *schema.Column) { c.Default = sp(d) } }
func identity() colOpt        { return func(c *schema.Column) { c.Identity = true } }
func comment(s string) colOpt { return func(c *schema.Column) { c.Comment = s } }
func maxLen(n int) colOpt     { return func(c *schema.Column) { c.MaxLength = ip(n) } }
func enum(v ...string) colOpt { return func(c *schema.Column) { c.Enum = v } }
func array() colOpt           { return func(c *schema.Column) { c.Array = true } }
func numeric(p, s int) colOpt { return func(c *schema.Column) { c.Precision, c.Scale = ip(p), ip(s) } }
func generated() colOpt       { return func(c *schema.Column) { c.Generated = true } }

// c — багана (Type нь UDT-ээс гарна, тест бүрт хангалттай).
func c(name, udt string, opts ...colOpt) schema.Column {
	col := schema.Column{Name: name, Type: udt, UDT: udt}
	for _, o := range opts {
		o(&col)
	}
	return col
}

// tbl — хүснэгт: баганад Position автоматаар, org_id/id/timestamps-ийг тусад нь өгнө.
func tbl(sch, name string, cols ...schema.Column) schema.Table {
	for i := range cols {
		cols[i].Position = i + 1
	}
	return schema.Table{Schema: sch, Name: name, Kind: schema.KindTable, Columns: cols}
}

func std(extra ...schema.Column) []schema.Column {
	base := []schema.Column{
		c("org_id", "uuid"),
		c("id", "uuid", def("gen_random_uuid()")),
	}
	base = append(base, extra...)
	return append(base,
		c("created_at", "timestamptz", def("now()")),
		c("updated_at", "timestamptz", def("now()")),
		c("deleted_at", "timestamptz", nullable()),
	)
}

func withPK(t schema.Table, pk ...string) schema.Table { t.PrimaryKey = pk; return t }

func fk(t schema.Table, col, refSchema, refTable, refCol string) schema.Table {
	t.ForeignKeys = append(t.ForeignKeys, schema.ForeignKey{
		Name: t.Name + "_" + col + "_fkey", Columns: []string{col}, RefSchema: refSchema, RefTable: refTable, RefColumns: []string{refCol},
	})
	return t
}

// testCatalog — golden/CLI тестүүдийн синтетик catalog (бодит ERP-ийн хүснэгтүүдийн хэлбэртэй).
func testCatalog() *schema.Catalog {
	lut := withPK(tbl("attendance", "lut_period", append([]schema.Column{
		c("org_id", "uuid"),
		c("id", "int4", identity()),
		c("period", "varchar", maxLen(255), nullable()),
	}, std()[2:]...)...), "org_id", "id")
	lut.Uniques = [][]string{{"org_id", "period"}}

	strct := withPK(tbl("organization", "struct", std(c("struct", "text"))...), "org_id", "id")
	beacon := withPK(tbl("attendance", "beacon_request",
		c("org_id", "uuid"), c("id", "uuid", def("gen_random_uuid()")),
		c("imei", "varchar", maxLen(50)), c("stuct_id", "uuid", nullable()),
		c("status", "varchar", maxLen(32), def("'Хүлээгдэж буй'::character varying")),
		c("created_at", "timestamp"), c("updated_at", "timestamp"),
	), "org_id", "id")
	beacon = fk(beacon, "stuct_id", "organization", "struct", "id")
	beacon.Uniques = [][]string{{"org_id", "imei"}}

	prio := withPK(tbl("planning", "project_priorities", std(c("project_id", "uuid"), c("priority", "varchar", maxLen(255)))...), "id")
	tasks := withPK(tbl("planning", "tasks",
		c("org_id", "uuid"), c("id", "uuid", def("gen_random_uuid()")),
		c("project_id", "uuid"), c("parent_id", "uuid", nullable()), c("task", "varchar", maxLen(255)),
		c("stage_id", "uuid"), c("start_date", "date", nullable()), c("end_date", "date", nullable()),
		c("progress", "float4"), c("priority_id", "uuid", nullable()), c("description", "text", nullable()),
		c("task_order", "varchar", maxLen(255), def("'1'::character varying")),
		c("emp_id", "uuid"), c("created_at", "timestamp", nullable()), c("updated_at", "timestamp", nullable()),
		c("deleted_at", "timestamp", nullable()), c("color", "varchar", maxLen(255), nullable()),
		c("is_milestone", "bool", def("false")),
	), "org_id", "id")
	tasks = fk(tasks, "priority_id", "planning", "project_priorities", "id")
	members := withPK(tbl("planning", "task_members",
		c("id", "uuid", def("gen_random_uuid()")), c("org_id", "uuid"), c("project_id", "uuid"), c("task_id", "uuid"),
		c("emp_id", "uuid"), c("task_weight_progress", "float4", nullable()),
		c("created_at", "timestamptz", def("now()")), c("updated_at", "timestamptz", def("now()")),
	), "id")
	group := withPK(tbl("planning", "task_sub_task_group",
		c("org_id", "uuid"), c("id", "uuid", def("gen_random_uuid()")), c("project_id", "uuid"), c("task_id", "uuid"),
		c("title", "varchar", maxLen(255)), c("created_at", "timestamp", nullable()), c("updated_at", "timestamp", nullable()),
		c("progress", "float4", def("0")), c("group_order", "int2"),
	), "org_id", "id")
	emp := withPK(tbl("organization", "employee", std(c("firstname", "varchar", maxLen(100)), c("lastname", "varchar", maxLen(100)))...), "id")

	party := withPK(tbl("finance", "party", std(c("name", "varchar", maxLen(200)))...), "id")
	glob := withPK(tbl("classification", "class", c("id", "uuid", def("gen_random_uuid()")), c("code", "varchar", maxLen(20)), c("name", "text")), "id")
	invoice := withPK(tbl("finance", "invoice", std(
		c("invoice_number", "varchar", maxLen(40), comment("Нэхэмжлэхийн дугаар")),
		c("code", "varchar", maxLen(10), nullable()),
		c("party_id", "uuid", comment("Харилцагч")),
		c("class_code", "uuid", nullable()),
		c("total_amount", "numeric", numeric(18, 2), def("0")),
		c("rate", "numeric", numeric(12, 4), nullable()),
		c("quantity", "numeric", numeric(10, 0), nullable()),
		c("weight", "float8", nullable()),
		c("lines_count", "int4", nullable()),
		c("big_counter", "int8", nullable()),
		c("tiny", "int2", nullable()),
		c("is_paid", "bool", def("false")),
		c("due_date", "date", nullable()),
		c("posted_at", "timestamptz", nullable()),
		c("open_time", "time", nullable()),
		c("meta", "jsonb", nullable()),
		c("notes", "text", nullable()),
		c("currency", "bpchar", maxLen(3), def("'MNT'")),
		c("status", "invoice_status", enum("draft", "posted", "void"), def("'draft'")),
		c("tags", "text", array(), nullable()),
		c("shape", "geometry", nullable()),
		c("search_vector", "tsvector", nullable(), generated()),
		c("created_by", "uuid", nullable()),
		c("updated_by", "uuid", nullable()),
		c("version", "int4", def("1")),
	)...), "id")
	invoice = fk(invoice, "party_id", "finance", "party", "id")
	invoice = fk(invoice, "class_code", "classification", "class", "id")
	invoice.Comment = "Нэхэмжлэх"
	invoice.Uniques = [][]string{{"org_id", "invoice_number"}, {"code"}}

	account := withPK(tbl("core", "account", std(
		c("username", "varchar", maxLen(60)),
		c("password_hash", "varchar", maxLen(100)),
		c("email", "varchar", maxLen(120), nullable()),
	)...), "id")
	noOrg := withPK(tbl("public", "lut_noorg", c("id", "int4", identity()), c("name", "varchar", maxLen(50))), "id")
	view := tbl("finance", "v_invoice", c("id", "uuid"))
	view.Kind = schema.KindView

	cat := &schema.Catalog{Version: schema.FormatVersion, Database: "test", Tables: []schema.Table{
		lut, strct, beacon, prio, tasks, members, group, emp, party, glob, invoice, account, noOrg, view,
	}}
	cat.Sort()
	return cat
}

func readTestdata(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func testForm(t *testing.T, name string) *LegacyForm {
	t.Helper()
	f, err := ParseForm(bytes.NewReader(readTestdata(t, name)))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func testGrid(t *testing.T, name string) *LegacyGrid {
	t.Helper()
	g, err := ParseGrid(bytes.NewReader(readTestdata(t, name)))
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// golden — гаралтыг testdata/<name>.golden-тэй харьцуулна (-update-ээр шинэчилнэ).
func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (go test ./gen -update-ээр үүсгэ)", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s golden-оос зөрлөө (go test ./gen -update-ээр шинэчилж diff-ийг хар)\n--- got ---\n%s", name, got)
	}
}

func mustGen(t *testing.T, o Options) *Result {
	t.Helper()
	if o.Catalog == nil {
		o.Catalog = testCatalog()
	}
	r, err := Generate(o)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
