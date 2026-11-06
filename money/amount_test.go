package money

import (
	"errors"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	ok := []struct{ in, want string }{
		{"0", "0.00"}, {"-0", "0.00"}, {"-0.00", "0.00"}, {"0.0", "0.00"},
		{"12", "12.00"}, {"12.5", "12.50"}, {"12.50", "12.50"}, {"1.500", "1.50"}, {"1.2300000", "1.23"},
		{"-1234.56", "-1234.56"}, {"007", "7.00"}, {"0.01", "0.01"}, {"-0.01", "-0.01"},
		{"1e2", "100.00"}, {"1E2", "100.00"}, {"1.5e1", "15.00"}, {"1e+2", "100.00"}, {"12345e-2", "123.45"},
		{"1000e-3", "1.00"}, {"0e5", "0.00"}, {"0.00e-999", "0.00"},
		{"9999999999999999.99", "9999999999999999.99"}, {"-9999999999999999.99", "-9999999999999999.99"},
		{"9999999999999999.990", "9999999999999999.99"}, {"99999999999999.99e2", "9999999999999999.00"},
	}
	for _, c := range ok {
		a, err := Parse(c.in)
		if err != nil {
			t.Errorf("Parse(%q): %v", c.in, err)
			continue
		}
		if a.String() != c.want {
			t.Errorf("Parse(%q) = %s, want %s", c.in, a, c.want)
		}
	}
	bad := []struct {
		in   string
		want error
	}{
		{"", ErrInvalid}, {" ", ErrInvalid}, {" 1", ErrInvalid}, {"1 ", ErrInvalid}, {"1 2", ErrInvalid},
		{"+1", ErrInvalid}, {".5", ErrInvalid}, {"1.", ErrInvalid}, {"-", ErrInvalid}, {".", ErrInvalid},
		{"--1", ErrInvalid}, {"1,000", ErrInvalid}, {"1_000", ErrInvalid}, {"abc", ErrInvalid}, {"NaN", ErrInvalid},
		{"Inf", ErrInvalid}, {"0x10", ErrInvalid}, {"1e", ErrInvalid}, {"1e+", ErrInvalid}, {"1e12345", ErrInvalid},
		{"1.2.3", ErrInvalid}, {"١٢٣", ErrInvalid}, {"１２", ErrInvalid}, {"1\x00", ErrInvalid}, {"1\n", ErrInvalid},
		{strings.Repeat("1", 300), ErrInvalid},
		{"0.005", ErrPrecision}, {"0.001", ErrPrecision}, {"1.234", ErrPrecision}, {"1e-3", ErrPrecision},
		{"-0.005", ErrPrecision}, {"1e-9999", ErrPrecision}, {"1e-1000", ErrPrecision},
		{"10000000000000000", ErrOverflow}, {"10000000000000000.00", ErrOverflow}, {"-10000000000000000", ErrOverflow},
		{"1e17", ErrOverflow}, {"1e9999", ErrOverflow}, {"9999999999999999.991", ErrPrecision},
	}
	for _, c := range bad {
		_, err := Parse(c.in)
		if !errors.Is(err, c.want) {
			t.Errorf("Parse(%q) err = %v, want %v", c.in, err, c.want)
		}
	}
}

func TestParseLoose(t *testing.T) {
	for in, want := range map[string]string{
		".5": "0.50", "1.": "1.00", "+1.5": "1.50", "  12.30\t": "12.30", "-.25": "-0.25", "+.5e1": "5.00",
	} {
		a, err := ParseLoose(in)
		if err != nil || a.String() != want {
			t.Errorf("ParseLoose(%q) = %v, %v; want %s", in, a, err, want)
		}
	}
	for _, in := range []string{"", " ", ".", "+", "1 2", "1,5", "0.005", "e5", ".e5"} {
		if _, err := ParseLoose(in); err == nil {
			t.Errorf("ParseLoose(%q) алдаагүй", in)
		}
	}
}

func TestParseRounded(t *testing.T) {
	type c struct {
		in   string
		mode Rounding
		want string
	}
	for _, x := range []c{
		{"0.005", HalfUp, "0.01"}, {"0.005", HalfEven, "0.00"}, {"0.015", HalfEven, "0.02"}, {"0.005", HalfDown, "0.00"},
		{"-0.005", HalfUp, "-0.01"}, {"-0.005", HalfDown, "0.00"}, {"0.001", Up, "0.01"}, {"-0.001", Up, "-0.01"},
		{"0.009", Down, "0.00"}, {"-0.009", Down, "0.00"}, {"-0.001", Floor, "-0.01"}, {"0.001", Floor, "0.00"},
		{"0.001", Ceil, "0.01"}, {"-0.009", Ceil, "0.00"}, {"1.005", HalfUp, "1.01"}, {"1.004999", HalfUp, "1.00"},
		{"1.00500001", HalfEven, "1.01"}, {"1e-5", HalfUp, "0.00"}, {"1e-5", Up, "0.01"}, {"0.0049", HalfUp, "0.00"},
		{"0.0051", HalfDown, "0.01"}, {"12", HalfUp, "12.00"}, {"9999999999999999.994", HalfUp, "9999999999999999.99"},
	} {
		a, err := ParseRounded(x.in, x.mode)
		if err != nil || a.String() != x.want {
			t.Errorf("ParseRounded(%q,%v) = %v,%v; want %s", x.in, x.mode, a, err, x.want)
		}
	}
	if _, err := ParseRounded("9999999999999999.995", HalfUp); !errors.Is(err, ErrOverflow) {
		t.Errorf("дугуйлсны дараа хэтэрсэн: %v", err)
	}
	if _, err := ParseRounded("1.005", 0); !errors.Is(err, ErrInvalidRounding) {
		t.Errorf("горимгүй: %v", err)
	}
}

