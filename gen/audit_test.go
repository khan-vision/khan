package gen

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestModelTables(t *testing.T) {
	dir := t.TempDir()
	model := func(table string) []byte {
		return []byte("package formModels\n\ntype X struct{}\n\n// TableName sets\nfunc (a *X) TableName() string {\n\treturn \"" + table + "\"\n}\n")
	}
	writeFile(t, filepath.Join(dir, "LutPeriod795.go"), model("attendance.lut_period"))
	writeFile(t, filepath.Join(dir, "AnnualLeave701.go"), model("attendance.annual_leave"))
	writeFile(t, filepath.Join(dir, "README.md"), []byte("x"))
	writeFile(t, filepath.Join(dir, "NoTable9.go"), []byte("package formModels\n"))
	got, err := ModelTables(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["795"] != "attendance.lut_period" || got["701"] != "attendance.annual_leave" {
		t.Fatalf("ModelTables = %v", got)
	}
	if _, err := ModelTables(filepath.Join(dir, "nope")); err == nil {
		t.Error("байхгүй хавтас: алдаа хүлээсэн")
	}
}

func TestAudit(t *testing.T) {
	forms := t.TempDir()
	models := t.TempDir()
	grids := t.TempDir()
	out := filepath.Join(t.TempDir(), "gen")
	for id, name := range map[string]string{"795": "form_795_lut_period.json", "892": "form_892_beacon_request.json", "1018": "form_1018_tasks_subform.json"} {
		writeFile(t, filepath.Join(forms, id+".json"), readTestdata(t, name))
	}
	writeFile(t, filepath.Join(grids, "894.json"), readTestdata(t, "grid_894_beacon_request.json"))
	writeFile(t, filepath.Join(models, "LutPeriod795.go"), []byte("package m\nfunc (a *A) TableName() string {\n\treturn \"attendance.lut_period\"\n}\n"))
	// хүснэгт нь catalog-д байхгүй, org_id-гүй, талбар таардаггүй формууд
	writeFile(t, filepath.Join(forms, "1.json"), []byte(`{"model":"ghost_table","schema":[],"ui":{"schema":[]}}`))
	writeFile(t, filepath.Join(forms, "2.json"), []byte(`{"model":"lut_noorg","schema":[{"model":"name","label":"Нэр","formType":"Text","rules":[]}],"ui":{"schema":[]}}`))
	writeFile(t, filepath.Join(forms, "3.json"), []byte(`not json`))
	writeFile(t, filepath.Join(forms, "4.json"), []byte(`{"model":"party","schema":[{"model":"nope","formType":"Text","rules":[]}],"ui":{"schema":[]}}`))

	rep, err := Audit(AuditOptions{Catalog: testCatalog(), FormsDir: forms, ModelsDir: models, GridsDir: grids, OutDir: out})
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]AuditItem{}
	for _, it := range rep.Items {
		by[it.Form] = it
	}
	if len(by) != 7 {
		t.Fatalf("items = %d", len(by))
	}
	if by["795"].Table != "attendance.lut_period" || by["795"].Status != StatusMinor && by["795"].Status != StatusClean {
		t.Errorf("795 = %+v", by["795"])
	}
	if by["892"].Grid != "894" || by["892"].Status != StatusTodo {
		t.Errorf("892 = %+v", by["892"])
	}
	if by["1018"].Status != StatusTodo || by["1018"].Children != 2 {
		t.Errorf("1018 = %+v", by["1018"])
	}
	if by["1"].Status != StatusError || by["2"].Status != StatusRefused || by["3"].Status != StatusError || by["4"].Status != StatusError {
		t.Errorf("1=%s 2=%s 3=%s 4=%s", by["1"].Status, by["2"].Status, by["3"].Status, by["4"].Status)
	}
	if _, err := os.Stat(filepath.Join(out, "795_attendance_lut_period.go")); err != nil {
		t.Errorf("OutDir файл алга: %v", err)
	}
	if ents, _ := os.ReadDir(out); len(ents) != 3 {
		t.Errorf("OutDir файл = %d", len(ents))
	}

	c := rep.Counts()
	if c[StatusRefused] != 1 || c[StatusError] != 3 {
		t.Errorf("counts = %v", c)
	}
	if top := rep.TopTodos(); len(top) == 0 || top[0].Forms < top[len(top)-1].Forms {
		t.Errorf("TopTodos эрэмбэ: %v", top)
	}
	reasons := rep.ErrorReasons()
	if len(reasons) == 0 {
		t.Error("ErrorReasons хоосон")
	}
	var buf bytes.Buffer
	rep.Write(&buf, true)
	for _, want := range []string{"формууд: 7", "refused: 1", "TODO код", "1018", "үүсээгүй шалтгаан"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("Write-д %q алга:\n%s", want, buf.String())
		}
	}

	// Force → refused нь үүснэ.
	rep2, err := Audit(AuditOptions{Catalog: testCatalog(), FormsDir: forms, Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep2.Counts()[StatusRefused] != 0 {
		t.Error("Force-той үед refused байх ёсгүй")
	}
	if _, err := Audit(AuditOptions{FormsDir: forms}); err == nil {
		t.Error("Catalog-гүй: алдаа хүлээсэн")
	}
	if _, err := Audit(AuditOptions{Catalog: testCatalog(), FormsDir: t.TempDir()}); err == nil {
		t.Error("хоосон хавтас: алдаа хүлээсэн")
	}
}

func TestParseFormAndGrid(t *testing.T) {
	f, err := ParseForm(strings.NewReader(`{"model":"m","identity":"id","schema":[
	  {"model":"a","formType":"Text","default":5,"label":7,"relation":{"table":"x.y","fields":"p, q","filterWithUser":[{"tableField":"org_id","userField":"org_id"}],"multiple":false}},
	  {"model":"b","formType":"Select","relation":{"table":null,"fields":null,"filterWithUser":null}},
	  {"model":"c","formType":"Text"}],
	  "ui":{"schema":[{"type":"row","children":[{"type":"col","children":[{"model":"b","formType":"Select"}]},{"type":"col","children":[{"model":"a","formType":"Text"},{"model":"b","formType":"Select"}]}]}]},
	  "triggers":"https://not-an-object","step":{"hasPermission":false,"list":[]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(f.Order, ",") != "b,a,c" {
		t.Errorf("Order = %v", f.Order)
	}
	a := f.Field("a")
	if a == nil || string(a.Default) != "5" || string(a.Label) != "7" || strings.Join(a.Relation.Fields, "|") != "p|q" || len(a.Relation.FilterWithUser) != 1 {
		t.Errorf("a = %+v %+v", a, a.Relation)
	}
	if f.HasSteps || f.HasExtraButtons {
		t.Error("хоосон step/extraButtons-г feature гэж үзэх ёсгүй")
	}
	for _, bad := range []string{`{}`, `[`, `{"schema":[]}`} {
		if _, err := ParseForm(strings.NewReader(bad)); err == nil {
			t.Errorf("ParseForm(%q): алдаа хүлээсэн", bad)
		}
	}
	for _, bad := range []string{`{}`, `x`} {
		if _, err := ParseGrid(strings.NewReader(bad)); err == nil {
			t.Errorf("ParseGrid(%q): алдаа хүлээсэн", bad)
		}
	}
	g := testGrid(t, "grid_894_beacon_request.json")
	if g.Column("imei") == nil || g.Column("zzz") != nil || !g.IsExcel || g.Paging != 50 {
		t.Errorf("grid = %+v", g)
	}
}

func TestResourceWrapper(t *testing.T) {
	src, err := Resource(Options{Catalog: testCatalog(), Table: "finance.party"})
	if err != nil || !strings.Contains(string(src), "func PartyResource(") {
		t.Fatalf("Resource: %v\n%s", err, src)
	}
	if _, err := Resource(Options{}); err == nil {
		t.Error("Resource(Options{}): алдаа хүлээсэн")
	}
}
