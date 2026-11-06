package validate

import (
	"encoding/json"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
)

func raw(s string) json.RawMessage { return json.RawMessage(s) }

type pcase struct {
	name    string
	kind    Kind
	in      string
	rules   Rules
	want    any
	present bool
	msg     string // "" = алдаагүй; "*" = ямар нэг алдаа; бусад = агуулсан дэд мөр
}

func runCases(t *testing.T, cases []pcase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, present, msg := Parse(tc.kind, raw(tc.in), tc.rules, "Талбар")
			switch {
			case tc.msg == "" && msg != "":
				t.Fatalf("алдаа гарах ёсгүй: %q", msg)
			case tc.msg == "*" && msg == "":
				t.Fatalf("алдаа хүлээсэн, утга=%v", got)
			case tc.msg != "" && tc.msg != "*" && !strings.Contains(msg, tc.msg):
				t.Fatalf("мессеж %q нь %q-г агуулах ёстой", msg, tc.msg)
			}
			if msg != "" {
				return
			}
			if present != tc.present {
				t.Fatalf("present=%v, хүлээсэн %v", present, tc.present)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("утга=%#v (%T), хүлээсэн %#v (%T)", got, got, tc.want, tc.want)
			}
		})
	}
}

func TestParseAbsentAndEmpty(t *testing.T) {
	runCases(t, []pcase{
		{name: "key байхгүй", kind: Int, in: ``, want: nil, present: false},
		{name: "key байхгүй + Required → caller шалгана", kind: String, in: ``, rules: Rules{Required: true}, want: nil, present: false},
		{name: "null nullable", kind: Int, in: `null`, rules: Rules{Nullable: true}, want: nil, present: true},
		{name: "empty string nullable int", kind: Int, in: `""`, rules: Rules{Nullable: true}, want: nil, present: true},
		{name: "null required", kind: String, in: `null`, rules: Rules{Required: true}, msg: "Талбар заавал"},
		{name: "empty required", kind: Date, in: `""`, rules: Rules{Required: true}, msg: "заавал"},
		{name: "blank required string", kind: String, in: `"   "`, rules: Rules{Required: true}, msg: "заавал"},
		{name: "null non-nullable string → empty", kind: String, in: `null`, want: "", present: true},
		{name: "empty non-nullable text → empty", kind: Text, in: `""`, want: "", present: true},
		{name: "null non-nullable int → илгээгээгүй", kind: Int, in: `null`, want: nil, present: false},
		{name: "empty uuid non-nullable → илгээгээгүй", kind: UUID, in: `""`, want: nil, present: false},
		{name: "nullable string null", kind: String, in: `null`, rules: Rules{Nullable: true}, want: nil, present: true},
		{name: "nullable string blank", kind: String, in: `"  "`, rules: Rules{Nullable: true}, want: nil, present: true},
		{name: "required + nullable → nullable давамгай", kind: String, in: `""`, rules: Rules{Required: true, Nullable: true}, want: nil, present: true},
		{name: "unknown kind", kind: Kind("zzz"), in: `1`, msg: "дэмжигдэхгүй"},
		{name: "bool null required", kind: Bool, in: `null`, rules: Rules{Required: true}, msg: "заавал"},
		{name: "json null nullable", kind: JSON, in: `null`, rules: Rules{Nullable: true}, want: nil, present: true},
		{name: "json empty string is a JSON value", kind: JSON, in: `""`, want: json.RawMessage(`""`), present: true},
	})
}

