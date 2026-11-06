package validate

import (
	"fmt"
	"net/mail"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/khan-vision/khan/auth"
)

// textChecks — урт / NUL / pattern / enum шалгалт (trim хийгдсэн утганд).
func textChecks(kind Kind, s string, r Rules, label string) string {
	if strings.IndexByte(s, 0) >= 0 {
		return label + " зөвшөөрөгдөөгүй тэмдэгт агуулсан байна"
	}
	n := utf8.RuneCountInString(s)
	if r.MinLen > 0 && n < r.MinLen {
		return fmt.Sprintf("%s хамгийн багадаа %d тэмдэгт байх ёстой", label, r.MinLen)
	}
	if max := r.EffectiveMaxLen(kind); max > 0 && n > max {
		return fmt.Sprintf("%s хамгийн ихдээ %d тэмдэгт байх ёстой", label, max)
	}
	if r.Pattern != nil && !r.Pattern.MatchString(s) {
		if r.PatternMsg != "" {
			return r.PatternMsg
		}
		return label + " буруу форматтай байна"
	}
	if len(r.Enum) > 0 && !inEnum(r.Enum, s) {
		return label + " зөвшөөрөгдөөгүй утгатай байна"
	}
	return ""
}

func inEnum(list []string, s string) bool {
	for _, e := range list {
		if e == s {
			return true
		}
	}
	return false
}

func parseText(kind Kind, s string, isStr bool, r Rules, label string) (any, bool, string) {
	if !isStr {
		return nil, true, wantText(label)
	}
	if r.TrimOn() {
		s = strings.TrimSpace(s)
	}
	if msg := textChecks(kind, s, r, label); msg != "" {
		return nil, true, msg
	}
	return s, true, ""
}

func parseEnum(s string, isStr bool, r Rules, label string) (any, bool, string) {
	if !isStr {
		return nil, true, wantText(label)
	}
	s = strings.TrimSpace(s)
	if !inEnum(r.Enum, s) {
		return nil, true, label + " зөвшөөрөгдөөгүй утгатай байна"
	}
	if r.Pattern != nil && !r.Pattern.MatchString(s) {
		return nil, true, label + " буруу форматтай байна"
	}
	return s, true, ""
}

var (
	phoneStrip  = strings.NewReplacer(" ", "", "-", "", "(", "", ")", "", " ", "")
	phoneMN     = regexp.MustCompile(`^[1-9][0-9]{7}$`)
	phoneIntl   = regexp.MustCompile(`^\+[1-9][0-9]{7,14}$`)
	phoneMNIntl = regexp.MustCompile(`^\+976([1-9][0-9]{7})$`)
)

// parsePhone — Монгол дугаар → 8 орон ("99112233"; "+976" угтварыг хасна);
// бусад улс → E.164 ("+14155550123").
func parsePhone(s string, isStr bool, _ Rules, label string) (any, bool, string) {
	if !isStr {
		return nil, true, wantText(label)
	}
	p := phoneStrip.Replace(strings.TrimSpace(s))
	switch {
	case phoneMN.MatchString(p):
		return p, true, ""
	case phoneMNIntl.MatchString(p):
		return phoneMNIntl.FindStringSubmatch(p)[1], true, ""
	case phoneIntl.MatchString(p):
		return p, true, ""
	}
	return nil, true, label + " буруу (8 оронтой дугаар эсвэл +улсын код-той байх ёстой)"
}

func parseEmail(s string, isStr bool, _ Rules, label string) (any, bool, string) {
	if !isStr {
		return nil, true, wantText(label)
	}
	s = strings.ToLower(strings.TrimSpace(s))
	bad := label + " буруу байна (нэр@домэйн.mn хэлбэртэй байх ёстой)"
	if len(s) > 254 || strings.ContainsAny(s, " \t\r\n<>\"") {
		return nil, true, bad
	}
	a, err := mail.ParseAddress(s)
	if err != nil || a.Address != s {
		return nil, true, bad
	}
	at := strings.LastIndexByte(s, '@')
	local, domain := s[:at], s[at+1:]
	if len(local) > 64 || !strings.Contains(domain, ".") || strings.HasPrefix(domain, ".") ||
		strings.HasSuffix(domain, ".") || strings.Contains(domain, "..") {
		return nil, true, bad
	}
	return s, true, ""
}

// parsePassword — bcrypt hash буцаана (auth.HashPassword). Trim хийхгүй.
func parsePassword(s string, isStr bool, r Rules, label string) (any, bool, string) {
	if !isStr {
		return nil, true, wantText(label)
	}
	min := r.MinLen
	if min < MinPasswordLen {
		min = MinPasswordLen
	}
	if utf8.RuneCountInString(s) < min {
		return nil, true, fmt.Sprintf("%s хамгийн багадаа %d тэмдэгт байх ёстой", label, min)
	}
	if len(s) > MaxPasswordBytes {
		return nil, true, fmt.Sprintf("%s %d байтаас урт байж болохгүй", label, MaxPasswordBytes)
	}
	if r.Pattern != nil && !r.Pattern.MatchString(s) {
		if r.PatternMsg != "" {
			return nil, true, r.PatternMsg
		}
		return nil, true, label + " буруу форматтай байна"
	}
	h, err := auth.HashPassword(s)
	if err != nil {
		return nil, true, label + " боловсруулж чадсангүй"
	}
	return h, true, ""
}
