// Package money — мөнгөн дүнгийн яг (float-гүй) төрөл: Amount, Rate, Money.
//
// # Шийдвэр
//
//   - Amount нь Postgres numeric(18,2)-тэй 1:1 — дотроо int64 "минор нэгж" (1/100). Хамгийн их
//     ±9999999999999999.99 нь int64-д багтаж, нэмэх/хасахад wrap хийхгүй. Бүх Amount
//     numeric(18,2)-д ХАДГАЛАГДАХ боломжтой (хязгаараас хэтрэх үйлдэл ErrOverflow буцаана).
//   - Amount нь валют агуулахгүй (багана бүр нэг валюттай, валют нь тусдаа багана/талбар).
//     Валюттай дүн хэрэгтэй үед Money{Amount, Currency} ашиглана — зөрүүтэй валютыг
//     нэмэх/харьцуулахад ErrCurrencyMismatch буцаана.
//   - Amount struct (нууц талбартай) тул `Amount(5)` гэж cast хийж хүчингүй утга үүсгэх боломжгүй;
//     zero value нь 0.00. == харьцуулалт зөв.
//   - JSON-д string ("1234.50") гаргана; оролтод string эсвэл тоо (1234.5) хоёуланг нь яг хүлээнэ.
//   - Алдаа нь panic биш — error буцаана; чимээгүй wrap/дугуйлалт байхгүй.
package money

import (
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
)

// Тогтмолууд.
const (
	// Scale — Amount-ийн бутархайн орон (numeric(18,2)).
	Scale = 2
	// MaxMinor — Amount-ийн хамгийн их минор нэгж (9999999999999999.99).
	MaxMinor int64 = maxRaw
	// MaxParts — Allocate/Split-ийн хамгийн их хэсгийн тоо.
	MaxParts = 1 << 20
)

// Amount — numeric(18,2) мөнгөн дүн (минор нэгжээр, 1/100). Zero value = 0.00.
type Amount struct{ v int64 }

// Zero — 0.00.
var Zero = Amount{}

// FromMinor — минор нэгжээс (ж: 123450 → 1234.50). |minor| > MaxMinor бол ErrOverflow.
func FromMinor(minor int64) (Amount, error) {
	if minor > maxRaw || minor < -maxRaw {
		return Amount{}, fmt.Errorf("%w: %d", ErrOverflow, minor)
	}
	return Amount{minor}, nil
}

// FromInt — бүхэл нэгжээс (ж: 5 → 5.00).
func FromInt(units int64) (Amount, error) {
	if units > maxRaw/100 || units < -maxRaw/100 {
		return Amount{}, fmt.Errorf("%w: %d", ErrOverflow, units)
	}
	return Amount{units * 100}, nil
}

// Parse — яг decimal текстээс: `-?\d+(\.\d+)?([eE][+-]?\d+)?` (validate package-тай ижил).
// 2-оос олон бутархай орон (хойд тэггүй) → ErrPrecision; "1e2" = 100.00; "+1", ".5", "1.", зай,
// мянгатын таслал, unicode цифр → ErrInvalid. Давхар тэмдэгт "-0" = 0.00.
func Parse(s string) (Amount, error) {
	v, err := parseScaled(s, Scale, false, 0)
	return Amount{v}, err
}

// ParseLoose — Parse + гадна ASCII зай, "+" тэмдэг, ".5", "1." (гараар оруулсан/импорт өгөгдөлд).
func ParseLoose(s string) (Amount, error) {
	v, err := parseScaled(s, Scale, true, 0)
	return Amount{v}, err
}

// ParseRounded — Parse, гэхдээ 2-оос олон оронг mode-оор дугуйлна (гадаад системийн өгөгдөл).
func ParseRounded(s string, mode Rounding) (Amount, error) {
	if !mode.Valid() {
		return Amount{}, ErrInvalidRounding
	}
	v, err := parseScaled(s, Scale, false, mode)
	return Amount{v}, err
}

// floatText — float64-ийг хамгийн богино decimal текст болгоно (NaN/Inf-ийг үгүйсгэнэ).
func floatText(f float64) (string, error) {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return "", fmt.Errorf("%w: %v", ErrInvalid, f)
	}
	return strconv.FormatFloat(f, 'f', -1, 64), nil
}

