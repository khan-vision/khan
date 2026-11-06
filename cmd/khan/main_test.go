package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/khan-vision/khan/schema"
)

func writeCatalog(t *testing.T, cat *schema.Catalog) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "c.json")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := cat.WriteJSON(f); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSchemaDiffCommand(t *testing.T) {
	col := schema.Column{Name: "id", Position: 1, Type: "uuid", UDT: "uuid"}
	a := &schema.Catalog{Version: schema.FormatVersion, Tables: []schema.Table{{Schema: "s", Name: "t", Kind: schema.KindTable, Columns: []schema.Column{col}}}}
	b := &schema.Catalog{Version: schema.FormatVersion}
	var out bytes.Buffer
	if err := run([]string{"schema", "diff", writeCatalog(t, a), writeCatalog(t, b)}, &out, &out); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(out.String()); got != "- s.t" {
		t.Fatalf("diff = %q", got)
	}
}

func TestUsageErrors(t *testing.T) {
	var out bytes.Buffer
	for _, args := range [][]string{nil, {"gen"}, {"schema", "nope"}, {"schema", "diff", "one"}} {
		if err := run(args, &out, &out); err == nil {
			t.Errorf("%v: алдаа хүлээсэн", args)
		}
	}
}
