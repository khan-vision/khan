package validate

import (
	"fmt"
	"math"
	"regexp"
)

// Дэмжигдсэн хязгаарууд.
const (
	DefaultStringMaxLen = 255
	DefaultTextMaxLen   = 10000
	MaxJSONBytes        = 256 << 10 // JSON төрлийн дээд хэмжээ
	MinPasswordLen      = 8
	MaxPasswordBytes    = 72 // bcrypt хязгаар
	DefaultNumberScale  = 6
	MaxNumberScale      = 18
	moneyScale          = 2
	moneyIntDigits      = 16 // numeric(18,2)
	numberIntDigits     = 18
)

// Rules — нэг талбарын шалгалтын дүрэм.
type Rules struct {
	Required bool  // create үед заавал (update үед ирсэн бол хоосон байж болохгүй)
	Nullable bool  // null/"" → NULL зөвшөөрөх
	Trim     *bool // String/Text: nil=true

	MinLen, MaxLen int // rune-ээр; String анхдагч MaxLen 255, Text 10000

	Min, Max *float64 // Int/Number/Money-ийн хязгаар (хоёр талдаа орно)

	Scale int // Number: бутархайн дээд орон (0 → 6, дээд 18); Money үргэлж 2

	Pattern    *regexp.Regexp
	PatternMsg string
	Enum       []string
}

// TrimOn — Trim идэвхтэй эсэх (nil → true).
func (r Rules) TrimOn() bool { return r.Trim == nil || *r.Trim }

// EffectiveMaxLen — kind-д хамаарсан бодит дээд урт (0 = хязгааргүй/хамааралгүй).
func (r Rules) EffectiveMaxLen(k Kind) int {
	if r.MaxLen > 0 {
		return r.MaxLen
	}
	switch k {
	case String:
		return DefaultStringMaxLen
	case Text:
		return DefaultTextMaxLen
	}
	return 0
}

// EffectiveScale — Number/Money-ийн бодит бутархайн орон.
func (r Rules) EffectiveScale(k Kind) int {
	switch k {
	case Money:
		return moneyScale
	case Number:
		if r.Scale > 0 {
			return r.Scale
		}
		return DefaultNumberScale
	}
	return 0
}

// Check — дүрмийн програмчлалын алдааг (зөрчилтэй хязгаар, хоосон Enum) илрүүлнэ.
// crud.Mount үед дуудагдана.
func (r Rules) Check(k Kind) error {
	if !k.Valid() {
		return fmt.Errorf("үл мэдэгдэх төрөл %q", k)
	}
	if r.MinLen < 0 || r.MaxLen < 0 {
		return fmt.Errorf("MinLen/MaxLen сөрөг байж болохгүй")
	}
	if r.MaxLen > 0 && r.MinLen > r.MaxLen {
		return fmt.Errorf("MinLen (%d) > MaxLen (%d)", r.MinLen, r.MaxLen)
	}
	for _, f := range []*float64{r.Min, r.Max} {
		if f != nil && (math.IsNaN(*f) || math.IsInf(*f, 0)) {
			return fmt.Errorf("Min/Max төгсгөлөг тоо байх ёстой")
		}
	}
	if r.Min != nil && r.Max != nil && *r.Min > *r.Max {
		return fmt.Errorf("Min (%v) > Max (%v)", *r.Min, *r.Max)
	}
	if (r.Min != nil || r.Max != nil) && k != Int && k != Number && k != Money {
		return fmt.Errorf("Min/Max зөвхөн Int/Number/Money-д")
	}
	if r.Scale < 0 || r.Scale > MaxNumberScale {
		return fmt.Errorf("Scale 0..%d байх ёстой", MaxNumberScale)
	}
	if k == Enum && len(r.Enum) == 0 {
		return fmt.Errorf("Enum төрөлд Enum жагсаалт заавал")
	}
	if len(r.Enum) > 0 && k != Enum && k != String {
		return fmt.Errorf("Enum жагсаалт зөвхөн Enum/String-д")
	}
	seen := map[string]bool{}
	for _, e := range r.Enum {
		if seen[e] {
			return fmt.Errorf("Enum утга давхардсан: %q", e)
		}
		seen[e] = true
	}
	return nil
}

// Bound — Rules.Min/Max-д зориулсан туслах (`Min: validate.Bound(0)`).
func Bound(f float64) *float64 { return &f }
