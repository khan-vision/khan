package money

import (
	"errors"
	"math/big"
	"math/rand"
	"testing"
)

func TestAddSubSum(t *testing.T) {
	max, min := Amount{MaxMinor}, Amount{-MaxMinor}
	one := mustA(t, "0.01")
	if s, err := mustA(t, "0.10").Add(mustA(t, "0.20")); err != nil || s.String() != "0.30" {
		t.Error(s, err)
	}
	if s, err := mustA(t, "1").Sub(mustA(t, "2.5")); err != nil || s.String() != "-1.50" {
		t.Error(s, err)
	}
	if s, err := max.Add(Zero); err != nil || s != max {
		t.Error(s, err)
	}
	if _, err := max.Add(one); !errors.Is(err, ErrOverflow) {
		t.Error(err)
	}
	if _, err := min.Sub(one); !errors.Is(err, ErrOverflow) {
		t.Error(err)
	}
	if _, err := max.Sub(min); !errors.Is(err, ErrOverflow) {
		t.Error(err)
	}
	if _, err := max.Add(max); !errors.Is(err, ErrOverflow) {
		t.Error(err)
	}
	if s, err := max.Add(min); err != nil || !s.IsZero() {
		t.Error(s, err)
	}
	if s, err := Sum(); err != nil || !s.IsZero() {
		t.Error(s, err)
	}
	if s, err := Sum(mustA(t, "0.1"), mustA(t, "0.2"), mustA(t, "0.3")); err != nil || s.String() != "0.60" {
		t.Error(s, err)
	}
	if _, err := Sum(max, one); !errors.Is(err, ErrOverflow) {
		t.Error(err)
	}
	if s, err := Sum(max, one.Neg(), one); err != nil || s != max { // завсар хэтрээгүй
		t.Error(s, err)
	}
}

func TestMulInt(t *testing.T) {
	for _, c := range []struct {
		a    string
		n    int64
		want string
		err  error
	}{
		{"12.34", 3, "37.02", nil}, {"12.34", 0, "0.00", nil}, {"-12.34", 3, "-37.02", nil}, {"12.34", -3, "-37.02", nil},
		{"-12.34", -3, "37.02", nil}, {"0.01", 99999999999999999, "999999999999999.99", nil},
		{"0.01", 1_000_000_000_000_000_000, "", ErrOverflow}, {"9999999999999999.99", 2, "", ErrOverflow},
		{"9999999999999999.99", 1, "9999999999999999.99", nil}, {"9999999999999999.99", -1, "-9999999999999999.99", nil},
		{"1", -1 << 63, "", ErrOverflow}, {"0", -1 << 63, "0.00", nil}, {"0.01", 1<<63 - 1, "", ErrOverflow},
	} {
		got, err := mustA(t, c.a).MulInt(c.n)
		if !errors.Is(err, c.err) || (err == nil && got.String() != c.want) {
			t.Errorf("%s×%d = %v,%v; want %s,%v", c.a, c.n, got, err, c.want, c.err)
		}
	}
}

