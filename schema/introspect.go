package schema

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"gorm.io/gorm"
)

// Filter — introspection-ий хамрах хүрээ.
type Filter struct {
	// Schemas — зөвхөн эдгээр schema (хоосон = системийнхээс бусад бүгд).
	Schemas []string
	// ExcludeSchemas — хасах schema.
	ExcludeSchemas []string
	// Ignore — хасах хүснэгт: "schema.name" эсвэл бүх schema-д "name".
	Ignore []string
	// SkipViews — view/matview-ийг оруулахгүй.
	SkipViews bool
}

func (f Filter) keep(schemaName, table string, kind Kind) bool {
	if f.SkipViews && kind != KindTable {
		return false
	}
	if len(f.Schemas) > 0 && !contains(f.Schemas, schemaName) {
		return false
	}
	if contains(f.ExcludeSchemas, schemaName) {
		return false
	}
	return !contains(f.Ignore, schemaName+"."+table) && !contains(f.Ignore, table)
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// systemSchemaSQL — pg_namespace n-ийн системийн schema-г хасах нөхцөл.
const systemSchemaSQL = `n.nspname NOT IN ('pg_catalog', 'information_schema')
	AND n.nspname NOT LIKE 'pg\_toast%' AND n.nspname NOT LIKE 'pg\_temp\_%'`

// relationSQL — хүснэгт (энгийн + partitioned parent), view, matview.
// Partition/inheritance child болон extension-ий эзэмшдэг relation-г хасна.
const relationSQL = `
SELECT n.nspname AS schema_name, c.relname AS name, c.relkind::text AS relkind,
       COALESCE(obj_description(c.oid, 'pg_class'), '') AS comment
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE c.relkind IN ('r', 'p', 'v', 'm')
  AND ` + systemSchemaSQL + `
  AND NOT EXISTS (SELECT 1 FROM pg_inherits i WHERE i.inhrelid = c.oid)
  AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.classid = 'pg_class'::regclass
                  AND d.objid = c.oid AND d.deptype = 'e')
ORDER BY 1, 2`

const columnSQL = `
SELECT n.nspname AS schema_name, c.relname AS table_name, a.attnum AS position, a.attname AS name,
       format_type(a.atttypid, a.atttypmod) AS type,
       COALESCE(et.typname, t.typname) AS udt,
       COALESCE(etn.nspname, tn.nspname) AS udt_schema,
       (t.typcategory = 'A') AS is_array,
       NOT a.attnotnull AS nullable,
       pg_get_expr(ad.adbin, ad.adrelid) AS default_expr,
       (a.attidentity <> '') AS identity,
       (a.attgenerated <> '') AS generated,
       a.atttypmod AS typmod,
       COALESCE(col_description(c.oid, a.attnum), '') AS comment
FROM pg_attribute a
JOIN pg_class c ON c.oid = a.attrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
JOIN pg_type t ON t.oid = a.atttypid
JOIN pg_namespace tn ON tn.oid = t.typnamespace
LEFT JOIN pg_type et ON t.typcategory = 'A' AND et.oid = t.typelem
LEFT JOIN pg_namespace etn ON etn.oid = et.typnamespace
LEFT JOIN pg_attrdef ad ON ad.adrelid = a.attrelid AND ad.adnum = a.attnum
WHERE a.attnum > 0 AND NOT a.attisdropped
  AND c.relkind IN ('r', 'p', 'v', 'm')
  AND ` + systemSchemaSQL + `
ORDER BY 1, 2, 3`

const constraintSQL = `
SELECT n.nspname AS schema_name, c.relname AS table_name, con.conname AS name, con.contype::text AS type,
       array_to_json(ARRAY(
         SELECT a.attname FROM unnest(con.conkey) WITH ORDINALITY k(attnum, ord)
         JOIN pg_attribute a ON a.attrelid = con.conrelid AND a.attnum = k.attnum
         ORDER BY k.ord))::text AS columns,
       COALESCE(rn.nspname, '') AS ref_schema, COALESCE(rc.relname, '') AS ref_table,
       COALESCE(array_to_json(ARRAY(
         SELECT a.attname FROM unnest(con.confkey) WITH ORDINALITY k(attnum, ord)
         JOIN pg_attribute a ON a.attrelid = con.confrelid AND a.attnum = k.attnum
         ORDER BY k.ord))::text, '[]') AS ref_columns,
       con.confdeltype::text AS on_delete
FROM pg_constraint con
JOIN pg_class c ON c.oid = con.conrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
LEFT JOIN pg_class rc ON rc.oid = con.confrelid
LEFT JOIN pg_namespace rn ON rn.oid = rc.relnamespace
WHERE con.contype IN ('p', 'f', 'u')
  AND ` + systemSchemaSQL + `
ORDER BY 1, 2, 3`

const enumSQL = `
SELECT n.nspname AS schema_name, t.typname AS name,
       array_to_json(array_agg(e.enumlabel ORDER BY e.enumsortorder))::text AS labels
FROM pg_enum e
JOIN pg_type t ON t.oid = e.enumtypid
JOIN pg_namespace n ON n.oid = t.typnamespace
GROUP BY 1, 2`

type relationRow struct {
	SchemaName string
	Name       string
	Relkind    string
	Comment    string
}

type columnRow struct {
	SchemaName  string
	TableName   string
	Position    int
	Name        string
	Type        string
	UDT         string `gorm:"column:udt"`
	UDTSchema   string `gorm:"column:udt_schema"`
	IsArray     bool
	Nullable    bool
	DefaultExpr *string
	Identity    bool
	Generated   bool
	Typmod      int
	Comment     string
}

type constraintRow struct {
	SchemaName string
	TableName  string
	Name       string
	Type       string
	Columns    string
	RefSchema  string
	RefTable   string
	RefColumns string
	OnDelete   string
}

type enumRow struct {
	SchemaName string
	Name       string
	Labels     string
}

// Introspect — DB-ийн бүтцийг уншина (зөвхөн SELECT, pg_catalog-оос).
func Introspect(ctx context.Context, db *gorm.DB, f Filter) (*Catalog, error) {
	if db == nil {
		return nil, fmt.Errorf("schema: db nil")
	}
	q := db.WithContext(ctx)

	var dbName string
	if err := q.Raw("SELECT current_database()").Scan(&dbName).Error; err != nil {
		return nil, fmt.Errorf("schema: current_database: %w", err)
	}

	var rels []relationRow
	if err := q.Raw(relationSQL).Scan(&rels).Error; err != nil {
		return nil, fmt.Errorf("schema: relations: %w", err)
	}
	var cols []columnRow
	if err := q.Raw(columnSQL).Scan(&cols).Error; err != nil {
		return nil, fmt.Errorf("schema: columns: %w", err)
	}
	var cons []constraintRow
	if err := q.Raw(constraintSQL).Scan(&cons).Error; err != nil {
		return nil, fmt.Errorf("schema: constraints: %w", err)
	}
	var enums []enumRow
	if err := q.Raw(enumSQL).Scan(&enums).Error; err != nil {
		return nil, fmt.Errorf("schema: enums: %w", err)
	}
	return build(dbName, rels, cols, cons, enums, f)
}

// build — түүхий мөрүүдээс Catalog угсарна (DB-гүй тестлэгдэнэ).
func build(dbName string, rels []relationRow, cols []columnRow, cons []constraintRow, enums []enumRow, f Filter) (*Catalog, error) {
	enumLabels := map[string][]string{}
	for _, e := range enums {
		var labels []string
		if err := json.Unmarshal([]byte(e.Labels), &labels); err != nil {
			return nil, fmt.Errorf("schema: enum %s.%s: %w", e.SchemaName, e.Name, err)
		}
		enumLabels[e.SchemaName+"."+e.Name] = labels
	}

	cat := &Catalog{Version: FormatVersion, Database: dbName}
	index := map[string]int{}
	for _, r := range rels {
		kind, partitioned := relkind(r.Relkind)
		if !f.keep(r.SchemaName, r.Name, kind) {
			continue
		}
		index[r.SchemaName+"."+r.Name] = len(cat.Tables)
		cat.Tables = append(cat.Tables, Table{
			Schema: r.SchemaName, Name: r.Name, Kind: kind, Partitioned: partitioned, Comment: r.Comment,
		})
	}

	for _, r := range cols {
		i, ok := index[r.SchemaName+"."+r.TableName]
		if !ok {
			continue
		}
		col := Column{
			Name: r.Name, Position: r.Position, Type: r.Type, UDT: r.UDT, Array: r.IsArray,
			Nullable: r.Nullable, Default: r.DefaultExpr, Identity: r.Identity, Generated: r.Generated,
			Comment: r.Comment, Enum: enumLabels[r.UDTSchema+"."+r.UDT],
		}
		applyTypmod(&col, r.Typmod)
		// View/matview-ийн багана pg_attribute-д NOT NULL биш тул nullable гэж үлдэнэ.
		cat.Tables[i].Columns = append(cat.Tables[i].Columns, col)
	}

	for _, r := range cons {
		i, ok := index[r.SchemaName+"."+r.TableName]
		if !ok {
			continue
		}
		var columns, refColumns []string
		if err := json.Unmarshal([]byte(r.Columns), &columns); err != nil {
			return nil, fmt.Errorf("schema: constraint %s: %w", r.Name, err)
		}
		if err := json.Unmarshal([]byte(r.RefColumns), &refColumns); err != nil {
			return nil, fmt.Errorf("schema: constraint %s: %w", r.Name, err)
		}
		t := &cat.Tables[i]
		switch r.Type {
		case "p":
			t.PrimaryKey = columns
			for _, name := range columns {
				if c := t.Column(name); c != nil {
					c.PK = true
				}
			}
		case "u":
			t.Uniques = append(t.Uniques, columns)
		case "f":
			t.ForeignKeys = append(t.ForeignKeys, ForeignKey{
				Name: r.Name, Columns: columns, RefSchema: r.RefSchema, RefTable: r.RefTable,
				RefColumns: refColumns, OnDelete: fkAction(r.OnDelete),
			})
		}
	}
	cat.Sort()
	return cat, nil
}

func relkind(k string) (Kind, bool) {
	switch k {
	case "v":
		return KindView, false
	case "m":
		return KindMatView, false
	case "p":
		return KindTable, true
	default:
		return KindTable, false
	}
}

func fkAction(a string) string {
	switch a {
	case "r":
		return "restrict"
	case "c":
		return "cascade"
	case "n":
		return "set null"
	case "d":
		return "set default"
	default:
		return "no action"
	}
}

// applyTypmod — numeric(p,s) болон varchar(n)/char(n)-ийн хэмжээ.
func applyTypmod(c *Column, typmod int) {
	if typmod < 4 {
		return
	}
	switch strings.ToLower(c.UDT) {
	case "numeric":
		p := ((typmod - 4) >> 16) & 0xffff
		s := (typmod - 4) & 0xffff
		c.Precision, c.Scale = &p, &s
	case "varchar", "bpchar":
		n := typmod - 4
		c.MaxLength = &n
	}
}