func TestParseString(t *testing.T) {
	no := false
	cyr := strings.Repeat("Ө", 255)
	runCases(t, []pcase{
		{name: "trim", kind: String, in: `"  Бат  "`, want: "Бат", present: true},
		{name: "trim off", kind: String, in: `"  Бат  "`, rules: Rules{Trim: &no}, want: "  Бат  ", present: true},
		{name: "trim off blank is not empty", kind: String, in: `"  "`, rules: Rules{Trim: &no, Required: true}, want: "  ", present: true},
		{name: "number rejected", kind: String, in: `12`, msg: "текст"},
		{name: "bool rejected", kind: String, in: `true`, msg: "текст"},
		{name: "object rejected", kind: String, in: `{}`, msg: "текст"},
		{name: "default maxlen 255 ok (255 кирилл rune)", kind: String, in: `"` + cyr + `"`, want: cyr, present: true},
		{name: "default maxlen 255 exceeded", kind: String, in: `"` + cyr + `Ө"`, msg: "ихдээ 255"},
		{name: "maxlen rune not bytes", kind: String, in: `"ӨӨӨӨ"`, rules: Rules{MaxLen: 4}, want: "ӨӨӨӨ", present: true},
		{name: "maxlen exceeded rune", kind: String, in: `"ӨӨӨӨӨ"`, rules: Rules{MaxLen: 4}, msg: "ихдээ 4"},
		{name: "minlen", kind: String, in: `"аб"`, rules: Rules{MinLen: 3}, msg: "багадаа 3"},
		{name: "minlen cyrillic ok", kind: String, in: `"абв"`, rules: Rules{MinLen: 3}, want: "абв", present: true},
		{name: "minlen skipped for empty non-required", kind: String, in: `""`, rules: Rules{MinLen: 3}, want: "", present: true},
		{name: "NUL char", kind: String, in: `"a\u0000b"`, msg: "тэмдэгт"},
		{name: "pattern ok", kind: String, in: `"AB-12"`, rules: Rules{Pattern: regexp.MustCompile(`^[A-Z]{2}-\d+$`)}, want: "AB-12", present: true},
		{name: "pattern fail custom msg", kind: String, in: `"x"`, rules: Rules{Pattern: regexp.MustCompile(`^\d+$`), PatternMsg: "Зөвхөн тоо"}, msg: "Зөвхөн тоо"},
		{name: "pattern fail default msg", kind: String, in: `"x"`, rules: Rules{Pattern: regexp.MustCompile(`^\d+$`)}, msg: "буруу форматтай"},
		{name: "string enum", kind: String, in: `"a"`, rules: Rules{Enum: []string{"a", "b"}}, want: "a", present: true},
		{name: "string enum fail", kind: String, in: `"c"`, rules: Rules{Enum: []string{"a", "b"}}, msg: "зөвшөөрөгдөөгүй"},
		{name: "text default maxlen 10000", kind: Text, in: `"` + strings.Repeat("a", 10000) + `"`, want: strings.Repeat("a", 10000), present: true},
		{name: "text over 10000", kind: Text, in: `"` + strings.Repeat("a", 10001) + `"`, msg: "ихдээ 10000"},
		{name: "text keeps inner newline", kind: Text, in: `" a\nb "`, want: "a\nb", present: true},
		{name: "html not stripped (escape is output concern)", kind: String, in: `"<b>x</b>"`, want: "<b>x</b>", present: true},
	})
}

