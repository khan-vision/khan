package money

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestJSON(t *testing.T) {
	type doc struct {
		A Amount         `json:"a"`
		P *Amount        `json:"p"`
		N NullAmount     `json:"n"`
		R Rate           `json:"r"`
		M map[Amount]int `json:"m,omitempty"`
	}
	in := doc{A: mustA(t, "1234.5"), N: NullAmountOf(mustA(t, "-0.05")), R: mustR(t, "0.105")}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"a":"1234.50","p":null,"n":"-0.05","r":"0.105"}`; string(b) != want {
		t.Errorf("marshal = %s, want %s", b, want)
	}
	var out doc
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.A != in.A || out.P != nil || out.N != in.N || out.R != in.R {
		t.Errorf("roundtrip: %+v", out)
	}
	// тоо болон string хоёуланг нь хүлээнэ
	for in, want := range map[string]string{
		`12.5`: "12.50", `"12.5"`: "12.50", `12`: "12.00", `-0.01`: "-0.01", `1e2`: "100.00", `1E+2`: "100.00",
		`1.50`: "1.50", `"1e2"`: "100.00", ` 7 `: "7.00", `9999999999999999.99`: "9999999999999999.99",
	} {
		var a Amount
		if err := json.Unmarshal([]byte(in), &a); err != nil || a.String() != want {
			t.Errorf("Unmarshal(%s) = %v,%v; want %s", in, a, err, want)
		}
	}
	for _, bad := range []string{`true`, `[]`, `{}`, `"abc"`, `"1,000"`, `" 1"`, `0.005`, `"0.005"`, `1e20`, `""`, `"+1"`, `"١"`} {
		var a Amount
		err := json.Unmarshal([]byte(bad), &a)
		if err == nil {
			t.Errorf("Unmarshal(%s) алдаагүй: %v", bad, a)
		}
	}
	// 0.005 → ErrPrecision, wrap-тай
	var a Amount
	if err := json.Unmarshal([]byte(`0.005`), &a); !errors.Is(err, ErrPrecision) {
		t.Errorf("err = %v", err)
	}
	// null no-op
	a = mustA(t, "5")
	if err := json.Unmarshal([]byte(`null`), &a); err != nil || a != mustA(t, "5") {
		t.Error(a, err)
	}
	var n NullAmount
	if err := json.Unmarshal([]byte(`null`), &n); err != nil || n.Valid {
		t.Error(n, err)
	}
	if err := json.Unmarshal([]byte(`"1.5"`), &n); err != nil || !n.Valid || n.Amount.String() != "1.50" {
		t.Error(n, err)
	}
	n = NullAmount{}
	if err := json.Unmarshal([]byte(`"x"`), &n); err == nil || n.Valid {
		t.Error("NullAmount буруу", n)
	}
	// text (map түлхүүр)
	m := map[Amount]int{mustA(t, "1.5"): 1}
	b, err = json.Marshal(m)
	if err != nil || string(b) != `{"1.50":1}` {
		t.Errorf("%s %v", b, err)
	}
	var m2 map[Amount]int
	if err := json.Unmarshal(b, &m2); err != nil || m2[mustA(t, "1.5")] != 1 {
		t.Error(m2, err)
	}
	if mustA(t, "3").JSONNumber() != json.Number("3.00") {
		t.Error("JSONNumber")
	}
}

func TestScanValue(t *testing.T) {
	for _, c := range []struct {
		src  any
		want string
	}{
		{"1234.50", "1234.50"}, {[]byte("-0.05"), "-0.05"}, {"12", "12.00"}, {"1.5", "1.50"}, {"0", "0.00"},
		{int64(7), "7.00"}, {int64(-7), "-7.00"}, {12.5, "12.50"}, {0.1, "0.10"}, {-19.99, "-19.99"},
		{"9999999999999999.99", "9999999999999999.99"}, {"1.500", "1.50"}, {float64(100), "100.00"},
	} {
		var a Amount
		if err := a.Scan(c.src); err != nil || a.String() != c.want {
			t.Errorf("Scan(%#v) = %v,%v; want %s", c.src, a, err, c.want)
		}
	}
	x, y := 0.1, 0.2
	for _, c := range []struct {
		src  any
		want error
	}{
		{nil, ErrNull}, {"1.234", ErrPrecision}, {[]byte("0.005"), ErrPrecision}, {x + y, ErrPrecision}, {1.005, ErrPrecision},
		{1e15, ErrPrecision}, {x - x + 1/(x-x), ErrPrecision}, {"abc", ErrInvalid}, {"", ErrInvalid}, {true, ErrInvalid},
		{int(5), ErrInvalid}, {int64(1e17), ErrOverflow}, {"1e30", ErrOverflow},
	} {
		var a Amount
		if err := a.Scan(c.src); !errors.Is(err, c.want) {
			t.Errorf("Scan(%#v) err = %v, want %v", c.src, err, c.want)
		}
	}
	// Value нь яг текст
	v, err := mustA(t, "1234.5").Value()
	if err != nil || v != "1234.50" {
		t.Error(v, err)
	}
	// NullAmount
	var n NullAmount
	if err := n.Scan(nil); err != nil || n.Valid {
		t.Error(n, err)
	}
	if err := n.Scan("2.5"); err != nil || !n.Valid || n.Amount.String() != "2.50" {
		t.Error(n, err)
	}
	if err := n.Scan("x"); err == nil || n.Valid {
		t.Error(n, err)
	}
	if v, _ := (NullAmount{}).Value(); v != nil {
		t.Error(v)
	}
	if v, _ := NullAmountOf(mustA(t, "3")).Value(); v != "3.00" {
		t.Error(v)
	}
	if (Amount{}).GormDataType() != "numeric(18,2)" || (NullAmount{}).GormDataType() != "numeric(18,2)" {
		t.Error("gorm type")
	}
}

func TestRate(t *testing.T) {
	for in, want := range map[string]string{
		"0.105": "0.105", "1": "1", "0": "0", "0.10": "0.1", "-0.05": "-0.05", "0.00000001": "0.00000001",
		"12.5": "12.5", "1e-2": "0.01", "9999999999.99999999": "9999999999.99999999",
	} {
		r, err := ParseRate(in)
		if err != nil || r.String() != want {
			t.Errorf("ParseRate(%q) = %v,%v; want %s", in, r, err, want)
		}
	}
	for in, want := range map[string]error{
		"0.000000001": ErrPrecision, "": ErrInvalid, ".1": ErrInvalid, "1,5": ErrInvalid, "1e30": ErrOverflow, "1000000000000": ErrOverflow,
	} {
		if _, err := ParseRate(in); !errors.Is(err, want) {
			t.Errorf("ParseRate(%q) err = %v, want %v", in, err, want)
		}
	}
	p, err := ParsePercent("10.5")
	if err != nil || p != mustR(t, "0.105") || p.Percent() != "10.5" {
		t.Error(p, err)
	}
	if p, _ := ParsePercent("10"); p.String() != "0.1" || p.Percent() != "10" || p.Raw() != 10_000_000 {
		t.Error(p)
	}
	if _, err := ParsePercent("0.0000001"); !errors.Is(err, ErrPrecision) {
		t.Error(err)
	}
	if p, err := ParsePercent("0.000001"); err != nil || p.String() != "0.00000001" {
		t.Error(p, err)
	}
	r, _ := RateFromRaw(1_000_000)
	if r.Percent() != "1" || r.Neg().Sign() != -1 || r.Cmp(r.Neg()) != 1 || r.IsZero() || !(Rate{}).IsZero() {
		t.Error("rate misc")
	}
	if _, err := RateFromRaw(maxRaw + 1); !errors.Is(err, ErrOverflow) {
		t.Error(err)
	}
	b, _ := json.Marshal(r)
	var r2 Rate
	if string(b) != `"0.01"` || json.Unmarshal([]byte(`0.01`), &r2) != nil || r2 != r {
		t.Error(string(b), r2)
	}
	if json.Unmarshal([]byte(`"abc"`), &r2) == nil || json.Unmarshal([]byte(`null`), &r2) != nil || r2 != r {
		t.Error("rate json errors", r2)
	}
	var r3 Rate
	if err := r3.Scan("0.10500000"); err != nil || r3.String() != "0.105" {
		t.Error(r3, err)
	}
	if err := r3.Scan(nil); !errors.Is(err, ErrNull) {
		t.Error(err)
	}
	if v, _ := r3.Value(); v != "0.105" || r3.GormDataType() != "numeric(18,8)" {
		t.Error(v)
	}
	var r4 Rate
	if err := r4.UnmarshalText([]byte("0.5")); err != nil || r4.String() != "0.5" {
		t.Error(r4, err)
	}
}

func TestTextAndMisc(t *testing.T) {
	var a Amount
	if err := a.UnmarshalText([]byte("12.3")); err != nil || a.String() != "12.30" {
		t.Error(a, err)
	}
	if err := a.UnmarshalText([]byte(" 1")); err == nil {
		t.Error("зайтай текст зөвшөөрөгдсөн")
	}
	if b, _ := a.MarshalText(); string(b) != "12.30" {
		t.Error(string(b))
	}
	if b, _ := mustR(t, "0.1").MarshalText(); string(b) != "0.1" {
		t.Error(string(b))
	}
	if _, err := Sum(Amount{MaxMinor}, Amount{MaxMinor}); err == nil {
		t.Error("Sum overflow")
	}
}