func TestFormatHuman(t *testing.T) {
	for _, c := range []struct {
		minor        int64
		plain, human string
	}{
		{0, "0.00", "0.00"}, {5, "0.05", "0.05"}, {-5, "-0.05", "-0.05"}, {100, "1.00", "1.00"},
		{123450, "1234.50", "1,234.50"}, {-123450, "-1234.50", "-1,234.50"}, {99999900, "999999.00", "999,999.00"},
		{100000000, "1000000.00", "1,000,000.00"}, {MaxMinor, "9999999999999999.99", "9,999,999,999,999,999.99"},
		{-MaxMinor, "-9999999999999999.99", "-9,999,999,999,999,999.99"}, {100000, "1000.00", "1,000.00"},
	} {
		a, err := FromMinor(c.minor)
		if err != nil {
			t.Fatal(err)
		}
		if a.String() != c.plain || a.Human() != c.human {
			t.Errorf("%d: %q/%q, want %q/%q", c.minor, a.String(), a.Human(), c.plain, c.human)
		}
		if b := mustA(t, c.plain); b != a {
			t.Errorf("roundtrip %q: %v != %v", c.plain, b, a)
		}
	}
}

func TestConstructors(t *testing.T) {
	if _, err := FromMinor(MaxMinor + 1); !errors.Is(err, ErrOverflow) {
		t.Error(err)
	}
	if _, err := FromMinor(-MaxMinor - 1); !errors.Is(err, ErrOverflow) {
		t.Error(err)
	}
	if a, err := FromInt(5); err != nil || a.String() != "5.00" {
		t.Error(a, err)
	}
	if a, err := FromInt(9999999999999999); err != nil || a.Minor() != 999999999999999900 {
		t.Error(a, err)
	}
	if _, err := FromInt(10000000000000000); !errors.Is(err, ErrOverflow) {
		t.Error(err)
	}
	var z Amount
	if !z.IsZero() || z != Zero || z.String() != "0.00" || z.Sign() != 0 {
		t.Error("zero value")
	}
}

func TestFloat(t *testing.T) {
	for in, want := range map[float64]string{0: "0.00", 12.5: "12.50", 0.1: "0.10", -3.07: "-3.07", 1e14: "100000000000000.00", 19.99: "19.99"} {
		a, err := FromFloat(in)
		if err != nil || a.String() != want {
			t.Errorf("FromFloat(%v) = %v,%v; want %s", in, a, err, want)
		}
	}
	x, y := 0.1, 0.2 // константа биш, жинхэнэ float64 нэмэлт
	for _, f := range []float64{x + y, 1.005, 1e15, 1e300, 1.0 / 3} {
		if _, err := FromFloat(f); !errors.Is(err, ErrPrecision) {
			t.Errorf("FromFloat(%v) err = %v", f, err)
		}
	}
	nan, inf := zeroF()/zeroF(), 1/zeroF()
	for _, f := range []float64{nan, inf, -inf} {
		if _, err := FromFloat(f); !errors.Is(err, ErrInvalid) {
			t.Errorf("FromFloat(%v) err = %v", f, err)
		}
		if _, err := FromFloatRound(f, HalfUp); !errors.Is(err, ErrInvalid) {
			t.Errorf("FromFloatRound(%v) err = %v", f, err)
		}
	}
	if a, err := FromFloatRound(x+y, HalfUp); err != nil || a.String() != "0.30" {
		t.Error(a, err)
	}
	if a, err := FromFloatRound(2.675, HalfUp); err != nil || a.String() != "2.68" {
		t.Error(a, err) // shortest repr "2.675" → 2.68 (float-ийн 2.67499.. биш)
	}
	if a := mustA(t, "1234.5"); a.Float64() != 1234.5 {
		t.Error(a.Float64())
	}
}

func zeroF() float64 { return 0 }

func TestCmpSignAbsNeg(t *testing.T) {
	a, b := mustA(t, "-5.00"), mustA(t, "3.25")
	if a.Cmp(b) != -1 || b.Cmp(a) != 1 || a.Cmp(a) != 0 || a.Sign() != -1 || b.Sign() != 1 {
		t.Error("cmp/sign")
	}
	if a.Abs() != mustA(t, "5") || a.Neg() != mustA(t, "5") || Zero.Neg() != Zero || b.Abs() != b {
		t.Error("abs/neg")
	}
	if Min(a, b) != a || Max(a, b) != b || Min(b, a) != a || Max(a, b) != b {
		t.Error("min/max")
	}
	m := Amount{MaxMinor}
	if m.Neg().Neg() != m || m.Neg().Abs() != m {
		t.Error("limit neg")
	}
}