func TestParseInt(t *testing.T) {
	runCases(t, []pcase{
		{name: "zero", kind: Int, in: `0`, want: int64(0), present: true},
		{name: "negative zero", kind: Int, in: `-0`, want: int64(0), present: true},
		{name: "positive", kind: Int, in: `42`, want: int64(42), present: true},
		{name: "negative", kind: Int, in: `-42`, want: int64(-42), present: true},
		{name: "1.0 ok", kind: Int, in: `1.0`, want: int64(1), present: true},
		{name: "1e3 ok", kind: Int, in: `1e3`, want: int64(1000), present: true},
		{name: "1.5 fail", kind: Int, in: `1.5`, msg: "бүхэл тоо"},
		{name: "string numeric", kind: Int, in: `"42"`, want: int64(42), present: true},
		{name: "string leading zero", kind: Int, in: `"07"`, want: int64(7), present: true},
		{name: "string spaces", kind: Int, in: `" 5 "`, want: int64(5), present: true},
		{name: "string 1.5", kind: Int, in: `"1.5"`, msg: "бүхэл тоо"},
		{name: "string plus", kind: Int, in: `"+5"`, msg: "бүхэл тоо"},
		{name: "string hex", kind: Int, in: `"0x10"`, msg: "бүхэл тоо"},
		{name: "string garbage", kind: Int, in: `"abc"`, msg: "бүхэл тоо"},
		{name: "int64 max", kind: Int, in: `9223372036854775807`, want: int64(9223372036854775807), present: true},
		{name: "int64 min", kind: Int, in: `-9223372036854775808`, want: int64(-9223372036854775808), present: true},
		{name: "overflow", kind: Int, in: `9223372036854775808`, msg: "хэт том"},
		{name: "underflow", kind: Int, in: `-9223372036854775809`, msg: "хэт том"},
		{name: "1e400", kind: Int, in: `1e400`, msg: "*"},
		{name: "1e-400 бүхэл биш", kind: Int, in: `1e-400`, msg: "бүхэл тоо"},
		{name: "0e-400 тэг", kind: Int, in: `0e-400`, want: int64(0), present: true},
		{name: "bool rejected", kind: Int, in: `true`, msg: "бүхэл тоо"},
		{name: "object rejected", kind: Int, in: `{"a":1}`, msg: "бүхэл тоо"},
		{name: "array rejected", kind: Int, in: `[1]`, msg: "бүхэл тоо"},
		{name: "min ok", kind: Int, in: `5`, rules: Rules{Min: Bound(5)}, want: int64(5), present: true},
		{name: "min fail", kind: Int, in: `4`, rules: Rules{Min: Bound(5)}, msg: "5-аас бага"},
		{name: "max ok", kind: Int, in: `10`, rules: Rules{Max: Bound(10)}, want: int64(10), present: true},
		{name: "max fail", kind: Int, in: `11`, rules: Rules{Max: Bound(10)}, msg: "10-аас их"},
		{name: "fractional bound", kind: Int, in: `1`, rules: Rules{Min: Bound(1.5)}, msg: "1.5-аас бага"},
	})
}

