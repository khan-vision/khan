package money

import (
	"encoding/json"
	"math/big"
	"strings"
	"testing"
)

// FuzzParseFormat — ямар ч оролтод panic гарахгүй; амжилттай задарсан бол
// String→Parse тогтвортой, Human→Parse (таслалгүй) ижил, JSON roundtrip ижил, жишиг big.Rat-тай таарна.
func FuzzParseFormat(f *testing.F) {
	for _, s := range []string{"0", "-0", "1234.50", "0.005", "1e2", ".5", "1.", " 1", "9999999999999999.99", "-9999999999999999.99",
		"1e-9999", "١٢٣", "1.2.3", "+1", "00012.3400", "1E+2", "12345e-2", "abc", ""} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		a, err := Parse(s)
		_, _ = ParseLoose(s)
		for _, m := range allModes {
			_, _ = ParseRounded(s, m)
		}
		if err != nil {
			return
		}
		if a.Minor() > MaxMinor || a.Minor() < -MaxMinor {
			t.Fatalf("хязгаараас гадна: %q → %d", s, a.Minor())
		}
		out := a.String()
		b, err := Parse(out)
		if err != nil || b != a {
			t.Fatalf("roundtrip %q → %q → %v,%v", s, out, b, err)
		}
		if h, err := Parse(strings.ReplaceAll(a.Human(), ",", "")); err != nil || h != a {
			t.Fatalf("human roundtrip %q → %q", s, a.Human())
		}
		j, err := json.Marshal(a)
		var c Amount
		if err != nil || json.Unmarshal(j, &c) != nil || c != a {
			t.Fatalf("json roundtrip %q → %s", s, j)
		}
		// жишиг: strict задралт ParseRounded(mode)-той тэнцүү (яг утгад)
		for _, m := range allModes {
			if r, err := ParseRounded(s, m); err != nil || r != a {
				t.Fatalf("ParseRounded(%q,%v) = %v,%v; Parse = %v", s, m, r, err, a)
			}
		}
		if rat, ok := new(big.Rat).SetString(s); ok {
			want := new(big.Rat).Mul(rat, big.NewRat(100, 1))
			if !want.IsInt() || want.Num().Int64() != a.Minor() {
				t.Fatalf("big.Rat зөрсөн %q: %v vs %d", s, want, a.Minor())
			}
		}
	})
}

// FuzzFormatParse — аль ч минор нэгж String→Parse-оор буцна.
func FuzzFormatParse(f *testing.F) {
	for _, v := range []int64{0, 1, -1, 99, 100, MaxMinor, -MaxMinor, 123456789} {
		f.Add(v)
	}
	f.Fuzz(func(t *testing.T, v int64) {
		a, err := FromMinor(v)
		if err != nil {
			return
		}
		b, err := Parse(a.String())
		if err != nil || b != a {
			t.Fatalf("%d → %q → %v,%v", v, a.String(), b, err)
		}
		var s Amount
		if err := s.Scan(a.String()); err != nil || s != a {
			t.Fatalf("scan %d", v)
		}
	})
}

// FuzzAllocate — нийлбэр инвариант: Σ хэсэг == a, 0 харьцаа 0, хувь floor..floor+1.
func FuzzAllocate(f *testing.F) {
	f.Add(int64(10000), int64(1), int64(1), int64(1), int64(0))
	f.Add(int64(-1), int64(0), int64(5), int64(5), int64(0))
	f.Add(MaxMinor, int64(1<<62), int64(1<<62), int64(3), int64(7))
	f.Add(int64(5), int64(3), int64(0), int64(0), int64(0))
	f.Fuzz(func(t *testing.T, minor, r1, r2, r3, r4 int64) {
		a, err := FromMinor(minor)
		if err != nil {
			return
		}
		ratios := []int64{r1, r2, r3, r4}
		for _, r := range ratios {
			if r < 0 {
				if _, err := a.Allocate(ratios...); err == nil {
					t.Fatal("сөрөг харьцаа зөвшөөрөгдсөн")
				}
				return
			}
		}
		if r1+r2+r3+r4 == 0 && r1 == 0 && r2 == 0 && r3 == 0 && r4 == 0 {
			if _, err := a.Allocate(ratios...); err == nil {
				t.Fatal("бүгд 0 зөвшөөрөгдсөн")
			}
			return
		}
		checkAllocation(t, a, ratios)
		n := int(uint64(r1)%17) + 1
		parts, err := a.Split(n)
		if err != nil {
			t.Fatal(err)
		}
		if s, err := Sum(parts...); err != nil || s != a {
			t.Fatalf("Split нийлбэр %v ≠ %v (%v)", s, a, err)
		}
	})
}

// FuzzRounding — MulRate/Div нь жишиг big.Rat дугуйлалттай таарна.
func FuzzRounding(f *testing.F) {
	f.Add(int64(12345), int64(10_000_000), int64(7), uint8(1))
	f.Add(int64(-5), int64(50_000_000), int64(-2), uint8(2))
	f.Fuzz(func(t *testing.T, minor, rate, n int64, mode uint8) {
		a, err := FromMinor(minor)
		if err != nil {
			return
		}
		m := Rounding(mode%7) + 1
		if r, err := RateFromRaw(rate); err == nil {
			want := refRound(new(big.Int).Mul(big.NewInt(minor), big.NewInt(rate)), big.NewInt(rateUnit), m)
			got, err := a.MulRate(r, m)
			if want.IsInt64() && want.Int64() <= MaxMinor && want.Int64() >= -MaxMinor {
				if err != nil || got.Minor() != want.Int64() {
					t.Fatalf("MulRate %d×%d %v = %v,%v want %v", minor, rate, m, got.Minor(), err, want)
				}
			} else if err == nil {
				t.Fatalf("MulRate overflow хүлээсэн %d×%d", minor, rate)
			}
		}
		if n != 0 {
			want := refRound(big.NewInt(minor), big.NewInt(n), m)
			got, err := a.Div(n, m)
			if err != nil || got.Minor() != want.Int64() {
				t.Fatalf("Div %d/%d %v = %v,%v want %v", minor, n, m, got.Minor(), err, want)
			}
		}
	})
}
