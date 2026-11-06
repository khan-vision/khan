package validate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Parse — нэг талбарын түүхий JSON утгыг шалгаж, DB-д бэлэн утга болгоно.
//
// Буцаах утга:
//   - present=false, msg="": утга ирээгүй (raw хоосон) ЭСВЭЛ ирсэн ч хоосон
//     (null/"") бөгөөд талбар Nullable/Required биш, String/Text биш — "илгээгээгүй"
//     гэж үзнэ (create-д Default/DB default, update-д өөрчлөхгүй).
//   - present=true, value=nil: NULL бичнэ (Nullable үед null/"").
//   - msg != "": алдаа (Монгол мессеж); value ашиглахгүй.
//
// Required: ирсэн боловч хоосон бол msg="<label> заавал". Утга огт ирээгүй (raw
// хоосон) үед Required-ийг caller (create-д) шалгана — Parse нь present=false буцаана.
//
// Value-ийн Go төрөл: String/Text/Enum/Date/Time/UUID/Phone/Email/Password/Money/Number
// → string; Int → int64; Bool → bool; DateTime → time.Time (UTC); JSON → json.RawMessage.
func Parse(kind Kind, raw json.RawMessage, r Rules, label string) (value any, present bool, msg string) {
	if label == "" {
		label = "Утга"
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil, false, ""
	}
	if !kind.Valid() {
		return nil, true, label + " төрөл дэмжигдэхгүй"
	}

	str, isStr := rawString(raw)
	if isEmptyValue(kind, r, raw, str, isStr) {
		switch {
		case r.Nullable:
			return nil, true, ""
		case r.Required:
			return nil, true, label + " заавал"
		case kind.textual():
			return "", true, ""
		default:
			return nil, false, ""
		}
	}

	switch kind {
	case String, Text:
		return parseText(kind, str, isStr, r, label)
	case Enum:
		return parseEnum(str, isStr, r, label)
	case Phone:
		return parsePhone(str, isStr, r, label)
	case Email:
		return parseEmail(str, isStr, r, label)
	case Password:
		return parsePassword(str, isStr, r, label)
	case UUID:
		return parseUUID(str, isStr, label)
	case Date:
		return parseDate(str, isStr, label)
	case DateTime:
		return parseDateTime(str, isStr, label)
	case Time:
		return parseClock(str, isStr, label)
	case Bool:
		return parseBool(raw, str, isStr, label)
	case Int:
		return parseInt(raw, str, isStr, r, label)
	case Number, Money:
		return parseDecimal(kind, raw, str, isStr, r, label)
	case JSON:
		return parseJSON(raw, label)
	}
	return nil, true, label + " төрөл дэмжигдэхгүй"
}