func TestMulRate(t *testing.T) {
	type c struct {
		a, r string
		mode Rounding
		want string
	}
	for _, x := range []c{
		{"100", "0.1", HalfUp, "10.00"}, {"123.45", "0.1", HalfUp, "12.35"}, {"123.45", "0.1", HalfEven, "12.34"},
		{"123.45", "0.1", Down, "12.34"}, {"123.45", "0.1", Up, "12.35"}, {"-123.45", "0.1", HalfUp, "-12.35"},
		{"-123.45", "0.1", HalfEven, "-12.34"}, {"-123.45", "0.1", Floor, "-12.35"}, {"-123.45", "0.1", Ceil, "-12.34"},
		{"123.45", "0.1", Floor, "12.34"}, {"123.45", "0.1", Ceil, "12.35"}, {"123.45", "0.1", HalfDown, "12.34"},
		{"999.99", "0.105", HalfUp, "105.00"}, {"999.90", "0.105", HalfUp, "104.99"}, {"0.01", "0.5", HalfUp, "0.01"}, {"0.01", "0.5", HalfEven, "0.00"},
		{"0.03", "0.5", HalfEven, "0.02"}, {"100", "0", HalfUp, "0.00"}, {"100", "-0.1", HalfUp, "-10.00"},
		{"100", "1.5", HalfUp, "150.00"}, {"0.01", "0.00000001", Up, "0.01"}, {"0.01", "0.00000001", HalfUp, "0.00"},
		{"1000000", "0.15", HalfUp, "150000.00"}, {"9999999999999999.99", "1", HalfUp, "9999999999999999.99"},
	} {
		got, err := mustA(t, x.a).MulRate(mustR(t, x.r), x.mode)
		if err != nil || got.String() != x.want {
			t.Errorf("%s×%s %v = %v,%v; want %s", x.a, x.r, x.mode, got, err, x.want)
		}
	}
	if _, err := mustA(t, "9999999999999999.99").MulRate(mustR(t, "1.01"), HalfUp); !errors.Is(err, ErrOverflow) {
		t.Error(err)
	}
	if _, err := mustA(t, "1").MulRate(mustR(t, "1"), 0); !errors.Is(err, ErrInvalidRounding) {
		t.Error(err)
	}
	if _, err := mustA(t, "1").MulRate(mustR(t, "1"), Rounding(99)); !errors.Is(err, ErrInvalidRounding) {
		t.Error(err)
	}
}

func TestDiv(t *testing.T) {
	for _, c := range []struct {
		a    string
		n    int64
		mode Rounding
		want string
	}{
		{"10", 3, HalfUp, "3.33"}, {"10", 3, Up, "3.34"}, {"10", -3, HalfUp, "-3.33"}, {"-10", 3, Floor, "-3.34"},
		{"0.05", 2, HalfUp, "0.03"}, {"0.05", 2, HalfEven, "0.02"}, {"0.07", 2, HalfEven, "0.04"}, {"0.05", 2, HalfDown, "0.02"},
		{"1", 1, HalfUp, "1.00"}, {"-1", -1, HalfUp, "1.00"}, {"9999999999999999.99", 1, HalfUp, "9999999999999999.99"},
		{"9999999999999999.99", -1, HalfUp, "-9999999999999999.99"}, {"0", 5, HalfUp, "0.00"}, {"1", 1 << 62, Up, "0.01"},
	} {
		got, err := mustA(t, c.a).Div(c.n, c.mode)
		if err != nil || got.String() != c.want {
			t.Errorf("%s/%d %v = %v,%v; want %s", c.a, c.n, c.mode, got, err, c.want)
		}
	}
	if _, err := mustA(t, "1").Div(0, HalfUp); !errors.Is(err, ErrDivByZero) {
		t.Error(err)
	}
	if _, err := mustA(t, "1").Div(2, 0); !errors.Is(err, ErrInvalidRounding) {
		t.Error(err)
	}
}

func TestTaxHelpers(t *testing.T) {
	vat := mustR(t, "0.1")
	got, err := mustA(t, "1000").AddRate(vat, HalfUp)
	if err != nil || got.String() != "1100.00" {
		t.Error(got, err)
	}
	net, tax, err := mustA(t, "1100").SplitGross(vat, HalfUp)
	if err != nil || net.String() != "1000.00" || tax.String() != "100.00" {
		t.Error(net, tax, err)
	}
	// татвар орсон 99.99: tax = 99.99×0.1/1.1 = 9.09 → net = 90.90; нийлбэр ЯГ.
	gross := mustA(t, "99.99")
	net, tax, err = gross.SplitGross(vat, HalfUp)
	if err != nil || tax.String() != "9.09" || net.String() != "90.90" {
		t.Error(net, tax, err)
	}
	if s, _ := net.Add(tax); s != gross {
		t.Error("net+tax != gross")
	}
	if _, err := mustA(t, "5").IncludedPart(mustR(t, "-1"), HalfUp); !errors.Is(err, ErrDivByZero) {
		t.Error(err)
	}
	if _, err := (Amount{MaxMinor}).AddRate(vat, HalfUp); !errors.Is(err, ErrOverflow) {
		t.Error(err)
	}
	p, err := ParsePercent("7.5")
	if err != nil {
		t.Fatal(err)
	}
	if g, _ := mustA(t, "200").MulRate(p, HalfUp); g.String() != "15.00" {
		t.Error(g)
	}
}

