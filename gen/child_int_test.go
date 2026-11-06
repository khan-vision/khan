package gen

import (
	"strings"
	"testing"

	"github.com/khan-vision/khan/schema"
)

// legacy subform хүснэгтүүд: int8 PK (sequence), PK-гүй, default-гүй int PK, text PK.
func TestSubformIntPK(t *testing.T) {
	cat := testCatalog()
	mk := func(name string, pk []string, cols ...schema.Column) schema.Table {
		base := []schema.Column{c("org_id", "uuid"), c("task_id", "uuid"), c("body", "varchar", maxLen(100)),
			c("created_at", "timestamptz", nullable()), c("updated_at", "timestamptz", nullable())}
		return withPK(tbl("public", name, append(cols, base...)...), pk...)
	}
	cat.Tables = append(cat.Tables,
		mk("sub_note", []string{"id"}, c("id", "int8", def("nextval('public.sub_note_id_seq'::regclass)"))),
		mk("sub_ident", []string{"tid"}, c("tid", "int4", identity())),
		mk("sub_nopk", nil, c("id", "int8", def("nextval('x'::regclass)"))),
		mk("sub_nodef", []string{"id"}, c("id", "int8")),
		mk("sub_text", []string{"id"}, c("id", "text")),
	)
	cat.Sort()
	sf := func(model string) string {
		return `{"model":"` + model + `","formType":"SubForm","name":"` + model + `","parent":"task_id","schema":[{"model":"body","label":"Тайлбар","formType":"Text","rules":[]}]}`
	}
	form := legacyForm(t, "tasks", `{"model":"task","label":"Ажил","formType":"Text","rules":[]},`+
		strings.Join([]string{sf("sub_note"), sf("sub_ident"), sf("sub_nopk"), sf("sub_nodef"), sf("sub_text")}, ","))
	r := mustGen(t, Options{Catalog: cat, Table: "planning.tasks", Form: form})
	if r.Children != 2 {
		t.Fatalf("children = %d\n%s", r.Children, r.Source)
	}
	src := squash(string(r.Source))
	if !strings.Contains(src, `ID: crud.IDSpec{Kind: crud.IDInt}`) || !strings.Contains(src, `ID: crud.IDSpec{Column: "tid", Kind: crud.IDInt}`) {
		t.Errorf("int child ID гараагүй:\n%s", r.Source)
	}
	var msgs []string
	for _, td := range r.Todos {
		if td.Code == CodeSubform {
			msgs = append(msgs, td.Msg)
		}
	}
	all := strings.Join(msgs, "\n")
	for _, want := range []string{"PK ганц баганатай биш", "sequence", "зөвхөн uuid эсвэл int"} {
		if !strings.Contains(all, want) {
			t.Errorf("TODO %q алга:\n%s", want, all)
		}
	}
}
