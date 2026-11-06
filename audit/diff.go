package audit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/khan-vision/khan/obs"
)

// Redacted — нууц талбарын утгын оронд хадгалагдах тэмдэг (obs.Redacted-тэй ижил).
const Redacted = obs.Redacted

// Change — нэг талбарын өөрчлөлт. JSON дүрслэл нь `[old, new]` хос.
type Change struct {
	Old any
	New any
}

// MarshalJSON — `[old, new]`.
func (c Change) MarshalJSON() ([]byte, error) { return json.Marshal([2]any{c.Old, c.New}) }

// UnmarshalJSON — `[old, new]` хосыг уншина (тоо json.Number хэлбэрээр, цифр алдагдахгүй).
func (c *Change) UnmarshalJSON(b []byte) error {
	var pair []json.RawMessage
	if err := json.Unmarshal(b, &pair); err != nil {
		return err
	}
	if len(pair) != 2 {
		return fmt.Errorf("audit: өөрчлөлт [old, new] хос байх ёстой, %d элемент ирлээ", len(pair))
	}
	var err error
	if c.Old, err = decodeJSON(pair[0]); err != nil {
		return err
	}
	c.New, err = decodeJSON(pair[1])
	return err
}

// Changes — өөрчлөгдсөн талбар → [old, new]. JSON-д түлхүүр цагаан толгойн дарааллаар
// (encoding/json map-ийг эрэмбэлдэг) гарна, тиймээс ижил өөрчлөлт үргэлж ижил байт болно.
type Changes map[string]Change

