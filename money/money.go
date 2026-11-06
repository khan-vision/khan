package money

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
)

// Money — валюттай дүн. Өөр валютыг нэмэх/хасах/харьцуулахад ErrCurrencyMismatch.
// Zero value = 0.00 MNT. Currency "" нь MNT-тэй тэнцүү гэж тооцогдоно.
//
// Money нь database/sql Scanner/Valuer-гүй: DB-д Amount (numeric) + тусдаа currency багана хадгална.
type Money struct {
	Amount   Amount
	Currency Currency
}

// New — Money үүсгэнэ; валют үл мэдэгдэх бол ErrUnknownCurrency. Валют "" → MNT.
// Валютын орноос олон бутархай (ж: JPY 10.50) хадгалагдахыг зөвшөөрнө — Round/Valid-аар шалгана.
func New(a Amount, c Currency) (Money, error) {
	if _, err := c.Info(); err != nil {
		return Money{}, err
	}
	return Money{Amount: a, Currency: c.Code()}, nil
}

// cur — каноник, шалгагдсан валют.
func (m Money) cur() (CurrencyInfo, error) { return m.Currency.Info() }

// Valid — валют мэдэгдэх ба дүн валютын минор нэгжид яг багтах эсэх (JPY: бүхэл).
func (m Money) Valid() error {
	info, err := m.cur()
	if err != nil {
		return err
	}
	if !fitsScale(m.Amount, info.Scale) {
		return fmt.Errorf("%w: %s нь %s-д %d оронтой байх ёстой", ErrPrecision, m.Amount, info.Code, info.Scale)
	}
	return nil
}

func fitsScale(a Amount, scale int) bool { return scale >= Scale || a.v%100 == 0 }

func (m Money) same(o Money) (Currency, error) {
	a, err := m.cur()
	if err != nil {
		return "", err
	}
	b, err := o.cur()
	if err != nil {
		return "", err
	}
	if a.Code != b.Code {
		return "", fmt.Errorf("%w: %s ≠ %s", ErrCurrencyMismatch, a.Code, b.Code)
	}
	return a.Code, nil
}

func (m Money) with(a Amount, err error) (Money, error) {
	if err != nil {
		return Money{}, err
	}
	return Money{Amount: a, Currency: m.Currency.Code()}, nil
}

// Add — m+o (ижил валют).
func (m Money) Add(o Money) (Money, error) {
	if _, err := m.same(o); err != nil {
		return Money{}, err
	}
	return m.with(m.Amount.Add(o.Amount))
}

// Sub — m-o (ижил валют).
func (m Money) Sub(o Money) (Money, error) {
	if _, err := m.same(o); err != nil {
		return Money{}, err
	}
	return m.with(m.Amount.Sub(o.Amount))
}

// Cmp — ижил валют дээр харьцуулна.
func (m Money) Cmp(o Money) (int, error) {
	if _, err := m.same(o); err != nil {
		return 0, err
	}
	return m.Amount.Cmp(o.Amount), nil
}

// Equal — ижил валют ба ижил дүн (мэдэгдэхгүй валют бол false).
func (m Money) Equal(o Money) bool {
	c, err := m.Cmp(o)
	return err == nil && c == 0
}

// Neg / Abs — тэмдэг. Валют үл мэдэгдэх үед ч Amount-д нөлөөгүй.
func (m Money) Neg() Money { return Money{m.Amount.Neg(), m.Currency.Code()} }

// Abs — абсолют утга.
func (m Money) Abs() Money { return Money{m.Amount.Abs(), m.Currency.Code()} }

// IsZero — дүн 0 эсэх.
func (m Money) IsZero() bool { return m.Amount.IsZero() }

// MulInt — m×n.
func (m Money) MulInt(n int64) (Money, error) { return m.with(m.Amount.MulInt(n)) }

// unitOf — валютын минор нэгж Amount-ийн (1/100) хэдэн хувьтай тэнцэх: 2 оронтой валют 1, JPY/KRW 100.
func unitOf(info CurrencyInfo) int64 {
	u := int64(1)
	for s := info.Scale; s < Scale; s++ {
		u *= 10
	}
	return u
}

// scaleUp — минор нэгжийн тоог (валютын орноор) Amount-ийн 1/100 руу буцаана; хязгаараас хэтэрвэл ErrOverflow.
func scaleUp(q *big.Int, unit int64) (Amount, error) {
	return fromBig(new(big.Int).Mul(q, bigFrom(unit)))
}

// MulRate — m×r, mode-оор ВАЛЮТЫН минор нэгж хүртэл (MNT 2, JPY 0 орон) дугуйлна; үр дүн үргэлж Valid.
func (m Money) MulRate(r Rate, mode Rounding) (Money, error) {
	info, err := m.cur()
	if err != nil {
		return Money{}, err
	}
	unit := unitOf(info)
	num := new(big.Int).Mul(bigFrom(m.Amount.v), bigFrom(r.v))
	q, err := divRound(num, bigFrom(rateUnit*unit), mode)
	if err != nil {
		return Money{}, err
	}
	return m.with(scaleUp(q, unit))
}