func TestParseMoneyNumber(t *testing.T) {
	runCases(t, []pcase{
		{name: "money int", kind: Money, in: `12`, want: "12.00", present: true},
		{name: "money 1 decimal", kind: Money, in: `12.5`, want: "12.50", present: true},
		{name: "money string 12.3", kind: Money, in: `"12.3"`, want: "12.30", present: true},
		{name: "money 2 decimals", kind: Money, in: `1234.56`, want: "1234.56", present: true},
		{name: "money trailing zeros ok", kind: Money, in: `1.500`, want: "1.50", present: true},
		{name: "money 0.005 fail", kind: Money, in: `0.005`, msg: "2 оронтой"},
		{name: "money 0.001 string fail", kind: Money, in: `"0.001"`, msg: "2 оронтой"},
		{name: "money zero", kind: Money, in: `0`, want: "0.00", present: true},
		{name: "money -0", kind: Money, in: `-0`, want: "0.00", present: true},
		{name: "money -0.00", kind: Money, in: `"-0.00"`, want: "0.00", present: true},
		{name: "money negative", kind: Money, in: `-5.5`, want: "-5.50", present: true},
		{name: "money small", kind: Money, in: `0.07`, want: "0.07", present: true},
		{name: "money max 16 digits", kind: Money, in: `"9999999999999999.99"`, want: "9999999999999999.99", present: true},
		{name: "money 1e16 fail", kind: Money, in: `1e16`, msg: "хэт том"},
		{name: "money 10^16 string fail", kind: Money, in: `"10000000000000000"`, msg: "хэт том"},
		{name: "money -1e16 fail", kind: Money, in: `-1e16`, msg: "хэт том"},
		{name: "money 1e15 ok", kind: Money, in: `1e15`, want: "1000000000000000.00", present: true},
		{name: "money exponent decimal", kind: Money, in: `1.5e2`, want: "150.00", present: true},
		{name: "money exponent negative", kind: Money, in: `5e-1`, want: "0.50", present: true},
		{name: "money huge exponent", kind: Money, in: `1e999999`, msg: "*"},
		{name: "money garbage", kind: Money, in: `"12,5"`, msg: "тоо байх"},
		{name: "money .5 fail", kind: Money, in: `".5"`, msg: "тоо байх"},
		{name: "money 5. fail", kind: Money, in: `"5."`, msg: "тоо байх"},
		{name: "money bool", kind: Money, in: `true`, msg: "тоо байх"},
		{name: "money NaN string", kind: Money, in: `"NaN"`, msg: "тоо байх"},
		{name: "money Infinity string", kind: Money, in: `"Infinity"`, msg: "тоо байх"},
		{name: "money long digits", kind: Money, in: `"` + strings.Repeat("9", 500) + `"`, msg: "*"},
		{name: "money min fail", kind: Money, in: `0.99`, rules: Rules{Min: Bound(1)}, msg: "1-аас бага"},
		{name: "money min exact 0.1 decimal exactness", kind: Money, in: `0.10`, rules: Rules{Min: Bound(0.1)}, want: "0.10", present: true},
		{name: "money max fail", kind: Money, in: `100.01`, rules: Rules{Max: Bound(100)}, msg: "100-аас их"},
		{name: "number default scale 6", kind: Number, in: `1.123456`, want: "1.123456", present: true},
		{name: "number default scale 6 exceeded", kind: Number, in: `1.1234567`, msg: "6 оронтой"},
		{name: "number scale 3 strips zeros", kind: Number, in: `"1.500"`, rules: Rules{Scale: 3}, want: "1.5", present: true},
		{name: "number scale 3 exceeded", kind: Number, in: `1.0005`, rules: Rules{Scale: 3}, msg: "3 оронтой"},
		{name: "number integer", kind: Number, in: `7`, want: "7", present: true},
		{name: "number negative", kind: Number, in: `-0.5`, want: "-0.5", present: true},
		{name: "number -0", kind: Number, in: `-0.0`, want: "0", present: true},
		{name: "number 18 int digits", kind: Number, in: `"123456789012345678"`, want: "123456789012345678", present: true},
		{name: "number 19 int digits", kind: Number, in: `"1234567890123456789"`, msg: "хэт том"},
		{name: "number small exponent", kind: Number, in: `1e-6`, want: "0.000001", present: true},
		{name: "number too small exponent", kind: Number, in: `1e-7`, msg: "6 оронтой"},
		{name: "number max bound", kind: Number, in: `5.5`, rules: Rules{Max: Bound(5.4)}, msg: "5.4-аас их"},
	})
}

func TestParseBool(t *testing.T) {
	runCases(t, []pcase{
		{name: "true", kind: Bool, in: `true`, want: true, present: true},
		{name: "false", kind: Bool, in: `false`, want: false, present: true},
		{name: "1", kind: Bool, in: `1`, want: true, present: true},
		{name: "0", kind: Bool, in: `0`, want: false, present: true},
		{name: "2 rejected", kind: Bool, in: `2`, msg: "тийм/үгүй"},
		{name: "string rejected", kind: Bool, in: `"true"`, msg: "тийм/үгүй"},
		{name: "array rejected", kind: Bool, in: `[]`, msg: "тийм/үгүй"},
		{name: "required false ok", kind: Bool, in: `false`, rules: Rules{Required: true}, want: false, present: true},
	})
}

