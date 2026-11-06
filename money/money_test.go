package money

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestCurrency(t *testing.T) {
	for c, sc := range map[Currency]int{MNT: 2, USD: 2, EUR: 2, CNY: 2, JPY: 0, KRW: 0, RUB: 2, "": 2} {
		if got, err := c.Scale(); err != nil || got != sc {
			t.Errorf("%q.Scale() = %d,%v; want %d", c, got, err, sc)
		}
	}
	if _, err := Currency("XXX").Scale(); !errors.Is(err, ErrUnknownCurrency) {
		t.Error(err)
	}
	if _, err := Currency("mnt").Info(); !errors.Is(err, ErrUnknownCurrency) { // ParseCurrency л том болгоно
		t.Error(err)
	}
	for in, want := range map[string]Currency{"": MNT, " usd ": USD, "Jpy": JPY, "MNT": MNT} {
		if c, err := ParseCurrency(in); err != nil || c != want {
			t.Errorf("ParseCurrency(%q) = %v,%v", in, c, err)
		}
	}
	if _, err := ParseCurrency("KWD"); !errors.Is(err, ErrUnknownCurrency) {
		t.Error(err)
	}
	if Currency("").String() != "MNT" || Currency("").Code() != MNT {
		t.Error("default")
	}
	seen := map[Currency]bool{}
	for _, c := range Currencies() {
		info, err := c.Info()
		if err != nil || info.Code != c || info.Symbol == "" || info.Name == "" || seen[c] {
			t.Errorf("%s: %+v %v", c, info, err)
		}
		if info.Scale != 0 && info.Scale != 2 {
			t.Errorf("%s scale %d", c, info.Scale)
		}
		seen[c] = true
	}
}

func TestMoneyBasics(t *testing.T) {
	mnt := func(s string) Money { m, err := New(mustA(t, s), MNT); must(t, err); return m }
	usd := func(s string) Money { m, err := New(mustA(t, s), USD); must(t, err); return m }
	if s, err := mnt("1.10").Add(mnt("2.20")); err != nil || s.String() != "3.30 MNT" {
		t.Error(s, err)
	}
	if s, err := (Money{Amount: mustA(t, "1")}).Add(mnt("2")); err != nil || s.Currency != MNT || s.Amount.String() != "3.00" {
		t.Error(s, err) // "" ≡ MNT
	}
	if _, err := mnt("1").Add(usd("1")); !errors.Is(err, ErrCurrencyMismatch) {
		t.Error(err)
	}
	if _, err := mnt("1").Sub(usd("1")); !errors.Is(err, ErrCurrencyMismatch) {
		t.Error(err)
	}
	if _, err := mnt("1").Cmp(usd("1")); !errors.Is(err, ErrCurrencyMismatch) {
		t.Error(err)
	}
	if mnt("1").Equal(usd("1")) || !mnt("1").Equal(Money{Amount: mustA(t, "1")}) || (Money{Currency: "ZZZ"}).Equal(Money{Currency: "ZZZ"}) {
		t.Error("equal")
	}
	if c, err := mnt("1").Cmp(mnt("2")); err != nil || c != -1 {
		t.Error(c, err)
	}
	if _, err := (Money{Currency: "ZZZ"}).Add(Money{Currency: "ZZZ"}); !errors.Is(err, ErrUnknownCurrency) {
		t.Error(err)
	}
	if _, err := New(Zero, "ZZZ"); !errors.Is(err, ErrUnknownCurrency) {
		t.Error(err)
	}
	if m, err := New(Zero, ""); err != nil || m.Currency != MNT {
		t.Error(m, err)
	}
	if mnt("5").Neg().Amount.String() != "-5.00" || mnt("-5").Abs().Amount.String() != "5.00" || !mnt("0").IsZero() {
		t.Error("neg/abs")
	}
	if _, err := (Money{Amount: Amount{MaxMinor}}).Add(mnt("0.01")); !errors.Is(err, ErrOverflow) {
		t.Error(err)
	}
	if m, err := mnt("10").MulInt(3); err != nil || m.Amount.String() != "30.00" {
		t.Error(m, err)
	}
	if m, err := usd("10").MulRate(mustR(t, "0.075"), HalfUp); err != nil || m.String() != "0.75 USD" {
		t.Error(m, err)
	}
	if m, err := usd("10").Div(3, HalfUp); err != nil || m.String() != "3.33 USD" {
		t.Error(m, err)
	}
	parts, err := usd("10").Split(3)
	if err != nil || len(parts) != 3 || parts[0].String() != "3.34 USD" {
		t.Error(parts, err)
	}
	parts, err = usd("10").Allocate(1, 3)
	if err != nil || parts[0].String() != "2.50 USD" || parts[1].String() != "7.50 USD" {
		t.Error(parts, err)
	}
	if _, err := (Money{Currency: "ZZZ"}).Split(2); !errors.Is(err, ErrUnknownCurrency) {
		t.Error(err)
	}
	if _, err := usd("10").Split(0); !errors.Is(err, ErrInvalidAllocation) {
		t.Error(err)
	}
}