// rawString — raw нь JSON мөр бол задалсан утга.
func rawString(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || raw[0] != '"' {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	return s, true
}

func isEmptyValue(kind Kind, r Rules, raw json.RawMessage, s string, isStr bool) bool {
	if string(raw) == "null" {
		return true
	}
	if !isStr || kind == JSON {
		return false
	}
	switch {
	case kind == Password:
		return s == ""
	case kind.textual() && !r.TrimOn():
		return s == ""
	}
	return strings.TrimSpace(s) == ""
}

func wantText(label string) string { return label + " текст байх ёстой" }

// ─── тоо ───

func numberText(raw json.RawMessage, s string, isStr bool) (string, bool) {
	if isStr {
		return strings.TrimSpace(s), true
	}
	if len(raw) > 0 && (raw[0] == '-' || (raw[0] >= '0' && raw[0] <= '9')) {
		return string(raw), true
	}
	return "", false
}

func checkBounds(d dec, r Rules, label string) string {
	if r.Min != nil {
		if m, ok := decFromFloat(*r.Min); ok && d.cmp(m) < 0 {
			return fmt.Sprintf("%s %s-аас бага байж болохгүй", label, fmtBound(*r.Min))
		}
	}
	if r.Max != nil {
		if m, ok := decFromFloat(*r.Max); ok && d.cmp(m) > 0 {
			return fmt.Sprintf("%s %s-аас их байж болохгүй", label, fmtBound(*r.Max))
		}
	}
	return ""
}

func fmtBound(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

func parseInt(raw json.RawMessage, s string, isStr bool, r Rules, label string) (any, bool, string) {
	txt, ok := numberText(raw, s, isStr)
	if !ok {
		return nil, true, label + " бүхэл тоо байх ёстой"
	}
	d, err := parseDec(txt)
	if err != nil || d.scale != 0 {
		return nil, true, label + " бүхэл тоо байх ёстой"
	}
	n, ok := d.int64()
	if !ok {
		return nil, true, label + " хэт том тоо байна"
	}
	if msg := checkBounds(d, r, label); msg != "" {
		return nil, true, msg
	}
	return n, true, ""
}

func parseDecimal(kind Kind, raw json.RawMessage, s string, isStr bool, r Rules, label string) (any, bool, string) {
	txt, ok := numberText(raw, s, isStr)
	if !ok {
		return nil, true, label + " тоо байх ёстой"
	}
	d, err := parseDec(txt)
	if err != nil {
		return nil, true, label + " тоо байх ёстой"
	}
	scale := r.EffectiveScale(kind)
	if d.scale > scale {
		if kind == Money {
			return nil, true, label + " дээд тал нь 2 оронтой бутархайтай байх ёстой"
		}
		return nil, true, fmt.Sprintf("%s дээд тал нь %d оронтой бутархайтай байх ёстой", label, scale)
	}
	maxInt := numberIntDigits
	if kind == Money {
		maxInt = moneyIntDigits
	}
	if d.intDigits() > maxInt {
		return nil, true, label + " хэт том тоо байна"
	}
	if msg := checkBounds(d, r, label); msg != "" {
		return nil, true, msg
	}
	if kind == Money {
		return d.fixed(moneyScale), true, ""
	}
	return d.String(), true, ""
}

// ─── bool / огноо / цаг ───

func parseBool(raw json.RawMessage, _ string, isStr bool, label string) (any, bool, string) {
	if !isStr {
		switch string(raw) {
		case "true", "1":
			return true, true, ""
		case "false", "0":
			return false, true, ""
		}
	}
	return nil, true, label + " тийм/үгүй утгатай байх ёстой"
}

func parseDate(s string, isStr bool, label string) (any, bool, string) {
	if !isStr {
		return nil, true, wantText(label)
	}
	s = strings.TrimSpace(s)
	t, err := time.Parse("2006-01-02", s)
	if err != nil || t.Year() < 1 || len(s) != 10 {
		return nil, true, label + " огноо буруу (ЖЖЖЖ-СС-ӨӨ байх ёстой)"
	}
	return s, true, ""
}

func parseDateTime(s string, isStr bool, label string) (any, bool, string) {
	if !isStr {
		return nil, true, wantText(label)
	}
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(s))
	if err != nil || t.Year() < 1 || t.Year() > 9999 {
		return nil, true, label + " огноо, цаг буруу (RFC3339 байх ёстой)"
	}
	return t.UTC(), true, ""
}

func parseClock(s string, isStr bool, label string) (any, bool, string) {
	if !isStr {
		return nil, true, wantText(label)
	}
	s = strings.TrimSpace(s)
	layout := "15:04:05"
	if len(s) == 5 {
		layout = "15:04"
	}
	t, err := time.Parse(layout, s)
	if err != nil || (len(s) != 5 && len(s) != 8) {
		return nil, true, label + " цаг буруу (ЦЦ:ММ эсвэл ЦЦ:ММ:СС байх ёстой)"
	}
	return t.Format("15:04:05"), true, ""
}

// ─── uuid / json ───

func parseUUID(s string, isStr bool, label string) (any, bool, string) {
	if !isStr {
		return nil, true, wantText(label)
	}
	s = strings.ToLower(strings.TrimSpace(s))
	if !IsUUID(s) {
		return nil, true, label + " буруу (UUID байх ёстой)"
	}
	return s, true, ""
}

// IsUUID — каноник 8-4-4-4-12 (том/жижиг үсэг) эсэх.
func IsUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := 0; i < 36; i++ {
		c := s[i]
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
				return false
			}
		}
	}
	return true
}

func parseJSON(raw json.RawMessage, label string) (any, bool, string) {
	if len(raw) > MaxJSONBytes {
		return nil, true, fmt.Sprintf("%s хэт том (дээд тал нь %d KiB)", label, MaxJSONBytes>>10)
	}
	if !json.Valid(raw) {
		return nil, true, label + " JSON буруу"
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return nil, true, label + " JSON буруу"
	}
	// Postgres jsonb нь \u0000-г хүлээж авдаггүй (22P05 → 500) — оролтын алдаа болгоно.
	if bytes.Contains(buf.Bytes(), []byte(`\u0000`)) {
		return nil, true, label + " хориотой тэмдэгт (\\u0000) агуулсан"
	}
	return json.RawMessage(buf.Bytes()), true, ""
}