func TestParseDateTimeTime(t *testing.T) {
	ts := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	runCases(t, []pcase{
		{name: "date ok", kind: Date, in: `"2026-02-28"`, want: "2026-02-28", present: true},
		{name: "date leap day", kind: Date, in: `"2028-02-29"`, want: "2028-02-29", present: true},
		{name: "date non-leap feb 29", kind: Date, in: `"2026-02-29"`, msg: "огноо буруу"},
		{name: "date 02-30", kind: Date, in: `"2026-02-30"`, msg: "огноо буруу"},
		{name: "date month 13", kind: Date, in: `"2026-13-01"`, msg: "огноо буруу"},
		{name: "date no padding", kind: Date, in: `"2026-1-2"`, msg: "огноо буруу"},
		{name: "date with time rejected", kind: Date, in: `"2026-01-02T00:00:00Z"`, msg: "огноо буруу"},
		{name: "date year 0000", kind: Date, in: `"0000-01-01"`, msg: "огноо буруу"},
		{name: "date trimmed", kind: Date, in: `" 2026-01-02 "`, want: "2026-01-02", present: true},
		{name: "date number rejected", kind: Date, in: `20260102`, msg: "текст"},
		{name: "date slash", kind: Date, in: `"2026/01/02"`, msg: "огноо буруу"},
		{name: "datetime Z", kind: DateTime, in: `"2026-03-04T05:06:07Z"`, want: ts, present: true},
		{name: "datetime offset → UTC", kind: DateTime, in: `"2026-03-04T13:06:07+08:00"`, want: ts, present: true},
		{name: "datetime fraction", kind: DateTime, in: `"2026-03-04T05:06:07.5Z"`, want: ts.Add(500 * time.Millisecond), present: true},
		{name: "datetime no zone rejected", kind: DateTime, in: `"2026-03-04T05:06:07"`, msg: "буруу"},
		{name: "datetime date only rejected", kind: DateTime, in: `"2026-03-04"`, msg: "буруу"},
		{name: "datetime bad hour", kind: DateTime, in: `"2026-03-04T25:00:00Z"`, msg: "буруу"},
		{name: "time HH:MM", kind: Time, in: `"09:30"`, want: "09:30:00", present: true},
		{name: "time HH:MM:SS", kind: Time, in: `"23:59:59"`, want: "23:59:59", present: true},
		{name: "time 24:00", kind: Time, in: `"24:00"`, msg: "цаг буруу"},
		{name: "time 9:30 no pad", kind: Time, in: `"9:30"`, msg: "цаг буруу"},
		{name: "time seconds 60", kind: Time, in: `"10:10:60"`, msg: "цаг буруу"},
	})
}

func TestParseUUIDEnumJSON(t *testing.T) {
	const u = "123e4567-e89b-12d3-a456-426614174000"
	runCases(t, []pcase{
		{name: "uuid lower", kind: UUID, in: `"` + u + `"`, want: u, present: true},
		{name: "uuid upper → lower", kind: UUID, in: `"` + strings.ToUpper(u) + `"`, want: u, present: true},
		{name: "uuid nil", kind: UUID, in: `"00000000-0000-0000-0000-000000000000"`, want: "00000000-0000-0000-0000-000000000000", present: true},
		{name: "uuid braces", kind: UUID, in: `"{` + u + `}"`, msg: "UUID"},
		{name: "uuid no dashes", kind: UUID, in: `"123e4567e89b12d3a456426614174000"`, msg: "UUID"},
		{name: "uuid short", kind: UUID, in: `"123e4567-e89b-12d3-a456-42661417400"`, msg: "UUID"},
		{name: "uuid non-hex", kind: UUID, in: `"123e4567-e89b-12d3-a456-42661417400g"`, msg: "UUID"},
		{name: "uuid number", kind: UUID, in: `5`, msg: "текст"},
		{name: "enum ok", kind: Enum, in: `"draft"`, rules: Rules{Enum: []string{"draft", "posted"}}, want: "draft", present: true},
		{name: "enum case sensitive", kind: Enum, in: `"Draft"`, rules: Rules{Enum: []string{"draft"}}, msg: "зөвшөөрөгдөөгүй"},
		{name: "enum unknown", kind: Enum, in: `"x"`, rules: Rules{Enum: []string{"draft"}}, msg: "зөвшөөрөгдөөгүй"},
		{name: "enum empty list", kind: Enum, in: `"x"`, msg: "зөвшөөрөгдөөгүй"},
		{name: "enum trimmed", kind: Enum, in: `" draft "`, rules: Rules{Enum: []string{"draft"}}, want: "draft", present: true},
		{name: "enum number", kind: Enum, in: `1`, rules: Rules{Enum: []string{"1"}}, msg: "текст"},
		{name: "json object compact", kind: JSON, in: `{ "a" : [1, 2] }`, want: json.RawMessage(`{"a":[1,2]}`), present: true},
		{name: "json array", kind: JSON, in: `[1]`, want: json.RawMessage(`[1]`), present: true},
		{name: "json number", kind: JSON, in: `5`, want: json.RawMessage(`5`), present: true},
		{name: "json invalid", kind: JSON, in: `{"a":`, msg: "JSON буруу"},
		{name: "json too big", kind: JSON, in: `"` + strings.Repeat("a", MaxJSONBytes) + `"`, msg: "хэт том"},
	})
}

