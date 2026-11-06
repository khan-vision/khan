package validate

import (
	"errors"
	"strconv"
	"strings"
)

// dec — float-гүй decimal. digits нь тэргүүн тэггүй (тэг бол ""), scale нь
// бутархайн орон (хойд тэгийг хассан, >= 0).
type dec struct {
	neg    bool
	digits string
	scale  int
}

var errDec = errors.New("decimal буруу")

const maxDecLen = 400

// parseDec — `-?\d+(\.\d+)?([eE][+-]?\d+)?`. "+" тэмдэг, зай, `.5`, `1.` зөвшөөрөхгүй.
func parseDec(s string) (dec, error) {
	if s == "" || len(s) > maxDecLen {
		return dec{}, errDec
	}
	i := 0
	neg := false
	if s[0] == '-' {
		neg = true
		i++
	}
	start := i
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == start {
		return dec{}, errDec
	}
	intPart := s[start:i]
	frac := ""
	if i < len(s) && s[i] == '.' {
		i++
		fs := i
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
		if i == fs {
			return dec{}, errDec
		}
		frac = s[fs:i]
	}
	exp := 0
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		i++
		es := i
		if i < len(s) && (s[i] == '+' || s[i] == '-') {
			i++
		}
		ds := i
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
		if i == ds || i-ds > 4 {
			return dec{}, errDec
		}
		n, err := strconv.Atoi(s[es:i])
		if err != nil {
			return dec{}, errDec
		}
		exp = n
	}
	if i != len(s) {
		return dec{}, errDec
	}
	if exp > maxDecLen || exp < -maxDecLen {
		return dec{}, errDec
	}
	digits := strings.TrimLeft(intPart+frac, "0")
	scale := len(frac) - exp
	if digits == "" {
		return dec{}, nil // тэг (−0 → 0)
	}
	if scale < 0 {
		if len(digits)-scale > maxDecLen {
			return dec{}, errDec
		}
		digits += strings.Repeat("0", -scale)
		scale = 0
	}
	for scale > 0 && digits[len(digits)-1] == '0' {
		digits = digits[:len(digits)-1]
		scale--
	}
	return dec{neg: neg, digits: digits, scale: scale}, nil
}

func (d dec) isZero() bool { return d.digits == "" }

// intDigits — бүхэл хэсгийн орны тоо (0.5 → 0).
func (d dec) intDigits() int {
	if n := len(d.digits) - d.scale; n > 0 {
		return n
	}
	return 0
}

// fixed — яг n оронтой бутархайтай plain мөр ("12.50"); n >= d.scale гэж үзнэ.
func (d dec) fixed(n int) string {
	var intPart, frac string
	if len(d.digits) > d.scale {
		intPart = d.digits[:len(d.digits)-d.scale]
		frac = d.digits[len(d.digits)-d.scale:]
	} else {
		intPart = "0"
		frac = strings.Repeat("0", d.scale-len(d.digits)) + d.digits
	}
	if len(frac) < n {
		frac += strings.Repeat("0", n-len(frac))
	}
	out := intPart
	if n > 0 {
		out += "." + frac
	}
	if d.neg && !d.isZero() {
		out = "-" + out
	}
	return out
}

// String — хамгийн богино plain хэлбэр ("12.3", "5", "-0.005").
func (d dec) String() string { return d.fixed(d.scale) }

// int64 — scale == 0 бөгөөд int64-д багтах үед.
func (d dec) int64() (int64, bool) {
	if d.scale != 0 {
		return 0, false
	}
	if d.isZero() {
		return 0, true
	}
	s := d.digits
	if d.neg {
		s = "-" + s
	}
	n, err := strconv.ParseInt(s, 10, 64)
	return n, err == nil
}

// cmp — -1, 0, 1.
func (d dec) cmp(o dec) int {
	switch {
	case d.isZero() && o.isZero():
		return 0
	case d.isZero():
		if o.neg {
			return 1
		}
		return -1
	case o.isZero():
		if d.neg {
			return -1
		}
		return 1
	case d.neg != o.neg:
		if d.neg {
			return -1
		}
		return 1
	}
	m := cmpMag(d, o)
	if d.neg {
		return -m
	}
	return m
}

// cmpMag — тэг биш хоёр тооны модулийн харьцуулалт.
func cmpMag(a, b dec) int {
	s := a.scale
	if b.scale > s {
		s = b.scale
	}
	ad := a.digits + strings.Repeat("0", s-a.scale)
	bd := b.digits + strings.Repeat("0", s-b.scale)
	if len(ad) != len(bd) {
		if len(ad) < len(bd) {
			return -1
		}
		return 1
	}
	return strings.Compare(ad, bd)
}

// decFromFloat — Rules.Min/Max-ийн хамгийн богино decimal дүрслэлээр (0.1 → "0.1").
func decFromFloat(f float64) (dec, bool) {
	d, err := parseDec(strconv.FormatFloat(f, 'f', -1, 64))
	return d, err == nil
}