// Fields — өөрчлөгдсөн талбаруудын нэрс (эрэмбэлсэн).
func (cs Changes) Fields() []string {
	out := make([]string, 0, len(cs))
	for k := range cs {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Old — өөрчлөлт бүрийн ХУУЧИН утга (буцаах/restore-д). Нууцлагдсан («***») утга оруулагдахгүй.
func (cs Changes) Old() map[string]any {
	out := make(map[string]any, len(cs))
	for k, c := range cs {
		if s, ok := c.Old.(string); ok && s == Redacted {
			continue
		}
		out[k] = c.Old
	}
	return out
}

// New — өөрчлөлт бүрийн ШИНЭ утга. Нууцлагдсан («***») утга оруулагдахгүй.
func (cs Changes) New() map[string]any {
	out := make(map[string]any, len(cs))
	for k, c := range cs {
		if s, ok := c.New.(string); ok && s == Redacted {
			continue
		}
		out[k] = c.New
	}
	return out
}

// DiffOptions — Diff / Snapshot-ийн тохиргоо. Түлхүүрийн нэр яг таарна (том/жижиг үсэг ялгана).
type DiffOptions struct {
	// Partial — true үед new-д БАЙХГҮЙ түлхүүрийг «өөрчлөгдөөгүй» гэж үзнэ (хэсэгчилсэн
	// PUT: body-д ирсэн талбар л). false үед байхгүй түлхүүр = nil (өгөгдлийн мөр бүтнээрээ
	// харьцуулагдана: create → old=nil, delete → new=nil).
	Partial bool
	// Exclude — бүхэлд нь орхигдох top-level түлхүүрүүд (үр дүнд огт орохгүй).
	Exclude []string
	// Redact — өөрчлөгдсөн нь бүртгэгдэх боловч утга нь «***» болох түлхүүрүүд (Password төрөл г.м.).
	// Хос талын аль нэг нь nil бол nil хэвээр (тохируулсан/арилгасныг харуулна).
	Redact []string
	// Reveal — түлхүүрийн нэрээрээ автоматаар нууцлагдах (password, token, secret, …;
	// obs.SensitiveKey) боловч ил хадгалагдах ёстой түлхүүрүүд. Анхаар: зөвхөн нэрээр
	// ялгадаг тул утга нь үнэхээр нууц биш гэдэгт итгэлтэй үед л ашигла.
	Reveal []string
}

type optSet struct {
	exclude, redact, reveal map[string]bool
}

func (o DiffOptions) compile() optSet {
	toSet := func(a []string) map[string]bool {
		m := make(map[string]bool, len(a))
		for _, s := range a {
			m[s] = true
		}
		return m
	}
	return optSet{exclude: toSet(o.Exclude), redact: toSet(o.Redact), reveal: toSet(o.Reveal)}
}

// secret — түлхүүр нууцлагдах ёстой эсэх (тодорхой Redact, эсвэл нэрээр автомат).
func (s optSet) secret(key string) bool {
	if s.redact[key] {
		return true
	}
	return !s.reveal[key] && obs.SensitiveKey(key)
}

// Diff — old → new хоорондох ЗӨВХӨН өөрчлөгдсөн талбаруудыг буцаана (хэзээ ч nil биш).
//
// Харьцуулалт нь төрлөөс үл хамаарна: 12.5, "12.50", json.Number("12.500"), int 12 vs 12.0,
// өөр бүсийн ижил time.Time, JSON-ийн түлхүүрийн дараалал — өөрчлөлт БИШ. Мөр ба тоо нь
// ЗӨВХӨН нэг тал нь тоо үед тоогоор харьцуулагдана (текст «007» ба «7» өөр). nil ба
// хоосон мөр (""), 0, false өөр утга. Нууц түлхүүр (DiffOptions.Redact, нэрээр автомат)
// «***» болно; мөр утгын доторх token/JWT/Bearer нь obs.RedactString-ээр далдлагдана.
func Diff(old, new map[string]any, opts DiffOptions) Changes {
	o := opts.compile()
	keys := make(map[string]struct{}, len(old)+len(new))
	for k := range old {
		keys[k] = struct{}{}
	}
	for k := range new {
		keys[k] = struct{}{}
	}
	out := Changes{}
	for k := range keys {
		if o.exclude[k] {
			continue
		}
		nv, inNew := new[k]
		if opts.Partial && !inNew {
			continue
		}
		a, b := normalize(old[k]), normalize(nv)
		if equal(a, b) {
			continue
		}
		out[k] = Change{Old: o.render(k, a), New: o.render(k, b)}
	}
	return out
}

// Snapshot — мөрийн бүтэн төлвийг (түлхүүр бүр) JSON-д аюулгүй хэлбэрт оруулна:
// DiffOptions.Exclude хасагдана, нууц түлхүүр «***» болно. nil → nil.
func Snapshot(row map[string]any, opts DiffOptions) map[string]any {
	if row == nil {
		return nil
	}
	o := opts.compile()
	out := make(map[string]any, len(row))
	for k, v := range row {
		if o.exclude[k] {
			continue
		}
		out[k] = o.render(k, normalize(v))
	}
	return out
}

// render — normalize хийгдсэн утгыг хадгалах хэлбэрт оруулна (нууц түлхүүрийг далдлана).
func (s optSet) render(key string, v any) any {
	if v != nil && s.secret(key) {
		return Redacted
	}
	return s.out(v)
}

// out — normalize хийгдсэн утгыг JSON-д тохирох хэлбэрт: тоо → json.Number, цаг → UTC RFC3339,
// мөр → obs.RedactString, nested map-ийн нууц түлхүүр → «***».
func (s optSet) out(v any) any {
	switch t := v.(type) {
	case nil, bool:
		return v
	case string:
		return obs.RedactString(t)
	case dec:
		return json.Number(t.jsonText())
	case time.Time:
		return t.Format(time.RFC3339Nano)
	case []any:
		out := make([]any, len(t))
		for i := range t {
			out[i] = s.out(t[i])
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			out[k] = s.render(k, x)
		}
		return out
	}
	return fmt.Sprint(v) // normalize өөр төрөл буцаахгүй
}

// ─── normalize / equal ───

// dec — normalize хийгдсэн тоо: canon нь харьцуулалтын каноник хэлбэр, text нь анхны бичлэг.
type dec struct{ canon, text string }

var jsonNumRe = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?$`)

func (d dec) jsonText() string {
	if jsonNumRe.MatchString(d.text) {
		return d.text
	}
	return d.canon
}

// normalize — дурын утгыг харьцуулах каноник хэлбэрт оруулна: nil, bool, string, dec,
// time.Time (UTC), []any, map[string]any.
func normalize(v any) any {
	switch t := v.(type) {
	case nil:
		return nil
	case bool, string:
		return t
	case []byte:
		return string(t)
	case json.RawMessage:
		if len(t) == 0 {
			return nil
		}
		x, err := decodeJSON(t)
		if err != nil {
			return string(t)
		}
		return normalize(x)
	case json.Number:
		return numberOrString(string(t))
	case int:
		return fromInt(int64(t))
	case int8:
		return fromInt(int64(t))
	case int16:
		return fromInt(int64(t))
	case int32:
		return fromInt(int64(t))
	case int64:
		return fromInt(t)
	case uint:
		return fromText(strconv.FormatUint(uint64(t), 10))
	case uint8:
		return fromText(strconv.FormatUint(uint64(t), 10))
	case uint16:
		return fromText(strconv.FormatUint(uint64(t), 10))
	case uint32:
		return fromText(strconv.FormatUint(uint64(t), 10))
	case uint64:
		return fromText(strconv.FormatUint(t, 10))
	case float32:
		return fromFloat(float64(t), 32)
	case float64:
		return fromFloat(t, 64)
	case time.Time:
		return t.UTC()
	case *time.Time:
		if t == nil {
			return nil
		}
		return t.UTC()
	case []any:
		out := make([]any, len(t))
		for i := range t {
			out[i] = normalize(t[i])
		}
		return out
	case []map[string]any:
		out := make([]any, len(t))
		for i := range t {
			out[i] = normalize(t[i])
		}
		return out
	case map[string]any:
		if t == nil {
			return nil
		}
		out := make(map[string]any, len(t))
		for k, x := range t {
			out[k] = normalize(x)
		}
		return out
	}
	// Бусад төрөл (struct, typed slice/map, pointer): JSON-оор дамжуулж каноник болгоно.
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	x, err := decodeJSON(raw)
	if err != nil {
		return fmt.Sprint(v)
	}
	switch x.(type) { // JSON-ийн үр дүн дахин энэ салаанд орохгүй (энгийн төрөл)
	case nil, bool, string, json.Number, []any, map[string]any:
		return normalize(x)
	}
	return fmt.Sprint(v)
}

func decodeJSON(b []byte) (any, error) {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var x any
	if err := d.Decode(&x); err != nil {
		return nil, err
	}
	return x, nil
}

func fromInt(n int64) any { return fromText(strconv.FormatInt(n, 10)) }

func fromText(s string) any {
	c, ok := canonDecimal(s)
	if !ok {
		return s
	}
	return dec{canon: c, text: s}
}

func numberOrString(s string) any { return fromText(s) }

func fromFloat(f float64, bits int) any {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return strconv.FormatFloat(f, 'g', -1, bits)
	}
	return fromText(strconv.FormatFloat(f, 'f', -1, bits))
}

var decRe = regexp.MustCompile(`^([+-]?)([0-9]*)(?:\.([0-9]*))?(?:[eE]([+-]?[0-9]+))?$`)

// canonDecimal — аравтын тоон бичлэгийг каноник мөр болгоно (12.50 → 12.5, 1e3 → 1000,
// 007 → 7, -0 → 0). Тоо биш бол ok=false. Маш том экспонент `<цифр>e<exp>` хэлбэртэй.
func canonDecimal(s string) (string, bool) {
	m := decRe.FindStringSubmatch(s)
	if m == nil || (m[2] == "" && m[3] == "") {
		return "", false
	}
	exp := 0
	if m[4] != "" {
		n, err := strconv.Atoi(m[4])
		if err != nil || n > 100000 || n < -100000 {
			return "", false
		}
		exp = n
	}
	digits := m[2] + m[3]
	exp -= len(m[3])
	digits = strings.TrimLeft(digits, "0")
	if digits == "" {
		return "0", true
	}
	trimmed := strings.TrimRight(digits, "0")
	exp += len(digits) - len(trimmed)
	digits = trimmed

	var out string
	switch {
	case exp >= 0 && exp <= 40:
		out = digits + strings.Repeat("0", exp)
	case exp < 0 && -exp < len(digits):
		out = digits[:len(digits)+exp] + "." + digits[len(digits)+exp:]
	case exp < 0 && -exp <= len(digits)+40:
		out = "0." + strings.Repeat("0", -exp-len(digits)) + digits
	default:
		out = digits + "e" + strconv.Itoa(exp)
	}
	if m[1] == "-" {
		out = "-" + out
	}
	return out, true
}

// equal — normalize хийгдсэн хоёр утгын тэнцэл.
func equal(a, b any) bool {
	switch x := a.(type) {
	case nil:
		return b == nil
	case bool:
		y, ok := b.(bool)
		return ok && x == y
	case string:
		switch y := b.(type) {
		case string:
			return x == y
		case dec: // «12.50» (мөр) ↔ 12.5 (тоо)
			c, ok := canonDecimal(x)
			return ok && c == y.canon
		case time.Time:
			return stringIsTime(x, y)
		}
		return false
	case dec:
		switch y := b.(type) {
		case dec:
			return x.canon == y.canon
		case string:
			return equal(b, a)
		}
		return false
	case time.Time:
		switch y := b.(type) {
		case time.Time:
			return x.Equal(y)
		case string:
			return stringIsTime(y, x)
		}
		return false
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !equal(x[i], y[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for k, xv := range x {
			yv, ok := y[k]
			if !ok || !equal(xv, yv) {
				return false
			}
		}
		return true
	}
	return false
}

// stringIsTime — мөр нь RFC3339 (эсвэл YYYY-MM-DD, UTC 00:00) цаг бөгөөд t-тэй ижил агшин эсэх.
func stringIsTime(s string, t time.Time) bool {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02"} {
		if p, err := time.Parse(layout, s); err == nil {
			return p.Equal(t)
		}
	}
	return false
}
