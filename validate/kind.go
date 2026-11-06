// Package validate нь JSON талбарын утгыг төрлөөр нь задлан шалгаж, DB-д
// бичихэд бэлэн (normalize хийсэн) Go утга болгох цэвэр (DB-гүй) library юм.
//
// crud package-ийн талбар бүр `validate.Parse`-аар дамждаг. Мөнгө/тоо нь
// ХЭЗЭЭ Ч float-аар дамжихгүй — decimal мөр (`"1234.50"`) хэлбэрээр баригдана.
//
//	v, present, msg := validate.Parse(validate.Money, json.RawMessage(`"12.5"`),
//	    validate.Rules{Required: true}, "Үнэ")
//	// v == "12.50", present == true, msg == ""
package validate

// Kind — талбарын төрөл.
type Kind string

const (
	String   Kind = "string"
	Text     Kind = "text"
	Int      Kind = "int"
	Number   Kind = "number"
	Money    Kind = "money"
	Bool     Kind = "bool"
	Date     Kind = "date"
	DateTime Kind = "datetime"
	Time     Kind = "time"
	UUID     Kind = "uuid"
	Enum     Kind = "enum"
	JSON     Kind = "json"
	Password Kind = "password"
	Phone    Kind = "phone"
	Email    Kind = "email"
)

var allKinds = []Kind{String, Text, Int, Number, Money, Bool, Date, DateTime, Time, UUID, Enum, JSON, Password, Phone, Email}

// Kinds — дэмжигдсэн бүх төрөл (тогтвортой дараалал).
func Kinds() []Kind { return append([]Kind(nil), allKinds...) }

// Valid — дэмжигдсэн төрөл эсэх.
func (k Kind) Valid() bool {
	for _, x := range allKinds {
		if x == k {
			return true
		}
	}
	return false
}

// textual — мөр хэлбэрээр хадгалагдаж trim/урт/pattern шалгалт хэрэглэгдэх төрлүүд.
func (k Kind) textual() bool { return k == String || k == Text }
