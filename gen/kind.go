package gen

import (
	"fmt"
	"strings"

	"github.com/khan-vision/khan/query"
	"github.com/khan-vision/khan/schema"
	"github.com/khan-vision/khan/validate"
)

// kindInfo — баганын төрлөөс гарсан суурь Kind + Rules.
type kindInfo struct {
	Kind  validate.Kind
	Rules validate.Rules
	Note  string // талбарын дээр гарах мэдээллийн тайлбар
}

// baseKind — DB баганын төрлийг validate.Kind болгоно. Дэмжигдэхгүй бол why хоосон биш.
//
// Хүснэгт (docs/GEN.md): numeric(18,2)→Money; scale=0 numeric→Int; бусад numeric→Number{Scale};
// int2/int4→Int (хязгаартай); int8→Int; bool→Bool; date→Date; timestamp[tz]→DateTime; time→Time;
// uuid→UUID; text→Text; varchar(n)/bpchar(n)→String{MaxLen n}; enum→Enum; json[b]→JSON.
func baseKind(c schema.Column) (ki kindInfo, why string) {
	if c.Array {
		return ki, "массив багана (" + c.Type + ") — crud дэмжихгүй"
	}
	if len(c.Enum) > 0 {
		ki.Kind = validate.Enum
		ki.Rules.Enum = append([]string(nil), c.Enum...)
		return ki, ""
	}
	udt := strings.ToLower(c.UDT)
	switch udt {
	case "int2":
		ki.Kind, ki.Rules.Min, ki.Rules.Max = validate.Int, validate.Bound(-32768), validate.Bound(32767)
	case "int4", "serial", "serial4":
		ki.Kind, ki.Rules.Min, ki.Rules.Max = validate.Int, validate.Bound(-2147483648), validate.Bound(2147483647)
	case "int8", "bigserial", "serial8":
		ki.Kind = validate.Int
	case "numeric":
		switch {
		case schema.IsMoney(c):
			ki.Kind = validate.Money
		case c.Scale == nil:
			ki.Kind = validate.Number
		case *c.Scale == 0:
			ki.Kind = validate.Int
			ki.Note = "numeric(" + fmt.Sprint(prec(c)) + ",0) → Int"
		default:
			ki.Kind = validate.Number
			ki.Rules.Scale = *c.Scale
			if *c.Scale > validate.MaxNumberScale {
				ki.Rules.Scale = validate.MaxNumberScale
				ki.Note = fmt.Sprintf("%s: scale %d > %d тул %d болгов", c.Type, *c.Scale, validate.MaxNumberScale, validate.MaxNumberScale)
			}
		}
	case "float4", "float8":
		ki.Kind = validate.Number
	case "bool":
		ki.Kind = validate.Bool
	case "date":
		ki.Kind = validate.Date
	case "timestamp", "timestamptz":
		ki.Kind = validate.DateTime
	case "time", "timetz":
		ki.Kind = validate.Time
	case "uuid":
		ki.Kind = validate.UUID
	case "text", "citext":
		ki.Kind = validate.Text
	case "varchar", "bpchar":
		ki.Kind = validate.String
		if c.MaxLength != nil {
			ki.Rules.MaxLen = *c.MaxLength
		}
	case "json", "jsonb":
		ki.Kind = validate.JSON
	default:
		return ki, "дэмжигдээгүй төрөл (" + c.Type + ")"
	}
	return ki, ""
}

func prec(c schema.Column) int {
	if c.Precision != nil {
		return *c.Precision
	}
	return 0
}

// textual — String/Text/Email/Phone/Password (мөр хадгалагдах) эсэх.
func textual(k validate.Kind) bool {
	switch k {
	case validate.String, validate.Text, validate.Email, validate.Phone, validate.Password:
		return true
	}
	return false
}

// numericKind — Int/Number/Money.
func numericKind(k validate.Kind) bool {
	return k == validate.Int || k == validate.Number || k == validate.Money
}

// filterKind — Kind → query.FilterKind ("" = шүүлтгүй).
func filterKind(k validate.Kind) query.FilterKind {
	switch k {
	case validate.String, validate.Text, validate.Email, validate.Phone:
		return query.KindText
	case validate.Int, validate.Number, validate.Money:
		return query.KindNumber
	case validate.Date, validate.DateTime:
		return query.KindDate
	case validate.Enum:
		return query.KindEnum
	case validate.Bool:
		return query.KindBool
	case validate.UUID:
		return query.KindUUID
	}
	return ""
}

// searchable — ILIKE хайлтад тохирох (текст) багана. uuid/enum дээр ILIKE ажиллахгүй.
func searchable(c schema.Column) bool {
	if c.Array || len(c.Enum) > 0 {
		return false
	}
	switch strings.ToLower(c.UDT) {
	case "text", "varchar", "bpchar", "citext":
		return true
	}
	return false
}

// kindConst — validate.Kind → Go тогтмолын нэр.
func kindConst(k validate.Kind) string {
	switch k {
	case validate.String:
		return "String"
	case validate.Text:
		return "Text"
	case validate.Int:
		return "Int"
	case validate.Number:
		return "Number"
	case validate.Money:
		return "Money"
	case validate.Bool:
		return "Bool"
	case validate.Date:
		return "Date"
	case validate.DateTime:
		return "DateTime"
	case validate.Time:
		return "Time"
	case validate.UUID:
		return "UUID"
	case validate.Enum:
		return "Enum"
	case validate.JSON:
		return "JSON"
	case validate.Password:
		return "Password"
	case validate.Phone:
		return "Phone"
	case validate.Email:
		return "Email"
	}
	return string(k)
}

// filterConstructor — query.FilterKind → query.<Ctor>.
func filterConstructor(k query.FilterKind) string {
	switch k {
	case query.KindText:
		return "Text"
	case query.KindNumber:
		return "Number"
	case query.KindDate:
		return "Date"
	case query.KindEnum:
		return "Enum"
	case query.KindBool:
		return "Bool"
	case query.KindUUID:
		return "UUID"
	}
	return "Text"
}
