package money

import (
	"fmt"
	"math/big"
	"strings"
)

// Rounding — дугуйлалтын горим. Zero value (заагаагүй) нь ХҮЧИНГҮЙ: дугуйлалт
// шаардах үйлдэл бүр горимыг тодорхой заах ёстой (ErrInvalidRounding).
//
// Бүх горим тэмдэгт-тэгш хэмтэй (Floor/Ceil-ээс бусад): "away" = тэгээс холдох.
type Rounding uint8

const (
	_ Rounding = iota
	// HalfUp — хагас нь тэгээс холдоно (арилжааны дугуйлалт): 2.5→3, -2.5→-3.
	HalfUp
	// HalfEven — хагас нь тэгш рүү (banker's): 2.5→2, 3.5→4. Олон дугуйлалтын хуримтлагдсан хазайлтыг бууруулна.
	HalfEven
	// HalfDown — хагас нь тэг рүү: 2.5→2, -2.5→-2.
	HalfDown
	// Down — тэг рүү таслах (truncate): 2.9→2, -2.9→-2.
	Down
	// Up — тэгээс холдох (0 биш үлдэгдэл байвал): 2.1→3, -2.1→-3.
	Up
	// Floor — сөрөг хязгааргүй рүү: 2.9→2, -2.1→-3.
	Floor
	// Ceil — эерэг хязгааргүй рүү: 2.1→3, -2.9→-2.
	Ceil
)

// Valid — горим тодорхой заагдсан эсэх.
func (m Rounding) Valid() bool { return m >= HalfUp && m <= Ceil }

// String — горимын нэр ("half_up", "half_even", ...).
func (m Rounding) String() string {
	switch m {
	case HalfUp:
		return "half_up"
	case HalfEven:
		return "half_even"
	case HalfDown:
		return "half_down"
	case Down:
		return "down"
	case Up:
		return "up"
	case Floor:
		return "floor"
	case Ceil:
		return "ceil"
	}
	return fmt.Sprintf("rounding(%d)", uint8(m))
}

// ParseRounding — String()-ийн урвуу (том/жижиг үсэг үл ялгана). Тохиргооны файлд ашиглана.
func ParseRounding(s string) (Rounding, error) {
	for m := HalfUp; m <= Ceil; m++ {
		if strings.EqualFold(s, m.String()) {
			return m, nil
		}
	}
	return 0, fmt.Errorf("%w: %q", ErrInvalidRounding, s)
}

// roundsAway — хасагдсан хэсэг (0 биш) байгаа үед абсолют утгыг +1 болгох эсэх.
// half: үлдэгдэл vs 0.5 (-1 бага, 0 тэнцүү, 1 их); odd: хадгалагдах бүхэл сондгой эсэх;
// neg: нийт утга сөрөг эсэх. inexact=false (үлдэгдэл 0) бол хэзээ ч нэмэхгүй.
func roundsAway(mode Rounding, neg bool, half int, inexact, odd bool) bool {
	if !inexact {
		return false
	}
	switch mode {
	case HalfUp:
		return half >= 0
	case HalfDown:
		return half > 0
	case HalfEven:
		return half > 0 || (half == 0 && odd)
	case Up:
		return true
	case Floor:
		return neg
	case Ceil:
		return !neg
	}
	return false // Down
}

// divRound — num/den-ийг бүхэл болгож mode-оор дугуйлна (den != 0).
func divRound(num, den *big.Int, mode Rounding) (*big.Int, error) {
	if !mode.Valid() {
		return nil, ErrInvalidRounding
	}
	if den.Sign() == 0 {
		return nil, ErrDivByZero
	}
	neg := num.Sign()*den.Sign() < 0
	an := new(big.Int).Abs(num)
	ad := new(big.Int).Abs(den)
	q, r := new(big.Int).QuoRem(an, ad, new(big.Int))
	if r.Sign() != 0 {
		half := r.Lsh(r, 1).Cmp(ad)
		if roundsAway(mode, neg, half, true, q.Bit(0) == 1) {
			q.Add(q, big.NewInt(1))
		}
	}
	if neg {
		q.Neg(q)
	}
	return q, nil
}
