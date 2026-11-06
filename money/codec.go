package money

import (
	"bytes"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
)

// jsonDecimal — JSON string эсвэл тооноос decimal текст гаргана. null → null=true.
// Бусад төрөл (bool, object, array) → ErrInvalid.
func jsonDecimal(b []byte) (s string, null bool, err error) {
	b = bytes.TrimSpace(b)
	switch {
	case len(b) == 0:
		return "", false, fmt.Errorf("%w: хоосон JSON", ErrInvalid)
	case string(b) == "null":
		return "", true, nil
	case b[0] == '"':
		if err := json.Unmarshal(b, &s); err != nil {
			return "", false, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		return s, false, nil
	case b[0] == '-' || (b[0] >= '0' && b[0] <= '9'):
		return string(b), false, nil
	}
	return "", false, fmt.Errorf("%w: JSON string/тоо биш: %s", ErrInvalid, clip(string(b)))
}

// scanScaled — database/sql Scan-ийн оролтыг (string, []byte, int64, float64) 10^-scale минор нэгж болгоно.
// NULL → ErrNull. float64 нь яг (хамгийн богино дүрслэл нь scale оронд багтах, |f|<1e15) үед л.
func scanScaled(src any, scale int) (int64, error) {
	switch x := src.(type) {
	case nil:
		return 0, ErrNull
	case string:
		return parseScaled(x, scale, false, 0)
	case []byte:
		return parseScaled(string(x), scale, false, 0)
	case int64:
		return parseScaled(strconv.FormatInt(x, 10), scale, false, 0)
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) || math.Abs(x) >= 1e15 {
			return 0, fmt.Errorf("%w: float64 %v", ErrPrecision, x)
		}
		return parseScaled(strconv.FormatFloat(x, 'f', -1, 64), scale, false, 0)
	}
	return 0, fmt.Errorf("%w: дэмжигдээгүй төрөл %T", ErrInvalid, src)
}

// MarshalText — String(). Map түлхүүр, form, YAML-д.
func (a Amount) MarshalText() ([]byte, error) { return []byte(a.String()), nil }

// UnmarshalText — Parse (strict).
func (a *Amount) UnmarshalText(b []byte) error {
	v, err := Parse(string(b))
	if err != nil {
		return err
	}
	*a = v
	return nil
}

// MarshalJSON — JSON string: "1234.50". String сонгосон шалтгаан: JS-ийн Number (float64) 2^53-аас
// дээш яг биш тул дүнг тоогоор гаргахгүй. Оролтод тоо ч хүлээнэ.
func (a Amount) MarshalJSON() ([]byte, error) { return json.Marshal(a.String()) }

// UnmarshalJSON — JSON string ("12.5") эсвэл тоо (12.5, 1e2)-г яг задална. null нь no-op
// (encoding/json-ийн конвенц; NULL-ийг NullAmount-аар төлөөлнө). Бусад төрөл → ErrInvalid.
func (a *Amount) UnmarshalJSON(b []byte) error {
	s, null, err := jsonDecimal(b)
	if err != nil || null {
		return err
	}
	v, err := Parse(s)
	if err != nil {
		return err
	}
	*a = v
	return nil
}

// JSONNumber — crud-ийн decimal overlay-тай нийцтэй JSON тоо (json.Number): "1234.50".
func (a Amount) JSONNumber() json.Number { return json.Number(a.String()) }

// Value — driver.Valuer: яг текст ("1234.50") — float-аар дамжихгүй.
func (a Amount) Value() (driver.Value, error) { return a.String(), nil }

// Scan — sql.Scanner. string/[]byte/int64 (бүхэл нэгж)/float64 (яг үед) хүлээнэ; NULL → ErrNull
// (NULL-тэй баганад NullAmount эсвэл *Amount); 2-оос олон оронтой утга → ErrPrecision.
func (a *Amount) Scan(src any) error {
	v, err := scanScaled(src, Scale)
	if err != nil {
		return err
	}
	*a = Amount{v}
	return nil
}

// GormDataType — gorm.io/gorm/schema.GormDataTypeInterface; AutoMigrate-д numeric(18,2).
func (Amount) GormDataType() string { return "numeric(18,2)" }

// NullAmount — NULL байж болох Amount (sql.NullString-тэй адил).
type NullAmount struct {
	Amount Amount
	Valid  bool
}

// NullAmountOf — Valid=true Amount.
func NullAmountOf(a Amount) NullAmount { return NullAmount{Amount: a, Valid: true} }

// Scan — NULL → Valid=false.
func (n *NullAmount) Scan(src any) error {
	if src == nil {
		*n = NullAmount{}
		return nil
	}
	if err := n.Amount.Scan(src); err != nil {
		*n = NullAmount{}
		return err
	}
	n.Valid = true
	return nil
}

// Value — Valid=false бол NULL.
func (n NullAmount) Value() (driver.Value, error) {
	if !n.Valid {
		return nil, nil
	}
	return n.Amount.Value()
}

// MarshalJSON — Valid=false бол null.
func (n NullAmount) MarshalJSON() ([]byte, error) {
	if !n.Valid {
		return []byte("null"), nil
	}
	return n.Amount.MarshalJSON()
}

// UnmarshalJSON — null → Valid=false; үгүй бол Amount.
func (n *NullAmount) UnmarshalJSON(b []byte) error {
	if string(bytes.TrimSpace(b)) == "null" {
		*n = NullAmount{}
		return nil
	}
	if err := n.Amount.UnmarshalJSON(b); err != nil {
		return err
	}
	n.Valid = true
	return nil
}

// GormDataType — numeric(18,2).
func (NullAmount) GormDataType() string { return "numeric(18,2)" }
