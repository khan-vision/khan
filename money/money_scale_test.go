package money

import (
	"errors"
	"strings"
	"testing"
)

func mon(t testing.TB, s string, c Currency) Money {
	t.Helper()
	m, err := New(mustA(t, s), c)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func joinM(ms []Money) string {
	s := make([]string, len(ms))
	for i, m := range ms {
		s[i] = m.Amount.String()
	}
	return strings.Join(s, ",")
}

// TestMoneyScaleSplitAllocate — JPY/KRW бүхэл нэгжээр, MNT/USD 2 оронтой хуваагдана.
func TestMoneyScaleSplitAllocate(t *testing.T) {
	cases := []struct {
		amt  string
		cur  Currency
		n    int
		want string
	}{
		{"10", JPY, 3, "4.00,3.00,3.00"},
		{"10", KRW, 3, "4.00,3.00,3.00"},
		{"-10", JPY, 3, "-4.00,-3.00,-3.00"},
		{"10", MNT, 3, "3.34,3.33,3.33"},
		{"10", USD, 3, "3.34,3.33,3.33"},
		{"0", JPY, 2, "0.00,0.00"},
	}
	for _, c := range cases {
		m := mon(t, c.amt, c.cur)
		parts, err := m.Split(c.n)
		if err != nil || joinM(parts) != c.want {
			t.Errorf("%s Split(%d) = %s,%v; want %s", m, c.n, joinM(parts), err, c.want)
		}
		ones := make([]int64, c.n)
		for i := range ones {
			ones[i] = 1
		}
		al, err := m.Allocate(ones...)
		if err != nil || joinM(al) != c.want {
			t.Errorf("%s Allocate(1..) = %s,%v", m, joinM(al), err)
		}
	}
	al, err := mon(t, "1000", JPY).Allocate(60, 40)
	if err != nil || joinM(al) != "600.00,400.00" {
		t.Error(joinM(al), err)
	}
	if _, err := mon(t, "10", JPY).Allocate(); !errors.Is(err, ErrInvalidAllocation) {
		t.Error(err)
	}
	if _, err := mon(t, "10.50", JPY).Split(3); !errors.Is(err, ErrPrecision) {
		t.Errorf("JPY 10.50 split: %v", err)
	}
	if _, err := (Money{mustA(t, "10"), "XXX"}).Split(3); !errors.Is(err, ErrUnknownCurrency) {
		t.Error(err)
	}
}

// TestMoneyScaleDivMulRate — Div/MulRate валютын орон хүртэл дугуйлна.
func TestMoneyScaleDivMulRate(t *testing.T) {
	r15 := mustR(t, "0.15")
	cases := []struct {
		amt  string
		cur  Currency
		mode Rounding
		div  int64
		wd   string
		wm   string
	}{
		{"10", JPY, HalfUp, 3, "3.00", "2.00"}, // 10×0.15=1.5 → 2
		{"10", JPY, HalfEven, 3, "3.00", "2.00"},
		{"10", JPY, HalfDown, 3, "3.00", "1.00"},
		{"10", JPY, Down, 4, "2.00", "1.00"},
		{"10", JPY, Up, 4, "3.00", "2.00"},
		{"-10", KRW, Floor, 3, "-4.00", "-2.00"},
		{"-10", KRW, Ceil, 3, "-3.00", "-1.00"},
		{"10", MNT, HalfUp, 3, "3.33", "1.50"},
		{"10", USD, Up, 3, "3.34", "1.50"},
	}
	for _, c := range cases {
		m := mon(t, c.amt, c.cur)
		d, err := m.Div(c.div, c.mode)
		if err != nil || d.Amount.String() != c.wd || d.Valid() != nil {
			t.Errorf("%s Div(%d,%v) = %v,%v; want %s", m, c.div, c.mode, d, err, c.wd)
		}
		x, err := m.MulRate(r15, c.mode)
		if err != nil || x.Amount.String() != c.wm || x.Valid() != nil {
			t.Errorf("%s MulRate(0.15,%v) = %v,%v; want %s", m, c.mode, x, err, c.wm)
		}
	}
	if _, err := mon(t, "10", JPY).Div(0, HalfUp); !errors.Is(err, ErrDivByZero) {
		t.Error(err)
	}
	if _, err := mon(t, "10", JPY).Div(3, 0); !errors.Is(err, ErrInvalidRounding) {
		t.Error(err)
	}
}

// TestMoneyUnmarshalScale — JPY "10.50" JSON оролт ErrPrecision.
func TestMoneyUnmarshalScale(t *testing.T) {
	var m Money
	if err := m.UnmarshalJSON([]byte(`{"amount":"10.50","currency":"JPY"}`)); !errors.Is(err, ErrPrecision) {
		t.Errorf("want ErrPrecision, got %v", err)
	}
	if err := m.UnmarshalJSON([]byte(`{"amount":"10","currency":"JPY"}`)); err != nil || m.Amount.String() != "10.00" {
		t.Error(m, err)
	}
}

// FuzzMoneyScale — аль ч валют/горимд Split/Allocate/Div/MulRate үр дүн Valid, Split/Allocate нийлбэр яг.
func FuzzMoneyScale(f *testing.F) {
	f.Add(int64(1000), uint8(4), uint8(0), int64(3), int64(15_000_000), uint8(0))
	f.Add(int64(-1000), uint8(5), uint8(1), int64(7), int64(-33_333_333), uint8(3))
	f.Add(int64(1), uint8(0), uint8(2), int64(1), int64(0), uint8(6))
	f.Fuzz(func(t *testing.T, minor int64, ci, np uint8, den, rate int64, mode uint8) {
		curs := Currencies()
		c := curs[int(ci)%len(curs)]
		a, err := FromMinor(minor)
		if err != nil {
			return
		}
		info, _ := c.Info()
		if info.Scale < Scale {
			a.v -= a.v % 100 // JPY/KRW-д хүчинтэй дүн
		}
		m := Money{a, c}
		if m.Valid() != nil {
			t.Fatalf("бэлтгэл буруу %v", m)
		}
		n := int(np)%7 + 1
		parts, err := m.Split(n)
		if err != nil {
			t.Fatal(err)
		}
		checkSum(t, m, parts)
		ratios := make([]int64, n)
		for i := range ratios {
			ratios[i] = int64(i%3) + 1
		}
		parts, err = m.Allocate(ratios...)
		if err != nil {
			t.Fatal(err)
		}
		checkSum(t, m, parts)
		rm := allModes[int(mode)%len(allModes)]
		if den != 0 {
			if d, err := m.Div(den, rm); err == nil && d.Valid() != nil {
				t.Fatalf("Div %v/%d = %v хүчингүй", m, den, d)
			}
		}
		if r, err := RateFromRaw(rate); err == nil {
			if x, err := m.MulRate(r, rm); err == nil && x.Valid() != nil {
				t.Fatalf("MulRate %v×%v = %v хүчингүй", m, r, x)
			}
		}
	})
}

func checkSum(t *testing.T, m Money, parts []Money) {
	t.Helper()
	sum := Money{Currency: m.Currency}
	for _, p := range parts {
		if err := p.Valid(); err != nil {
			t.Fatalf("%v хэсэг %v: %v", m, p, err)
		}
		var err error
		if sum, err = sum.Add(p); err != nil {
			t.Fatal(err)
		}
	}
	if sum.Amount != m.Amount {
		t.Fatalf("нийлбэр %v ≠ %v", sum, m)
	}
}
