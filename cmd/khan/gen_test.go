package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/khan-vision/khan/schema"
)

func i2p(i int) *int { return &i }

// genCatalog — `gen` командын тестэд зориулсан жижиг snapshot.
func genCatalog() *schema.Catalog {
	cols := func(extra ...schema.Column) []schema.Column {
		base := []schema.Column{
			{Name: "org_id", Position: 1, Type: "uuid", UDT: "uuid"},
			{Name: "id", Position: 2, Type: "uuid", UDT: "uuid", PK: true, Default: ptr("gen_random_uuid()")},
		}
		base = append(base, extra...)
		n := len(base)
		return append(base,
			schema.Column{Name: "created_at", Position: n + 1, Type: "timestamptz", UDT: "timestamptz", Default: ptr("now()")},
			schema.Column{Name: "updated_at", Position: n + 2, Type: "timestamptz", UDT: "timestamptz", Default: ptr("now()")},
			schema.Column{Name: "deleted_at", Position: n + 3, Type: "timestamptz", UDT: "timestamptz", Nullable: true},
		)
	}
	room := schema.Table{Schema: "organization", Name: "conference_room", Kind: schema.KindTable, PrimaryKey: []string{"id"},
		Columns: cols(
			schema.Column{Name: "room_name", Position: 3, Type: "character varying(500)", UDT: "varchar", MaxLength: i2p(500), Comment: "Өрөөний нэр"},
			schema.Column{Name: "capacity", Position: 4, Type: "integer", UDT: "int4", Nullable: true},
		)}
	plain := schema.Table{Schema: "public", Name: "lut_plain", Kind: schema.KindTable, PrimaryKey: []string{"id"},
		Columns: []schema.Column{
			{Name: "id", Position: 1, Type: "integer", UDT: "int4", Identity: true, PK: true},
			{Name: "name", Position: 2, Type: "text", UDT: "text"},
		}}
	return &schema.Catalog{Version: schema.FormatVersion, Tables: []schema.Table{room, plain}}
}

func ptr(s string) *string { return &s }

func TestGenResourceCommand(t *testing.T) {
	cat := writeCatalog(t, genCatalog())
	var out, errb bytes.Buffer
	args := []string{"gen", "resource", "-catalog", cat, "-table", "organization.conference_room", "-label", "Хурлын өрөө",
		"-name", "Room", "-endpoint", "/api/organization/conference/rooms", "-pkg", "confcore"}
	if err := run(args, &out, &errb); err != nil {
		t.Fatalf("%v\n%s", err, errb.String())
	}
	src := out.String()
	for _, want := range []string{"package confcore", "func RoomResource(db func() *gorm.DB", `"/api/organization/conference/rooms"`,
		`"Хурлын өрөө"`, `"Өрөөний нэр"`, "validate.Int"} {
		if !strings.Contains(src, want) {
			t.Errorf("%q алга:\n%s", want, src)
		}
	}
	if !strings.Contains(errb.String(), "RoomResource") || !strings.Contains(errb.String(), "TODO") {
		t.Errorf("stderr хураангуй: %q", errb.String())
	}

	// -o файл
	path := filepath.Join(t.TempDir(), "room.go")
	out.Reset()
	if err := run(append(args, "-o", path), &out, &errb); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil || out.Len() != 0 || !strings.Contains(string(b), "RoomResource") {
		t.Fatalf("-o: %v out=%d", err, out.Len())
	}
}