// Дугуйлалтыг жишиг (big.Rat) хэрэгжүүлэлттэй тулгана.
func TestRoundingAgainstReference(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 20000; i++ {
		a := rng.Int63n(2*MaxMinor+1) - MaxMinor
		if i%3 == 0 {
			a = rng.Int63n(2001) - 1000
		}
		r := rng.Int63n(2_000_000_001) - 1_000_000_000 // ±10.0
		if i%5 == 0 {
			r = (rng.Int63n(41) - 20) * 5_000_000 // 0.05 алхамтай → олон тэнцүү хагас
		}
		mode := allModes[rng.Intn(len(allModes))]
		num := new(big.Int).Mul(big.NewInt(a), big.NewInt(r))
		want := refRound(num, big.NewInt(rateUnit), mode)
		got, err := Amount{a}.MulRate(Rate{r}, mode)
		if !want.IsInt64() || want.Int64() > MaxMinor || want.Int64() < -MaxMinor {
			if !errors.Is(err, ErrOverflow) {
				t.Fatalf("%d×%d %v: overflow хүлээсэн, %v", a, r, mode, err)
			}
			continue
		}
		if err != nil || got.Minor() != want.Int64() {
			t.Fatalf("%d×%d %v = %v,%v; want %v", a, r, mode, got.Minor(), err, want)
		}
		n := rng.Int63n(2001) - 1000
		if n == 0 {
			n = 7
		}
		want = refRound(big.NewInt(a), big.NewInt(n), mode)
		got, err = Amount{a}.Div(n, mode)
		if err != nil || got.Minor() != want.Int64() {
			t.Fatalf("%d/%d %v = %v,%v; want %v", a, n, mode, got.Minor(), err, want)
		}
	}
}

func TestAllocate(t *testing.T) {
	type c struct {
		a      string
		ratios []int64
		want   []string
	}
	for _, x := range []c{
		{"100", []int64{1, 1, 1}, []string{"33.34", "33.33", "33.33"}},
		{"100", []int64{60, 40}, []string{"60.00", "40.00"}},
		{"0.05", []int64{3, 7}, []string{"0.02", "0.03"}}, // 0.015 / 0.035 → үлдэгдэл 0.4/0.6
		{"0.05", []int64{1, 1}, []string{"0.03", "0.02"}}, // тэнцвэл эхний индекс
		{"0.01", []int64{1, 1, 1}, []string{"0.01", "0.00", "0.00"}},
		{"-100", []int64{1, 1, 1}, []string{"-33.34", "-33.33", "-33.33"}},
		{"10", []int64{0, 5, 0, 5}, []string{"0.00", "5.00", "0.00", "5.00"}},
		{"10", []int64{0, 1, 0}, []string{"0.00", "10.00", "0.00"}},
		{"0", []int64{1, 2}, []string{"0.00", "0.00"}},
		{"100", []int64{7}, []string{"100.00"}},
		{"1", []int64{1 << 62, 1 << 62, 1 << 62}, []string{"0.34", "0.33", "0.33"}},
		{"1000.01", []int64{50, 30, 20}, []string{"500.01", "300.00", "200.00"}},
		{"9999999999999999.99", []int64{1, 1}, []string{"5000000000000000.00", "4999999999999999.99"}},
	} {
		got, err := mustA(t, x.a).Allocate(x.ratios...)
		if err != nil || len(got) != len(x.want) {
			t.Errorf("Allocate(%s,%v) = %v,%v", x.a, x.ratios, got, err)
			continue
		}
		for i := range got {
			if got[i].String() != x.want[i] {
				t.Errorf("Allocate(%s,%v)[%d] = %s, want %s", x.a, x.ratios, i, got[i], x.want[i])
			}
		}
	}
	for _, bad := range [][]int64{nil, {}, {0}, {0, 0}, {-1, 2}, {1, -1}} {
		if _, err := mustA(t, "10").Allocate(bad...); !errors.Is(err, ErrInvalidAllocation) {
			t.Errorf("Allocate(%v) err = %v", bad, err)
		}
	}
	if _, err := mustA(t, "10").Allocate(make([]int64, MaxParts+1)...); !errors.Is(err, ErrInvalidAllocation) {
		t.Error(err)
	}
	rates, err := mustA(t, "100").AllocateRates(mustR(t, "0.333"), mustR(t, "0.667"))
	if err != nil || rates[0].String() != "33.30" || rates[1].String() != "66.70" {
		t.Error(rates, err)
	}
}

