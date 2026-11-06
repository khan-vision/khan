package money

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
)

// Rate — яг decimal харьцаа/хувь (татвар, хүү, коэффициент): 8 орон хүртэл, дотроо
// int64 × 10^-8. Жишээ: ParseRate("0.105") = 10.5%, ParsePercent("10.5") ижил утга.
// Zero value = 0. Татварын хуулийг энд кодлоогүй — зөвхөн ерөнхий тооцоо.
type Rate struct{ v int64 }

const (
	// RateScale — Rate-ийн бутархайн орон.
	RateScale = 8
	rateUnit  = int64(100_000_000) // 1.0
)

// ParseRate — "0.105", "1", "0.00000001" (≤8 орон; 8-аас олон → ErrPrecision). Синтакс Parse-тай ижил.
func ParseRate(s string) (Rate, error) {
	v, err := parseScaled(s, RateScale, false, 0)
	return Rate{v}, err
}

// ParsePercent — хувиар: "10" → 0.10, "7.5" → 0.075 (≤6 орон хувь).
func ParsePercent(s string) (Rate, error) {
	v, err := parseScaled(s, RateScale-2, false, 0) // p% = p×10^-2 ⇒ ижил бүхэл тоо
	return Rate{v}, err
}

// RateFromRaw — 10^-8 нэгжээс (1_000_000 = 0.01 = 1%).
func RateFromRaw(raw int64) (Rate, error) {
	if raw > maxRaw || raw < -maxRaw {
		return Rate{}, fmt.Errorf("%w: %d", ErrOverflow, raw)
	}
	return Rate{raw}, nil
}

// Raw — 10^-8 нэгжээр.
func (r Rate) Raw() int64 { return r.v }

// String — хамгийн богино plain хэлбэр: "0.105", "1", "-0.05", "0".
func (r Rate) String() string { return formatScaled(r.v, RateScale, true) }

// Percent — хувиар ("10.5" ⇒ 0.105). Хамгийн богино хэлбэр.
func (r Rate) Percent() string { return formatScaled(r.v, RateScale-2, true) }

// Sign / IsZero / Cmp / Neg — Amount-тай ижил утгатай.
func (r Rate) Sign() int {
	switch {
	case r.v < 0:
		return -1
	case r.v > 0:
		return 1
	}
	return 0
}

// IsZero — 0 эсэх.
func (r Rate) IsZero() bool { return r.v == 0 }

// Cmp — r<o → -1, == → 0, > → 1.
func (r Rate) Cmp(o Rate) int {
	switch {
	case r.v < o.v:
		return -1
	case r.v > o.v:
		return 1
	}
	return 0
}

// Neg — эсрэг тэмдэгтэй.
func (r Rate) Neg() Rate { return Rate{-r.v} }

// MarshalText — String(). MarshalJSON нь string гаргана ("0.105").
func (r Rate) MarshalText() ([]byte, error) { return []byte(r.String()), nil }

// UnmarshalText — ParseRate.
func (r *Rate) UnmarshalText(b []byte) error {
	v, err := ParseRate(string(b))
	if err != nil {
		return err
	}
	*r = v
	return nil
}

// MarshalJSON — JSON string ("0.105").
func (r Rate) MarshalJSON() ([]byte, error) { return json.Marshal(r.String()) }

// UnmarshalJSON — JSON string эсвэл тоог яг хүлээнэ; null нь no-op.
func (r *Rate) UnmarshalJSON(b []byte) error {
	s, null, err := jsonDecimal(b)
	if err != nil || null {
		return err
	}
	v, err := ParseRate(s)
	if err != nil {
		return err
	}
	*r = v
	return nil
}

// Value — driver.Valuer: яг текст ("0.105").
func (r Rate) Value() (driver.Value, error) { return r.String(), nil }

// Scan — sql.Scanner (Amount.Scan-тай ижил оролтын төрлүүд; >8 орон → ErrPrecision).
func (r *Rate) Scan(src any) error {
	v, err := scanScaled(src, RateScale)
	if err != nil {
		return err
	}
	*r = Rate{v}
	return nil
}

// GormDataType — gorm.io/gorm/schema.GormDataTypeInterface.
func (Rate) GormDataType() string { return "numeric(18,8)" }