func TestParsePhoneEmailPassword(t *testing.T) {
	runCases(t, []pcase{
		{name: "phone 8", kind: Phone, in: `"99112233"`, want: "99112233", present: true},
		{name: "phone spaced", kind: Phone, in: `"9911 2233"`, want: "99112233", present: true},
		{name: "phone dashed", kind: Phone, in: `"99-11-22-33"`, want: "99112233", present: true},
		{name: "phone +976", kind: Phone, in: `"+976 99112233"`, want: "99112233", present: true},
		{name: "phone intl", kind: Phone, in: `"+1 (415) 555-0123"`, want: "+14155550123", present: true},
		{name: "phone leading zero", kind: Phone, in: `"09911223"`, msg: "буруу"},
		{name: "phone 7 digits", kind: Phone, in: `"9911223"`, msg: "буруу"},
		{name: "phone 9 digits no plus", kind: Phone, in: `"991122334"`, msg: "буруу"},
		{name: "phone letters", kind: Phone, in: `"99ab2233"`, msg: "буруу"},
		{name: "phone number type", kind: Phone, in: `99112233`, msg: "текст"},
		{name: "email ok lower", kind: Email, in: `" Bat@Example.MN "`, want: "bat@example.mn", present: true},
		{name: "email no at", kind: Email, in: `"bat.example.mn"`, msg: "буруу"},
		{name: "email no dot domain", kind: Email, in: `"bat@localhost"`, msg: "буруу"},
		{name: "email display name", kind: Email, in: `"Bat <bat@example.mn>"`, msg: "буруу"},
		{name: "email space", kind: Email, in: `"ba t@example.mn"`, msg: "буруу"},
		{name: "email double dot", kind: Email, in: `"bat@example..mn"`, msg: "буруу"},
		{name: "email too long", kind: Email, in: `"` + strings.Repeat("a", 250) + `@x.mn"`, msg: "буруу"},
		{name: "password short", kind: Password, in: `"abc1234"`, msg: "багадаа 8"},
		{name: "password minlen raised", kind: Password, in: `"abcdefgh"`, rules: Rules{MinLen: 10}, msg: "багадаа 10"},
		{name: "password minlen below 8 clamps", kind: Password, in: `"abc"`, rules: Rules{MinLen: 3}, msg: "багадаа 8"},
		{name: "password too long", kind: Password, in: `"` + strings.Repeat("a", 73) + `"`, msg: "72 байт"},
		{name: "password pattern", kind: Password, in: `"abcdefgh"`, rules: Rules{Pattern: regexp.MustCompile(`\d`), PatternMsg: "Тоо оруулна уу"}, msg: "Тоо оруулна уу"},
	})
}