// Div — m/n, mode-оор ВАЛЮТЫН минор нэгж хүртэл дугуйлна (JPY 10 / 3 → 3 иен); n==0 → ErrDivByZero.
func (m Money) Div(n int64, mode Rounding) (Money, error) {
	info, err := m.cur()
	if err != nil {
		return Money{}, err
	}
	unit := unitOf(info)
	den := new(big.Int).Mul(bigFrom(n), bigFrom(unit))
	q, err := divRound(bigFrom(m.Amount.v), den, mode)
	if err != nil {
		return Money{}, err
	}
	return m.with(scaleUp(q, unit))
}

// Round — дүнг валютын минор нэгжид (MNT 2, JPY 0) mode-оор дугуйлна.
func (m Money) Round(mode Rounding) (Money, error) {
	info, err := m.cur()
	if err != nil {
		return Money{}, err
	}
	if info.Scale >= Scale {
		if !mode.Valid() {
			return Money{}, ErrInvalidRounding
		}
		return Money{m.Amount, info.Code}, nil
	}
	q, err := m.Amount.Div(100, mode)
	if err != nil {
		return Money{}, err
	}
	return m.with(q.MulInt(100))
}

// Allocate — Amount.Allocate, гэхдээ валютын минор нэгжээр: JPY/KRW-д бүхэл иен/вонгоор хуваана,
// тиймээс нийлбэр m-тэй ЯГ тэнцэж, хэсэг бүр Valid. m өөрөө валютын оронд багтахгүй бол ErrPrecision.
func (m Money) Allocate(ratios ...int64) ([]Money, error) {
	return m.byUnit(func(a Amount) ([]Amount, error) { return a.Allocate(ratios...) })
}

// Split — Amount.Split, гэхдээ валютын минор нэгжээр (JPY 10 / 3 → 4, 3, 3 иен).
func (m Money) Split(n int) ([]Money, error) {
	return m.byUnit(func(a Amount) ([]Amount, error) { return a.Split(n) })
}

// byUnit — m-ийг валютын минор нэгж болгон багасгаж, f-ээр хуваагаад буцаан томруулна.
func (m Money) byUnit(f func(Amount) ([]Amount, error)) ([]Money, error) {
	if err := m.Valid(); err != nil {
		return nil, err
	}
	info, _ := m.cur() // Valid амжилттай тул алдаагүй
	unit := unitOf(info)
	parts, err := f(Amount{m.Amount.v / unit})
	if err != nil {
		return nil, err
	}
	for i := range parts {
		parts[i].v *= unit // |part| <= |m| тул халилтгүй
	}
	return m.wrap(parts, nil)
}

func (m Money) wrap(parts []Amount, err error) ([]Money, error) {
	if err != nil {
		return nil, err
	}
	if _, cerr := m.cur(); cerr != nil {
		return nil, cerr
	}
	out := make([]Money, len(parts))
	for i, p := range parts {
		out[i] = Money{p, m.Currency.Code()}
	}
	return out, nil
}

// String — "1234.50 MNT" (ISO кодтой, машин-уншигдахуйц).
func (m Money) String() string { return m.Amount.String() + " " + m.Currency.String() }

// Human — хүнд зориулсан: "1,234.50 ₮", "$1,234.50", "¥1,235". Валютын оронд багтахгүй
// бутархайтай дүнг (JPY 10.50) чимээгүй дугуйлахгүй, 2 оронтой харуулна. Валют мэдэгдэхгүй бол ISO кодоор.
func (m Money) Human() string {
	info, err := m.cur()
	if err != nil {
		return m.Amount.Human() + " " + string(m.Currency)
	}
	dec := info.Scale
	if !fitsScale(m.Amount, dec) {
		dec = Scale
	}
	body := humanize(m.Amount.v, dec)
	neg := ""
	if body[0] == '-' {
		neg, body = "-", body[1:]
	}
	if info.SymbolAfter {
		return neg + body + " " + info.Symbol
	}
	return neg + info.Symbol + body
}

type moneyJSON struct {
	Amount   *Amount  `json:"amount"`
	Currency Currency `json:"currency"`
}

// MarshalJSON — {"amount":"1234.50","currency":"MNT"}.
func (m Money) MarshalJSON() ([]byte, error) {
	if _, err := m.cur(); err != nil {
		return nil, err
	}
	return json.Marshal(moneyJSON{Amount: &m.Amount, Currency: m.Currency.Code()})
}

// UnmarshalJSON — {"amount": "12.5" | 12.5, "currency": "USD"}; currency байхгүй бол MNT,
// amount байхгүй бол ErrInvalid; үл мэдэгдэх валют → ErrUnknownCurrency; валютын оронд багтахгүй дүн
// (JPY "10.50") → ErrPrecision.
func (m *Money) UnmarshalJSON(b []byte) error {
	if string(bytes.TrimSpace(b)) == "null" {
		return nil // encoding/json конвенц: no-op
	}
	var raw moneyJSON
	if err := json.Unmarshal(b, &raw); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if raw.Amount == nil {
		return fmt.Errorf("%w: amount байхгүй", ErrInvalid)
	}
	c, err := ParseCurrency(string(raw.Currency))
	if err != nil {
		return err
	}
	got := Money{Amount: *raw.Amount, Currency: c}
	if err := got.Valid(); err != nil {
		return err // JPY/KRW-д бутархайтай дүн (10.50) → ErrPrecision
	}
	*m = got
	return nil
}