// FromFloat — float64-ээс, ЗӨВХӨН яг байх үед: хамгийн богино decimal дүрслэл нь ≤2 оронтой ба
// |f| < 1e15 (float64 15 цифр хүртэл найдвартай). 0.1+0.2 (=0.30000000000000004) → ErrPrecision.
// Хуучин float64 кодоос шилжихэд FromFloatRound-ыг ашигла.
func FromFloat(f float64) (Amount, error) {
	s, err := floatText(f)
	if err != nil {
		return Amount{}, err
	}
	if math.Abs(f) >= 1e15 {
		return Amount{}, fmt.Errorf("%w: %v", ErrPrecision, f)
	}
	return Parse(s)
}

// FromFloatRound — float64-ийн хамгийн богино decimal дүрслэлийг mode-оор дугуйлна.
// Хуучин float64 кодыг шилжүүлэх түр гүүр; шинэ кодод хэрэглэхгүй.
func FromFloatRound(f float64, mode Rounding) (Amount, error) {
	s, err := floatText(f)
	if err != nil {
		return Amount{}, err
	}
	return ParseRounded(s, mode)
}

// Minor — минор нэгж (1234.50 → 123450).
func (a Amount) Minor() int64 { return a.v }

// Float64 — ойролцоо float64. ЗӨВХӨН дэлгэц/хуучин интерфейст; тооцоонд БҮҮ ашигла.
func (a Amount) Float64() float64 { return float64(a.v) / 100 }

// String — яг 2 оронтой plain текст: "1234.50", "-0.05", "0.00". DB/API-ийн каноник хэлбэр.
func (a Amount) String() string { return formatScaled(a.v, Scale, false) }

// Human — мянгатын таслалтай: "1,234.50", "-1,234,567.00".
func (a Amount) Human() string { return humanize(a.v, Scale) }

// humanize — v (минор, 2 орон)-ийг decimals оронтой (0 эсвэл 2) таслалтай текст болгоно.
// decimals=0 үед v % 100 == 0 байх ёстой (дуудагч шалгана).
func humanize(v int64, decimals int) string {
	s := formatScaled(v, Scale, false) // "-1234.50"
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	ip, fp, _ := strings.Cut(s, ".")
	out := group(ip)
	if decimals > 0 {
		out += "." + fp
	}
	if neg {
		out = "-" + out
	}
	return out
}

// Sign — -1, 0, 1.
func (a Amount) Sign() int {
	switch {
	case a.v < 0:
		return -1
	case a.v > 0:
		return 1
	}
	return 0
}

// IsZero — 0.00 эсэх.
func (a Amount) IsZero() bool { return a.v == 0 }

// Cmp — a<b → -1, a==b → 0, a>b → 1.
func (a Amount) Cmp(b Amount) int {
	switch {
	case a.v < b.v:
		return -1
	case a.v > b.v:
		return 1
	}
	return 0
}

// Neg — эсрэг тэмдэгтэй (хязгаар тэгш хэмтэй тул хэзээ ч overflow болохгүй).
func (a Amount) Neg() Amount { return Amount{-a.v} }

// Abs — абсолют утга.
func (a Amount) Abs() Amount {
	if a.v < 0 {
		return Amount{-a.v}
	}
	return a
}

// Min / Max — хоёр дүнгээс бага / их нь.
func Min(a, b Amount) Amount {
	if b.v < a.v {
		return b
	}
	return a
}

// Max — Min-ийн эсрэг.
func Max(a, b Amount) Amount {
	if b.v > a.v {
		return b
	}
	return a
}

// Add — a+b; хязгаараас хэтэрвэл ErrOverflow.
func (a Amount) Add(b Amount) (Amount, error) { return FromMinor(a.v + b.v) }

// Sub — a-b; хязгаараас хэтэрвэл ErrOverflow.
func (a Amount) Sub(b Amount) (Amount, error) { return FromMinor(a.v - b.v) }

// Sum — бүх дүнгийн нийлбэр (хоосон бол 0.00); завсарт хязгаараас хэтэрвэл ErrOverflow.
func Sum(amounts ...Amount) (Amount, error) {
	var s int64
	for _, a := range amounts {
		s += a.v // |s| <= maxRaw, |a.v| <= maxRaw → wrap-гүй
		if s > maxRaw || s < -maxRaw {
			return Amount{}, ErrOverflow
		}
	}
	return Amount{s}, nil
}

// bigFrom — int64-ийг *big.Int болгоно.
func bigFrom(v int64) *big.Int { return big.NewInt(v) }

// fromBig — *big.Int-ийг Amount болгоно (хязгаар шалгана).
func fromBig(b *big.Int) (Amount, error) {
	if !b.IsInt64() {
		return Amount{}, ErrOverflow
	}
	return FromMinor(b.Int64())
}