func must(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestMoneyHumanRoundValid(t *testing.T) {
	m := func(s string, c Currency) Money { x, err := New(mustA(t, s), c); must(t, err); return x }
	for _, c := range []struct {
		m    Money
		want string
	}{
		{m("1234.5", MNT), "1,234.50 ₮"}, {m("-1234.5", MNT), "-1,234.50 ₮"}, {m("0", MNT), "0.00 ₮"},
		{m("1234.5", USD), "$1,234.50"}, {m("-1234.5", USD), "-$1,234.50"}, {m("1234.5", EUR), "€1,234.50"},
		{m("1235", JPY), "¥1,235"}, {m("1235", KRW), "₩1,235"}, {m("10.50", JPY), "¥10.50"}, {m("99", RUB), "99.00 ₽"},
		{m("-1000000", CNY), "-CN¥1,000,000.00"}, {Money{Amount: mustA(t, "1.5")}, "1.50 ₮"},
		{Money{Amount: mustA(t, "1.5"), Currency: "ZZZ"}, "1.50 ZZZ"},
	} {
		if got := c.m.Human(); got != c.want {
			t.Errorf("Human = %q, want %q", got, c.want)
		}
	}
	if err := m("10", JPY).Valid(); err != nil {
		t.Error(err)
	}
	if err := m("10.50", JPY).Valid(); !errors.Is(err, ErrPrecision) {
		t.Error(err)
	}
	if err := m("10.50", USD).Valid(); err != nil {
		t.Error(err)
	}
	if err := (Money{Currency: "ZZZ"}).Valid(); !errors.Is(err, ErrUnknownCurrency) {
		t.Error(err)
	}
	for _, c := range []struct {
		in   string
		mode Rounding
		want string
	}{
		{"10.50", HalfUp, "11.00"}, {"10.50", HalfEven, "10.00"}, {"11.50", HalfEven, "12.00"}, {"-10.50", HalfUp, "-11.00"},
		{"10.49", HalfUp, "10.00"}, {"10.01", Up, "11.00"}, {"10.99", Down, "10.00"}, {"-10.01", Floor, "-11.00"}, {"10", HalfUp, "10.00"},
	} {
		got, err := m(c.in, JPY).Round(c.mode)
		if err != nil || got.Amount.String() != c.want || got.Valid() != nil {
			t.Errorf("JPY %s %v = %v,%v; want %s", c.in, c.mode, got, err, c.want)
		}
	}
	if got, err := m("10.57", USD).Round(HalfUp); err != nil || got.Amount.String() != "10.57" {
		t.Error(got, err)
	}
	if _, err := m("1", USD).Round(0); !errors.Is(err, ErrInvalidRounding) {
		t.Error(err)
	}
	if _, err := m("1", JPY).Round(0); !errors.Is(err, ErrInvalidRounding) {
		t.Error(err)
	}
	if _, err := (Money{Currency: "ZZZ"}).Round(HalfUp); !errors.Is(err, ErrUnknownCurrency) {
		t.Error(err)
	}
}

func TestMoneyJSON(t *testing.T) {
	m, _ := New(mustA(t, "1234.5"), USD)
	b, err := json.Marshal(m)
	if err != nil || string(b) != `{"amount":"1234.50","currency":"USD"}` {
		t.Fatalf("%s %v", b, err)
	}
	var out Money
	if err := json.Unmarshal(b, &out); err != nil || out != m {
		t.Error(out, err)
	}
	// currency байхгүй → MNT; amount тоо
	if err := json.Unmarshal([]byte(`{"amount":12.5}`), &out); err != nil || out.Currency != MNT || out.Amount.String() != "12.50" {
		t.Error(out, err)
	}
	// zero Money
	b, err = json.Marshal(Money{})
	if err != nil || string(b) != `{"amount":"0.00","currency":"MNT"}` {
		t.Errorf("%s %v", b, err)
	}
	for in, want := range map[string]error{
		`{}`: ErrInvalid, `{"amount":null}`: ErrInvalid, `{"amount":"1","currency":"ZZZ"}`: ErrUnknownCurrency,
		`{"amount":"x"}`: ErrInvalid, `"1"`: ErrInvalid, `{"amount":1,"currency":5}`: ErrInvalid,
	} {
		var x Money
		if err := json.Unmarshal([]byte(in), &x); !errors.Is(err, want) {
			t.Errorf("Unmarshal(%s) err = %v, want %v", in, err, want)
		}
	}
	out = m
	if err := json.Unmarshal([]byte(`null`), &out); err != nil || out != m {
		t.Error(out, err)
	}
	if _, err := json.Marshal(Money{Currency: "ZZZ"}); err == nil {
		t.Error("үл мэдэгдэх валют marshal хийгдсэн")
	}
}

func TestRounding(t *testing.T) {
	for _, m := range allModes {
		p, err := ParseRounding(m.String())
		if err != nil || p != m || !m.Valid() {
			t.Error(m, p, err)
		}
	}
	if p, err := ParseRounding("HALF_EVEN"); err != nil || p != HalfEven {
		t.Error(p, err)
	}
	if _, err := ParseRounding("x"); !errors.Is(err, ErrInvalidRounding) {
		t.Error(err)
	}
	if Rounding(0).Valid() || Rounding(8).Valid() || Rounding(0).String() == "" {
		t.Error("invalid")
	}
}
