package audit

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCanonDecimal(t *testing.T) {
	cases := map[string]string{
		"12.50": "12.5", "12.5": "12.5", "012.500": "12.5", "0": "0", "-0": "0", "0.00": "0", "1e3": "1000",
		"1E+3": "1000", "1.5e-3": "0.0015", "-0.10": "-0.1", ".5": "0.5", "5.": "5", "100": "100",
		"+7": "7", "9999999999999999.99": "9999999999999999.99", "1e100": "1e100", "0.0000001": "0.0000001",
		"1e-100": "1e-100",
	}
	for in, want := range cases {
		got, ok := canonDecimal(in)
		if !ok || got != want {
			t.Errorf("canonDecimal(%q)=%q,%v want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"", ".", "-", "abc", "1/2", "0x10", "1e", "1e999999", "NaN", "1 2", "--1"} {
		if got, ok := canonDecimal(in); ok {
			t.Errorf("canonDecimal(%q) = %q, тоо биш байх ёстой", in, got)
		}
	}
}

func TestDiffUnchangedAcrossTypes(t *testing.T) {
	utc := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	ub := time.FixedZone("UB", 8*3600)
	cases := []struct {
		name string
		a, b any
	}{
		{"float vs money string", 12.5, "12.50"},
		{"json.Number scale", json.Number("12.500"), 12.5},
		{"int vs float", 12, 12.0},
		{"int64 vs string", int64(7), "7"},
		{"uint vs int", uint8(3), 3},
		{"float32", float32(0.1), 0.1}, // 0.1 → "0.1" хоёулаа
		{"bytes vs string", []byte("abc"), "abc"},
		{"time zones", utc, utc.In(ub)},
		{"time vs rfc3339 string", utc, "2026-11-06T20:00:00+08:00"},
		{"date string vs midnight", time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), "2026-01-02"},
		{"nil vs nil", nil, nil},
		{"raw json key order", json.RawMessage(`{"a":1,"b":[1,2]}`), map[string]any{"b": []any{1, 2}, "a": 1.0}},
		{"nested decimals", map[string]any{"x": map[string]any{"p": "1.50"}}, map[string]any{"x": map[string]any{"p": "1.50"}}},
		{"nested num vs str", map[string]any{"p": 1.5}, map[string]any{"p": "1.50"}},
		{"slice of maps", []map[string]any{{"q": 1}}, []any{map[string]any{"q": 1.0}}},
		{"time ptr", &utc, utc},
		{"bool", true, true},
	}
	for _, tc := range cases {
		ch := Diff(map[string]any{"f": tc.a}, map[string]any{"f": tc.b}, DiffOptions{})
		if len(ch) != 0 {
			t.Errorf("%s: өөрчлөлт гарсан: %v", tc.name, ch)
		}
		ch = Diff(map[string]any{"f": tc.b}, map[string]any{"f": tc.a}, DiffOptions{})
		if len(ch) != 0 {
			t.Errorf("%s (урвуу): өөрчлөлт гарсан: %v", tc.name, ch)
		}
	}
}

func TestDiffDetectsChanges(t *testing.T) {
	cases := []struct {
		name string
		a, b any
	}{
		{"decimal", "12.50", 12.51},
		{"text 007 vs 7 are different strings", "007", "7"},
		{"nil vs empty string", nil, ""},
		{"nil vs zero", nil, 0},
		{"nil vs false", nil, false},
		{"false vs true", false, true},
		{"empty vs zero", "", 0},
		{"string vs number text", "abc", 1},
		{"time differs", time.Unix(0, 0), time.Unix(1, 0)},
		{"json value", json.RawMessage(`{"a":1}`), json.RawMessage(`{"a":2}`)},
		{"json extra key", map[string]any{"a": 1}, map[string]any{"a": 1, "b": 2}},
		{"slice order", []any{1, 2}, []any{2, 1}},
		{"slice len", []any{1}, []any{1, 2}},
		{"nan", "NaN", 1},
		{"time vs number", time.Unix(0, 0), 0},
		{"big decimal", "9999999999999999.99", "9999999999999999.98"},
	}
	for _, tc := range cases {
		ch := Diff(map[string]any{"f": tc.a}, map[string]any{"f": tc.b}, DiffOptions{})
		if len(ch) != 1 {
			t.Errorf("%s: өөрчлөлт илрээгүй (%v → %v): %v", tc.name, tc.a, tc.b, ch)
		}
	}
}

func TestDiffNilVsMissing(t *testing.T) {
	// Бүтэн харьцуулалт: байхгүй = nil.
	if ch := Diff(map[string]any{"a": nil}, map[string]any{}, DiffOptions{}); len(ch) != 0 {
		t.Errorf("nil vs missing өөрчлөлт биш: %v", ch)
	}
	if ch := Diff(map[string]any{}, map[string]any{"a": nil}, DiffOptions{}); len(ch) != 0 {
		t.Errorf("missing vs nil өөрчлөлт биш: %v", ch)
	}
	ch := Diff(map[string]any{"a": 1}, map[string]any{}, DiffOptions{})
	if c, ok := ch["a"]; !ok || c.New != nil || c.Old != json.Number("1") {
		t.Errorf("устгагдсан түлхүүр [1,nil] байх ёстой: %v", ch)
	}
	// Partial: new-д байхгүй түлхүүр өөрчлөгдөөгүй; харин new-д nil гэж ИРСЭН бол өөрчлөлт.
	if ch := Diff(map[string]any{"a": 1, "b": 2}, map[string]any{"b": 2}, DiffOptions{Partial: true}); len(ch) != 0 {
		t.Errorf("Partial: %v", ch)
	}
	ch = Diff(map[string]any{"a": 1}, map[string]any{"a": nil}, DiffOptions{Partial: true})
	if len(ch) != 1 || ch["a"].New != nil {
		t.Errorf("Partial nil тавилт: %v", ch)
	}
	// create / delete
	ch = Diff(nil, map[string]any{"a": "x", "b": nil}, DiffOptions{})
	if len(ch) != 1 || ch["a"].Old != nil || ch["a"].New != "x" {
		t.Errorf("create: %v", ch)
	}
	ch = Diff(map[string]any{"a": "x"}, nil, DiffOptions{})
	if len(ch) != 1 || ch["a"].Old != "x" || ch["a"].New != nil {
		t.Errorf("delete: %v", ch)
	}
	if ch := Diff(nil, nil, DiffOptions{}); ch == nil || len(ch) != 0 {
		t.Error("хоосон Diff nil биш хоосон map байх ёстой")
	}
}

func TestDiffRedactExcludeReveal(t *testing.T) {
	old := map[string]any{"password": "h1", "api_token": "t1", "pin": "1234", "skip": 1, "note": "a", "meta": map[string]any{"secret_key": "s", "k": 1}}
	nw := map[string]any{"password": "h2", "api_token": "t2", "pin": "9999", "skip": 2, "note": "b", "meta": map[string]any{"secret_key": "s2", "k": 2}}
	ch := Diff(old, nw, DiffOptions{Redact: []string{"pin"}, Exclude: []string{"skip"}, Reveal: []string{"api_token"}})
	if _, ok := ch["skip"]; ok {
		t.Error("Exclude ажиллаагүй")
	}
	for _, k := range []string{"password", "pin"} {
		if ch[k].Old != Redacted || ch[k].New != Redacted {
			t.Errorf("%s нууцлагдаагүй: %v", k, ch[k])
		}
	}
	if ch["api_token"].Old != "t1" || ch["api_token"].New != "t2" {
		t.Errorf("Reveal ажиллаагүй: %v", ch["api_token"])
	}
	m := ch["meta"].New.(map[string]any)
	if m["secret_key"] != Redacted || m["k"] != json.Number("2") {
		t.Errorf("nested нууц: %v", m)
	}
	raw, _ := json.Marshal(ch)
	for _, leak := range []string{"h1", "h2", "1234", "9999", `"s"`, `"s2"`} {
		if strings.Contains(string(raw), leak) {
			t.Errorf("JSON-д %s алдагдсан: %s", leak, raw)
		}
	}
	// nil → утга: «тавигдсан» нь харагдана, утга нууц.
	ch = Diff(map[string]any{"password": nil}, map[string]any{"password": "x"}, DiffOptions{})
	if ch["password"].Old != nil || ch["password"].New != Redacted {
		t.Errorf("nil → нууц: %v", ch["password"])
	}
}

func TestDiffRedactsTokensInStrings(t *testing.T) {
	ch := Diff(map[string]any{"note": "a"}, map[string]any{"note": "Authorization: Bearer abcdefghijklmnopqrstuvwxyz0123456789"}, DiffOptions{})
	if s := ch["note"].New.(string); strings.Contains(s, "abcdefghijkl") {
		t.Errorf("Bearer token ил: %s", s)
	}
}

func TestDiffStableOrderAndJSON(t *testing.T) {
	old := map[string]any{"z": 1, "a": 1, "m": 1}
	nw := map[string]any{"z": 2, "a": 2, "m": 2}
	first, _ := json.Marshal(Diff(old, nw, DiffOptions{}))
	for i := 0; i < 20; i++ {
		got, _ := json.Marshal(Diff(old, nw, DiffOptions{}))
		if string(got) != string(first) {
			t.Fatalf("тогтворгүй: %s vs %s", got, first)
		}
	}
	if string(first) != `{"a":[1,2],"m":[1,2],"z":[1,2]}` {
		t.Errorf("JSON: %s", first)
	}
	cs := Diff(old, nw, DiffOptions{})
	if !reflect.DeepEqual(cs.Fields(), []string{"a", "m", "z"}) {
		t.Errorf("Fields: %v", cs.Fields())
	}
}

func TestChangeJSONRoundTrip(t *testing.T) {
	cs := Changes{"price": {Old: json.Number("12.50"), New: json.Number("13")}, "n": {Old: nil, New: "x"}, "p": {Old: Redacted, New: Redacted}}
	b, err := json.Marshal(cs)
	if err != nil {
		t.Fatal(err)
	}
	var back Changes
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back["price"].Old != json.Number("12.50") || back["n"].Old != nil {
		t.Errorf("round trip: %v", back)
	}
	if o := back.Old(); o["price"] != json.Number("12.50") || len(o) != 2 { // p нууц → орохгүй
		t.Errorf("Old(): %v", o)
	}
	if n := back.New(); n["n"] != "x" || len(n) != 2 {
		t.Errorf("New(): %v", n)
	}
	for _, bad := range []string{`[1]`, `[1,2,3]`, `{}`, `"x"`} {
		var c Change
		if err := json.Unmarshal([]byte(bad), &c); err == nil {
			t.Errorf("%s: алдаа хүлээсэн", bad)
		}
	}
}

func TestDecimalRenderKeepsScale(t *testing.T) {
	ch := Diff(map[string]any{"p": json.Number("12.50")}, map[string]any{"p": json.Number("13.00")}, DiffOptions{})
	b, _ := json.Marshal(ch)
	if string(b) != `{"p":[12.50,13.00]}` {
		t.Errorf("scale алдагдсан: %s", b)
	}
	// JSON тоо биш бичлэг (007 мөр, "1." г.м.) каноник болно.
	ch = Diff(map[string]any{"p": 1}, map[string]any{"p": json.Number("1.")}, DiffOptions{})
	_ = ch
	ch = Diff(map[string]any{"p": 0}, map[string]any{"p": json.Number(".5")}, DiffOptions{})
	if b, _ := json.Marshal(ch); string(b) != `{"p":[0,0.5]}` {
		t.Errorf("каноник: %s", b)
	}
}

func TestNormalizeMisc(t *testing.T) {
	type pt struct{ X int }
	if got := normalize(pt{3}); !reflect.DeepEqual(got, map[string]any{"X": fromInt(3)}) {
		t.Errorf("struct: %#v", got)
	}
	if got := normalize(make(chan int)); got == nil {
		t.Error("marshal хийж болохгүй төрөл мөр болох ёстой")
	}
	if got := normalize(json.RawMessage(`{bad`)); got != "{bad" {
		t.Errorf("буруу JSON: %v", got)
	}
	if got := normalize(json.RawMessage(``)); got != nil {
		t.Errorf("хоосон raw: %v", got)
	}
	if normalize((*time.Time)(nil)) != nil || normalize(map[string]any(nil)) != nil {
		t.Error("typed nil")
	}
	if got := normalize(json.Number("abc")); got != "abc" {
		t.Errorf("json.Number буруу: %v", got)
	}
	nan := normalize(nanFloat())
	if _, ok := nan.(string); !ok {
		t.Errorf("NaN мөр байх ёстой: %#v", nan)
	}
	var np *int
	if normalize(np) != nil {
		t.Error("nil pointer → nil")
	}
	if equal(1, 2) { // normalize хийгдээгүй төрөл
		t.Error("equal нь үл мэдэгдэх төрөлд false")
	}
	if equal("x", 1) || equal(true, "x") || equal(map[string]any{}, []any{}) || equal(time.Now(), true) {
		t.Error("төрөл зөрөх үед false")
	}
	if equal(dec{canon: "1"}, true) {
		t.Error("dec vs bool")
	}
	if stringIsTime("garbage", time.Now()) {
		t.Error("stringIsTime")
	}
	if equal(map[string]any{"a": 1}, map[string]any{"b": 1}) || equal(map[string]any{"a": dec{canon: "1"}}, map[string]any{"a": dec{canon: "2"}}) {
		t.Error("map equal")
	}
}

func nanFloat() float64 { var z float64; return z / z }

func TestSnapshot(t *testing.T) {
	if Snapshot(nil, DiffOptions{}) != nil {
		t.Error("nil → nil")
	}
	s := Snapshot(map[string]any{"password": "h", "name": "n", "x": 12.5, "drop": 1, "when": time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)},
		DiffOptions{Exclude: []string{"drop"}})
	if _, ok := s["drop"]; ok || s["password"] != Redacted || s["name"] != "n" || s["when"] != "2026-01-02T03:04:05Z" {
		t.Errorf("snapshot: %v", s)
	}
}
