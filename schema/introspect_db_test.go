package schema_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/khan-vision/khan/schema"
	"github.com/khan-vision/khan/testkit"
)

// TestIntrospectPostgres — бодит Postgres дээр (KHAN_TEST_DB_* байхгүй бол skip).
// Бүх DDL нэг транзакцад, тест дуусахад rollback.
func TestIntrospectPostgres(t *testing.T) {
	tx := testkit.TxDB(t)
	ddl := []string{
		`CREATE SCHEMA khan_it`,
		`CREATE TYPE khan_it.doc_status AS ENUM ('draft', 'posted')`,
		`CREATE TABLE khan_it.doc (
			id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
			org_id uuid NOT NULL,
			code varchar(32),
			status khan_it.doc_status NOT NULL DEFAULT 'draft',
			total_amount numeric(18,2) NOT NULL DEFAULT 0,
			tags text[],
			created_at timestamptz, updated_at timestamptz, deleted_at timestamptz,
			UNIQUE (org_id, code))`,
		`COMMENT ON TABLE khan_it.doc IS 'Баримт'`,
		`CREATE TABLE khan_it.doc_line (
			id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
			doc_id uuid NOT NULL REFERENCES khan_it.doc(id) ON DELETE CASCADE,
			qty int,
			price float8)`,
		`CREATE TABLE khan_it.event (id bigint, at date NOT NULL) PARTITION BY RANGE (at)`,
		`CREATE TABLE khan_it.event_2026 PARTITION OF khan_it.event FOR VALUES FROM ('2026-01-01') TO ('2027-01-01')`,
		`CREATE VIEW khan_it.v_doc AS SELECT id, code FROM khan_it.doc`,
		`CREATE MATERIALIZED VIEW khan_it.mv_doc AS SELECT org_id, sum(total_amount) AS total FROM khan_it.doc GROUP BY org_id`,
	}
	for _, s := range ddl {
		if err := tx.Exec(s).Error; err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}

	cat, err := schema.Introspect(context.Background(), tx, schema.Filter{Schemas: []string{"khan_it"}})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tb := range cat.Tables {
		names = append(names, tb.Name+":"+string(tb.Kind))
	}
	want := []string{"doc:table", "doc_line:table", "event:table", "mv_doc:matview", "v_doc:view"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("tables = %v, want %v (partition child хасагдах ёстой)", names, want)
	}

	doc, _ := cat.Table("khan_it.doc")
	if doc.Comment != "Баримт" || !reflect.DeepEqual(doc.PrimaryKey, []string{"id"}) ||
		!reflect.DeepEqual(doc.Uniques, [][]string{{"org_id", "code"}}) {
		t.Fatalf("doc = %+v", doc)
	}
	if c := doc.Column("status"); !reflect.DeepEqual(c.Enum, []string{"draft", "posted"}) || c.Nullable {
		t.Fatalf("status = %+v", c)
	}
	if c := doc.Column("total_amount"); !schema.IsMoney(*c) {
		t.Fatalf("total_amount = %+v", c)
	}
	if c := doc.Column("code"); c.MaxLength == nil || *c.MaxLength != 32 {
		t.Fatalf("code = %+v", c)
	}
	if c := doc.Column("tags"); !c.Array || c.UDT != "text" {
		t.Fatalf("tags = %+v", c)
	}
	if issues := schema.Lint(*doc); len(issues) != 0 {
		t.Fatalf("doc lint = %v", issues)
	}

	line, _ := cat.Table("khan_it.doc_line")
	if !line.Column("id").Identity || len(line.ForeignKeys) != 1 || line.ForeignKeys[0].OnDelete != "cascade" ||
		line.ForeignKeys[0].RefTable != "doc" {
		t.Fatalf("doc_line = %+v", line)
	}
	if ev, _ := cat.Table("khan_it.event"); !ev.Partitioned {
		t.Fatal("event partitioned биш")
	}
	if mv, _ := cat.Table("khan_it.mv_doc"); len(mv.Columns) != 2 {
		t.Fatalf("matview columns = %+v", mv.Columns)
	}
}
