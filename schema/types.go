package schema

import "strings"

// GoTypeOptions — codegen-ий төрөл сонголт.
type GoTypeOptions struct {
	// Numeric — numeric/decimal баганын Go төрөл. Хоосон = "float64" (Khan Vision
	// ERP-ийн одоогийн model-уудтай ижил). DB тал ҮРГЭЛЖ numeric(18,2) хэвээр.
	Numeric string
	// JSON — json/jsonb баганын төрөл. Хоосон = "datatypes.JSON" (gorm.io/datatypes).
	JSON string
}

// GoType — баганын Go төрөл ба шаардлагатай import. Nullable багана pointer
// болно (JSON/[]byte/slice-ээс бусад нь).
func GoType(c Column, o GoTypeOptions) (typ string, imports []string) {
	if o.Numeric == "" {
		o.Numeric = "float64"
	}
	if o.JSON == "" {
		o.JSON = "datatypes.JSON"
	}
	base, imp, nilable := baseGoType(c, o)
	if imp != "" {
		imports = append(imports, imp)
	}
	if c.Array {
		pq := []string{"github.com/lib/pq"}
		switch base {
		case "int16", "int32", "int64":
			return "pq.Int64Array", pq
		case "float32", "float64":
			return "pq.Float64Array", pq
		case "bool":
			return "pq.BoolArray", pq
		default:
			return "pq.StringArray", pq
		}
	}
	if c.Nullable && !nilable {
		base = "*" + base
	}
	return base, imports
}

func baseGoType(c Column, o GoTypeOptions) (typ, imp string, nilable bool) {
	if len(c.Enum) > 0 {
		return "string", "", false
	}
	switch strings.ToLower(c.UDT) {
	case "int2":
		return "int16", "", false
	case "int4", "serial", "serial4":
		return "int32", "", false
	case "int8", "bigserial", "serial8":
		return "int64", "", false
	case "float4":
		return "float32", "", false
	case "float8":
		return "float64", "", false
	case "numeric", "money":
		switch o.Numeric {
		case "decimal.Decimal":
			return o.Numeric, "github.com/shopspring/decimal", false
		default:
			return o.Numeric, "", false
		}
	case "bool":
		return "bool", "", false
	case "date", "timestamp", "timestamptz", "time", "timetz":
		return "time.Time", "time", false
	case "json", "jsonb":
		if strings.HasPrefix(o.JSON, "datatypes.") {
			return o.JSON, "gorm.io/datatypes", true
		}
		return o.JSON, "", true
	case "bytea":
		return "[]byte", "", true
	default:
		// uuid, text, varchar, bpchar, citext, inet, interval, geometry, tsvector …
		return "string", "", false
	}
}

// IsMoney — мөнгөн дүнгийн дүрэм (numeric(18,2)) хангагдсан эсэх.
func IsMoney(c Column) bool {
	return strings.EqualFold(c.UDT, "numeric") && c.Precision != nil && c.Scale != nil &&
		*c.Precision == 18 && *c.Scale == 2
}