func TestSplit(t *testing.T) {
	got, err := mustA(t, "10").Split(3)
	if err != nil || got[0].String() != "3.34" || got[1].String() != "3.33" || got[2].String() != "3.33" {
		t.Error(got, err)
	}
	got, _ = mustA(t, "-0.02").Split(3)
	if got[0].String() != "-0.01" || got[1].String() != "-0.01" || got[2].String() != "0.00" {
		t.Error(got)
	}
	got, _ = Amount{MaxMinor}.Split(1)
	if len(got) != 1 || got[0].Minor() != MaxMinor {
		t.Error(got)
	}
	for _, n := range []int{0, -1, MaxParts + 1} {
		if _, err := mustA(t, "10").Split(n); !errors.Is(err, ErrInvalidAllocation) {
			t.Errorf("Split(%d) err = %v", n, err)
		}
	}
	// Split == Allocate(1,1,...)
	rng := rand.New(rand.NewSource(2))
	for i := 0; i < 500; i++ {
		a := Amount{rng.Int63n(2*MaxMinor+1) - MaxMinor}
		n := rng.Intn(40) + 1
		s, _ := a.Split(n)
		al, _ := a.Allocate(ones(n)...)
		for j := range s {
			if s[j] != al[j] {
				t.Fatalf("Split≠Allocate %v n=%d j=%d", a, n, j)
			}
		}
	}
}

func ones(n int) []int64 {
	r := make([]int64, n)
	for i := range r {
		r[i] = 1
	}
	return r
}

// checkAllocation — Allocate-ийн инвариантууд (fuzz-ээс ч дуудна).
func checkAllocation(t testing.TB, a Amount, ratios []int64) {
	t.Helper()
	got, err := a.Allocate(ratios...)
	if err != nil {
		t.Fatalf("Allocate(%v,%v): %v", a, ratios, err)
	}
	sum := new(big.Int)
	for _, r := range ratios {
		sum.Add(sum, big.NewInt(r))
	}
	var total int64
	for i, g := range got {
		total += g.Minor()
		if a.Sign() >= 0 && g.Sign() < 0 || a.Sign() <= 0 && g.Sign() > 0 {
			t.Fatalf("тэмдэг зөрсөн: %v → %v", a, got)
		}
		if ratios[i] == 0 && !g.IsZero() {
			t.Fatalf("0 харьцаа ≠ 0: %v %v %v", a, ratios, got)
		}
		// floor ≤ |хувь| ≤ floor+1
		exact := new(big.Int).Mul(big.NewInt(a.Abs().Minor()), big.NewInt(ratios[i]))
		fl := new(big.Int).Div(exact, sum)
		d := new(big.Int).Sub(big.NewInt(g.Abs().Minor()), fl)
		if d.Sign() < 0 || d.Cmp(big.NewInt(1)) > 0 {
			t.Fatalf("хувь хэт зөрсөн: %v %v %v", a, ratios, got)
		}
	}
	if total != a.Minor() {
		t.Fatalf("нийлбэр %d ≠ %d (ratios %v)", total, a.Minor(), ratios)
	}
}

func TestAllocateProperty(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	for i := 0; i < 5000; i++ {
		a := Amount{rng.Int63n(2*MaxMinor+1) - MaxMinor}
		if i%2 == 0 {
			a = Amount{rng.Int63n(20001) - 10000}
		}
		n := rng.Intn(12) + 1
		ratios := make([]int64, n)
		nonzero := false
		for j := range ratios {
			switch rng.Intn(4) {
			case 0:
			case 1:
				ratios[j] = rng.Int63n(10)
			case 2:
				ratios[j] = rng.Int63n(1_000_000)
			default:
				ratios[j] = rng.Int63()
			}
			nonzero = nonzero || ratios[j] > 0
		}
		if !nonzero {
			ratios[0] = 1
		}
		checkAllocation(t, a, ratios)
	}
}
