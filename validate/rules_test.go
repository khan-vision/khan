package validate

import (
	"math"
	"strings"
	"testing"
)

func TestKinds(t *testing.T) {
	if len(Kinds()) != 15 {
		t.Fatalf("15 төрөл хүлээсэн: %d", len(Kinds()))
	}
	for _, k := range Kinds() {
		if !k.Valid() {
			t.Errorf("%s valid байх ёстой", k)
		}
	}
	if Kind("x").Valid() || Kind("").Valid() {
		t.Fatal("үл мэдэгдэх төрөл valid биш")
	}
	ks := Kinds()
	ks[0] = "mutated"
	if Kinds()[0] == "mutated" {
		t.Fatal("Kinds() хуулбар буцаах ёстой")
	}
}

func TestRulesDefaults(t *testing.T) {
	var r Rules
	if !r.TrimOn() {
		t.Fatal("Trim анхдагч true")
	}
	if r.EffectiveMaxLen(String) != 255 || r.EffectiveMaxLen(Text) != 10000 || r.EffectiveMaxLen(Int) != 0 {
		t.Fatal("MaxLen анхдагч")
	}
	if (Rules{MaxLen: 5}).EffectiveMaxLen(String) != 5 {
		t.Fatal("MaxLen тодорхой")
	}
	if r.EffectiveScale(Money) != 2 || r.EffectiveScale(Number) != 6 || (Rules{Scale: 3}).EffectiveScale(Number) != 3 || r.EffectiveScale(Int) != 0 {
		t.Fatal("Scale анхдагч")
	}
}

func TestRulesCheck(t *testing.T) {
	nan := math.NaN()
	inf := math.Inf(1)
	for name, tc := range map[string]struct {
		k  Kind
		r  Rules
		ok bool
	}{
		"ok":                {String, Rules{MinLen: 1, MaxLen: 5}, true},
		"bad kind":          {Kind("zz"), Rules{}, false},
		"neg len":           {String, Rules{MinLen: -1}, false},
		"min>max len":       {String, Rules{MinLen: 5, MaxLen: 3}, false},
		"min>max":           {Int, Rules{Min: Bound(5), Max: Bound(1)}, false},
		"min==max":          {Int, Rules{Min: Bound(5), Max: Bound(5)}, true},
		"nan":               {Int, Rules{Min: &nan}, false},
		"inf":               {Money, Rules{Max: &inf}, false},
		"min on string":     {String, Rules{Min: Bound(1)}, false},
		"scale too big":     {Number, Rules{Scale: 19}, false},
		"scale neg":         {Number, Rules{Scale: -1}, false},
		"enum kind no list": {Enum, Rules{}, false},
		"enum list":         {Enum, Rules{Enum: []string{"a"}}, true},
		"enum on int":       {Int, Rules{Enum: []string{"a"}}, false},
		"enum on string":    {String, Rules{Enum: []string{"a"}}, true},
		"enum dup":          {Enum, Rules{Enum: []string{"a", "a"}}, false},
	} {
		err := tc.r.Check(tc.k)
		if (err == nil) != tc.ok {
			t.Errorf("%s: err=%v, ok=%v", name, err, tc.ok)
		}
	}
}

func TestErrors(t *testing.T) {
	e := NewErrors()
	if e.Err() != nil || e.Len() != 0 {
		t.Fatal("хоосон Errors → nil")
	}
	e.Add("b", "x")
	e.Add("a", "y")
	e.Add("a", "y") // давхардахгүй
	e.Add("a", "z")
	if len(e["a"]) != 2 || e.Len() != 2 {
		t.Fatalf("%v", e)
	}
	if e.Err() == nil {
		t.Fatal("Err()")
	}
	if got := e.Error(); got != "Мэдээлэл буруу: a: y; z | b: x" {
		t.Fatalf("Error() тогтвортой байх ёстой: %q", got)
	}
	o := NewErrors()
	o.Add("a", "y")
	o.Add("c", "w")
	e.Merge(o)
	if len(e["a"]) != 2 || len(e["c"]) != 1 {
		t.Fatalf("Merge: %v", e)
	}
	if !strings.Contains(e.Error(), "c: w") {
		t.Fatal("Merge Error()")
	}
}