func TestGenResourceWithLegacyAndRefusal(t *testing.T) {
	cat := writeCatalog(t, genCatalog())
	form := filepath.Join(t.TempDir(), "form.json")
	if err := os.WriteFile(form, []byte(`{"model":"conference_room","schema":[
	 {"model":"room_name","label":"Нэр","formType":"Text","rules":[{"type":"required"}]},
	 {"model":"capacity","label":"Багтаамж","formType":"Number","rules":[{"type":"min","val":"1"}]}],"ui":{"schema":[]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	grid := filepath.Join(t.TempDir(), "grid.json")
	if err := os.WriteFile(grid, []byte(`{"model":"conference_room","sort":"room_name","sortOrder":"asc","paging":20,"isGlobalSearch":true,"schema":[{"model":"room_name","label":"Нэр"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	// -table өгөөгүй: формын model-оос
	if err := run([]string{"gen", "resource", "-catalog", cat, "-form", form, "-grid", grid, "-label", "Өрөө"}, &out, &errb); err != nil {
		t.Fatalf("%v\n%s", err, errb.String())
	}
	src := strings.Join(strings.Fields(out.String()), " ")
	for _, want := range []string{`"t.room_name ASC, t.id ASC"`, "DefaultLimit: 20", "Min: validate.Bound(1)", "Required: true"} {
		if !strings.Contains(src, strings.Join(strings.Fields(want), " ")) {
			t.Errorf("%q алга:\n%s", want, out.String())
		}
	}

	// org_id-гүй хүснэгт: татгалзана, -force-оор үүснэ.
	out.Reset()
	err := run([]string{"gen", "resource", "-catalog", cat, "-table", "public.lut_plain"}, &out, &errb)
	if err == nil || !strings.Contains(err.Error(), "-force") || out.Len() != 0 {
		t.Fatalf("татгалзах ёстой: %v out=%d", err, out.Len())
	}
	if err := run([]string{"gen", "resource", "-catalog", cat, "-table", "public.lut_plain", "-force"}, &out, &errb); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "schema.Lint зөрчил") {
		t.Errorf("lint comment алга:\n%s", out.String())
	}
}

func TestGenUsageErrors(t *testing.T) {
	cat := writeCatalog(t, genCatalog())
	var out bytes.Buffer
	for _, args := range [][]string{
		{"gen"}, {"gen", "nope"}, {"gen", "resource"}, {"gen", "audit"},
		{"gen", "resource", "-catalog", cat},
		{"gen", "resource", "-catalog", cat, "-table", "nope.nope"},
		{"gen", "resource", "-catalog", cat, "-table", "organization.conference_room", "-hidden", "weird"},
		{"gen", "resource", "-catalog", cat, "-table", "organization.conference_room", "-ref-label", "novalue"},
		{"gen", "resource", "-catalog", cat, "-table", "organization.conference_room", "-form", "/nonexistent.json"},
		{"gen", "resource", "-catalog", cat, "-table", "organization.conference_room", "-name", "bad name"},
		{"gen", "audit", "-catalog", cat, "-forms", "/nonexistent-dir"},
	} {
		if err := run(args, &out, &out); err == nil {
			t.Errorf("%v: алдаа хүлээсэн", args)
		}
	}
}

func TestGenRefLabelFlag(t *testing.T) {
	r := refLabels{}
	if err := r.Set("emp_id=last_name+first_name"); err != nil || strings.Join(r["emp_id"], ",") != "last_name,first_name" {
		t.Fatalf("Set: %v %v", err, r)
	}
	for _, bad := range []string{"", "=a", "a=", "a"} {
		if err := r.Set(bad); err == nil {
			t.Errorf("Set(%q): алдаа хүлээсэн", bad)
		}
	}
}

func TestGenAuditCommand(t *testing.T) {
	cat := writeCatalog(t, genCatalog())
	forms := t.TempDir()
	if err := os.WriteFile(filepath.Join(forms, "10.json"), []byte(`{"model":"conference_room","schema":[{"model":"room_name","label":"Нэр","formType":"Text","rules":[]}],"ui":{"schema":[]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(forms, "11.json"), []byte(`{"model":"lut_plain","schema":[{"model":"name","formType":"Text","rules":[]}],"ui":{"schema":[]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(t.TempDir(), "gen")
	var out bytes.Buffer
	if err := run([]string{"gen", "audit", "-catalog", cat, "-forms", forms, "-v", "-o", outDir}, &out, &out); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	for _, want := range []string{"формууд: 2", "refused: 1", "org_id алга/буруу", "10     "} {
		if !strings.Contains(s, want) {
			t.Errorf("%q алга:\n%s", want, s)
		}
	}
	if _, err := os.Stat(filepath.Join(outDir, "10_organization_conference_room.go")); err != nil {
		t.Error(err)
	}
}