func TestParsePasswordHash(t *testing.T) {
	v, present, msg := Parse(Password, raw(`"  secret-pass-1 "`), Rules{}, "Нууц үг")
	if msg != "" || !present {
		t.Fatalf("msg=%q present=%v", msg, present)
	}
	h, ok := v.(string)
	if !ok || !strings.HasPrefix(h, "$2") || strings.Contains(h, "secret") {
		t.Fatalf("bcrypt hash хүлээсэн: %v", v)
	}
	// Trim хийгдэхгүй: зай нь нууц үгийн хэсэг.
	if _, _, m := Parse(Password, raw(`"        "`), Rules{}, "x"); m != "" {
		t.Fatalf("8 зай нь хүчинтэй нууц үг (trim хийхгүй): %q", m)
	}
	// Оруулаагүй/хоосон нууц үг → илгээгээгүй (update-д хуучин hash хэвээр).
	if _, present, m := Parse(Password, raw(`""`), Rules{}, "x"); present || m != "" {
		t.Fatalf("хоосон нууц үг: present=%v msg=%q", present, m)
	}
}

func TestParseDefaultLabel(t *testing.T) {
	_, _, msg := Parse(Int, raw(`"x"`), Rules{}, "")
	if !strings.HasPrefix(msg, "Утга ") {
		t.Fatalf("label анхдагч: %q", msg)
	}
}

func TestParseMessagesUseLabel(t *testing.T) {
	_, _, msg := Parse(Int, raw(`1.5`), Rules{}, "Тоо хэмжээ")
	if !strings.HasPrefix(msg, "Тоо хэмжээ ") {
		t.Fatalf("label угтвар: %q", msg)
	}
}

func TestDecimalHelpers(t *testing.T) {
	for _, tc := range []struct {
		in    string
		str   string
		fixed int
		fx    string
	}{
		{"0", "0", 2, "0.00"}, {"-0", "0", 2, "0.00"}, {"0.50", "0.5", 2, "0.50"}, {"007.100", "7.1", 3, "7.100"},
		{"12e-1", "1.2", 1, "1.2"}, {"1.25e2", "125", 0, "125"}, {"-0.005", "-0.005", 3, "-0.005"},
	} {
		d, err := parseDec(tc.in)
		if err != nil {
			t.Fatalf("%s: %v", tc.in, err)
		}
		if d.String() != tc.str || d.fixed(tc.fixed) != tc.fx {
			t.Errorf("%s: String=%s fixed=%s", tc.in, d.String(), d.fixed(tc.fixed))
		}
	}
	for _, bad := range []string{"", "+1", " 1", "1 ", ".5", "5.", "1e", "1e+", "--1", "1.2.3", "0x1", "1_0", "e5", "١٢"} {
		if _, err := parseDec(bad); err == nil {
			t.Errorf("%q буруу гэж үзэх ёстой", bad)
		}
	}
	cmp := func(a, b string) int {
		x, _ := parseDec(a)
		y, _ := parseDec(b)
		return x.cmp(y)
	}
	for _, tc := range []struct {
		a, b string
		want int
	}{
		{"1", "2", -1}, {"2", "1", 1}, {"1.0", "1", 0}, {"-1", "1", -1}, {"-2", "-1", -1}, {"0", "-0", 0}, {"0", "0.001", -1},
		{"-0.001", "0", -1}, {"100", "99.99", 1}, {"0.1", "0.10", 0}, {"12345678901234567890", "12345678901234567891", -1},
	} {
		if got := cmp(tc.a, tc.b); got != tc.want {
			t.Errorf("cmp(%s,%s)=%d, хүлээсэн %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestIsUUIDExhaustive(t *testing.T) {
	if !IsUUID("ABCDEF01-2345-6789-abcd-ef0123456789") {
		t.Fatal("холимог үсэг зөвшөөрөх ёстой")
	}
	for _, bad := range []string{"", "x", strings.Repeat("a", 36), "12345678-1234-1234-1234-12345678901", "12345678_1234-1234-1234-123456789012"} {
		if IsUUID(bad) {
			t.Errorf("%q UUID биш", bad)
		}
	}
}
