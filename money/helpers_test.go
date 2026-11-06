package money

import (
	"math/big"
	"testing"
)

// mustA — тестэд Parse хийж алдаа бол унагаана.
func mustA(t testing.TB, s string) Amount {
	t.Helper()
	a, err := Parse(s)
	if err != nil {
		t.Fatalf("Parse(%q): %v", s, err)
	}
	return a
}

func mustR(t testing.TB, s string) Rate {
	t.Helper()
	r, err := ParseRate(s)
	if err != nil {
		t.Fatalf("ParseRate(%q): %v", s, err)
	}
	return r
}

// refRound — divRound-оос ХАМААРААГҮЙ жишиг: big.Rat + floor дээр суурилсан дугуйлалт.
func refRound(num, den *big.Int, mode Rounding) *big.Int {
	x := new(big.Rat).SetFrac(num, den)
	f := new(big.Int).Div(x.Num(), x.Denom()) // Euclid: den>0 тул floor
	frac := new(big.Rat).Sub(x, new(big.Rat).SetInt(f))
	if frac.Sign() == 0 {
		return f
	}
	c := new(big.Int).Add(f, big.NewInt(1))
	pos := x.Sign() > 0
	switch mode {
	case Floor:
		return f
	case Ceil:
		return c
	case Down:
		if pos {
			return f
		}
		return c
	case Up:
		if pos {
			return c
		}
		return f
	}
	switch frac.Cmp(big.NewRat(1, 2)) {
	case -1:
		return f
	case 1:
		return c
	}
	switch mode {
	case HalfUp:
		if pos {
			return c
		}
		return f
	case HalfDown:
		if pos {
			return f
		}
		return c
	default: // HalfEven
		if f.Bit(0) == 0 {
			return f
		}
		return c
	}
}

var allModes = []Rounding{HalfUp, HalfEven, HalfDown, Down, Up, Floor, Ceil}
