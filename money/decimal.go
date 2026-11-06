package money

import (
	"fmt"
	"strconv"
	"strings"
)

// maxRaw — Amount/Rate-ийн дотоод бүхэл тооны абсолют дээд хязгаар: 18 цифр
// (numeric(18,2)-ийн 9999999999999999.99). 2*maxRaw < MaxInt64 тул нэмэх/хасах wrap хийхгүй.
const (
	maxRaw    int64 = 999_999_999_999_999_999
	maxDigits       = 18
	// maxInputLen — задлах текстийн дээд урт (DoS-оос хамгаална).
	maxInputLen = 256
	maxExpDigit = 4
)

// scanDecimal — текстийг (тэмдэг, цифрүүд, 10-ийн зэрэг) болгоно: утга = ±digits × 10^exp.
// digits нь тэргүүн/хойд тэггүй (тэг бол ""). Float ашиглахгүй.
//
// strict:  `-?\d+(\.\d+)?([eE][+-]?\d+)?` — validate package-тай ижил.
// loose=true: гадна ASCII зай, "+" тэмдэг, ".5", "1." зөвшөөрнө.
func scanDecimal(s string, loose bool) (neg bool, digits string, exp int, err error) {
	if loose {
		s = strings.Trim(s, " \t\r\n")
	}
	if s == "" || len(s) > maxInputLen {
		return false, "", 0, ErrInvalid
	}
	i := 0
	switch s[0] {
	case '-':
		neg = true
		i++
	case '+':
		if !loose {
			return false, "", 0, ErrInvalid
		}
		i++
	}
	isDigit := func(j int) bool { return j < len(s) && s[j] >= '0' && s[j] <= '9' }
	st := i
	for isDigit(i) {
		i++
	}
	intPart := s[st:i]
	frac := ""
	hasDot := false
	if i < len(s) && s[i] == '.' {
		hasDot = true
		i++
		fs := i
		for isDigit(i) {
			i++
		}
		frac = s[fs:i]
	}
	if intPart == "" && frac == "" {
		return false, "", 0, ErrInvalid
	}
	if !loose && (intPart == "" || (hasDot && frac == "")) {
		return false, "", 0, ErrInvalid
	}
	e := 0
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		i++
		es := i
		if i < len(s) && (s[i] == '+' || s[i] == '-') {
			i++
		}
		ds := i
		for isDigit(i) {
			i++
		}
		if i == ds || i-ds > maxExpDigit {
			return false, "", 0, ErrInvalid
		}
		n, cerr := strconv.Atoi(s[es:i])
		if cerr != nil {
			return false, "", 0, ErrInvalid
		}
		e = n
	}
	if i != len(s) {
		return false, "", 0, ErrInvalid
	}
	digits = strings.TrimLeft(intPart+frac, "0")
	if digits == "" {
		return false, "", 0, nil // тэг; -0 нь 0
	}
	exp = e - len(frac)
	for digits[len(digits)-1] == '0' { // хойд тэгийг хасч exp-д шилжүүлнэ
		digits = digits[:len(digits)-1]
		exp++
	}
	return neg, digits, exp, nil
}

// scaleDecimal — ±digits×10^exp-ийг 10^-scale нэгжээр бүхэлд хөрвүүлнэ.
// mode == 0 бол яг байх ёстой (үгүй бол ErrPrecision); бусад үед mode-оор дугуйлна.
// Үр дүн |v| <= maxRaw байх ёстой (үгүй бол ErrOverflow).
func scaleDecimal(neg bool, digits string, exp, scale int, mode Rounding) (int64, error) {
	if digits == "" {
		return 0, nil
	}
	if mode != 0 && !mode.Valid() {
		return 0, ErrInvalidRounding
	}
	k := exp + scale
	var v int64
	if k >= 0 {
		if len(digits)+k > maxDigits {
			return 0, ErrOverflow
		}
		n, err := strconv.ParseInt(digits+strings.Repeat("0", k), 10, 64)
		if err != nil {
			return 0, ErrOverflow
		}
		v = n
	} else {
		if mode == 0 {
			return 0, ErrPrecision
		}
		drop := -k
		var kept int64
		half := -1 // хасагдах хэсэг vs 0.5 нэгж
		switch {
		case drop > len(digits):
			// утга < 0.1 нэгж → хагасаас бага
		default:
			if drop < len(digits) {
				if len(digits)-drop > maxDigits {
					return 0, ErrOverflow
				}
				n, err := strconv.ParseInt(digits[:len(digits)-drop], 10, 64)
				if err != nil {
					return 0, ErrOverflow
				}
				kept = n
			}
			d := digits[len(digits)-drop:]
			switch {
			case d[0] > '5':
				half = 1
			case d[0] == '5':
				half = 1
				if strings.Trim(d[1:], "0") == "" {
					half = 0
				}
			}
		}
		if roundsAway(mode, neg, half, true, kept&1 == 1) {
			kept++
		}
		v = kept
	}
	if v > maxRaw {
		return 0, ErrOverflow
	}
	if neg {
		v = -v
	}
	return v, nil
}

// parseScaled — scanDecimal + scaleDecimal, алдаанд оруулсан текстийг (товчилж) хавсаргана.
func parseScaled(s string, scale int, loose bool, mode Rounding) (int64, error) {
	neg, digits, exp, err := scanDecimal(s, loose)
	if err == nil {
		var v int64
		if v, err = scaleDecimal(neg, digits, exp, scale, mode); err == nil {
			return v, nil
		}
	}
	return 0, fmt.Errorf("%w: %q", err, clip(s))
}

func clip(s string) string {
	const n = 40
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// formatScaled — v×10^-scale-ийг plain текст болгоно. trim=true бол хойд тэг (ба хоосон бутархайн цэг) хасна.
func formatScaled(v int64, scale int, trim bool) string {
	u := uint64(v)
	neg := v < 0
	if neg {
		u = -u
	}
	s := strconv.FormatUint(u, 10)
	if len(s) <= scale {
		s = strings.Repeat("0", scale-len(s)+1) + s
	}
	ip, fp := s[:len(s)-scale], s[len(s)-scale:]
	if trim {
		fp = strings.TrimRight(fp, "0")
	}
	out := ip
	if fp != "" {
		out += "." + fp
	}
	if neg {
		out = "-" + out
	}
	return out
}

// group — бүхэл цифрийн мөрийг мянгатын таслалаар бүлэглэнэ ("1234567" → "1,234,567").
func group(ip string) string {
	if len(ip) <= 3 {
		return ip
	}
	var b strings.Builder
	first := len(ip) % 3
	if first > 0 {
		b.WriteString(ip[:first])
	}
	for i := first; i < len(ip); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(ip[i : i+3])
	}
	return b.String()
}
