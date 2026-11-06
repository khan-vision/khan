package parity

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"sort"
	"strconv"
	"strings"
)

// IgnoreConfig — normalise/харьцуулалтын тохиргоо (ignore.json).
type IgnoreConfig struct {
	// IgnorePaths — JSON body-оос хасах зам: "$..created_at", "$.data[*].id", "$.meta.*".
	// ".." = дурын гүн, "[*]" эсвэл "*" = нэг түвшин (түлхүүр нь том/жижиг үсэг үл ялгана).
	IgnorePaths []string `json:"ignore_paths,omitempty"`
	// UnorderedPaths — эдгээр замд байгаа массивыг эрэмбэ үл харгалзан харьцуулна.
	UnorderedPaths []string `json:"unordered_paths,omitempty"`
	// IgnoreHeaders / CompareHeaders — харьцуулах header жагсаалтыг засах (жижиг үсгээр).
	IgnoreHeaders  []string `json:"ignore_headers,omitempty"`
	CompareHeaders []string `json:"compare_headers,omitempty"`
	// NoDefaults — DefaultIgnorePaths-ийг ашиглахгүй.
	NoDefaults bool `json:"no_defaults,omitempty"`
}

// DefaultIgnorePaths — үргэлж өөр байх талбарууд. `$..id` зориуд ороогүй (жинхэнэ ялгааг
// нуудаг); бичих тест (mutation) хийхэд ignore.json-д нэмнэ.
var DefaultIgnorePaths = []string{
	"$..created_at", "$..updated_at", "$..deleted_at",
	"$..request_id", "$..requestid", "$..trace_id", "$..etag",
	"$..token", "$..access_token", "$..refresh_token",
}

// DefaultHeaders — харьцуулдаг header-үүд (Set-Cookie тусад нь).
var DefaultHeaders = []string{
	"cache-control", "content-security-policy", "x-content-type-options", "content-disposition",
}

// LoadIgnore — ignore.json унших (зам хоосон бол үндсэн тохиргоо).
func LoadIgnore(path string) (IgnoreConfig, error) {
	var c IgnoreConfig
	if path == "" {
		return c, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("ignore: %w", err)
	}
	return c, nil
}

type pathPat []string // "**" | "*" | "#n" | key

func parsePat(s string) pathPat {
	s = strings.TrimPrefix(strings.TrimSpace(s), "$")
	var out pathPat
	for i := 0; i < len(s); {
		switch {
		case strings.HasPrefix(s[i:], ".."):
			out = append(out, "**")
			i += 2
		case s[i] == '.':
			i++
		case s[i] == '[':
			j := strings.IndexByte(s[i:], ']')
			if j < 0 {
				j = len(s) - i - 1
			}
			in := strings.Trim(s[i+1:i+j], `'"`)
			if in == "*" || in == "" {
				out = append(out, "*")
			} else if _, err := strconv.Atoi(in); err == nil {
				out = append(out, "#"+in)
			} else {
				out = append(out, in)
			}
			i += j + 1
		default:
			j := i
			for j < len(s) && s[j] != '.' && s[j] != '[' {
				j++
			}
			out = append(out, s[i:j])
			i = j
		}
	}
	return out
}

func (p pathPat) match(path []string) bool {
	if len(p) == 0 {
		return len(path) == 0
	}
	if p[0] == "**" {
		for k := 0; k <= len(path); k++ {
			if p[1:].match(path[k:]) {
				return true
			}
		}
		return false
	}
	if len(path) == 0 {
		return false
	}
	if p[0] == "*" || strings.EqualFold(p[0], path[0]) {
		return p[1:].match(path[1:])
	}
	return false
}

type pats []pathPat

func compilePats(list []string) pats {
	var out pats
	for _, s := range list {
		out = append(out, parsePat(s))
	}
	return out
}

func (ps pats) any(path []string) bool {
	for _, p := range ps {
		if p.match(path) {
			return true
		}
	}
	return false
}

// Normalizer — JSON body-г харьцуулж болохуйц каноник хэлбэрт оруулна.
type Normalizer struct {
	ignore    pats
	unordered pats
	headers   []string
	skipHdr   map[string]bool
}

// NewNormalizer — тохиргооноос үүсгэнэ.
func NewNormalizer(c IgnoreConfig) *Normalizer {
	list := append([]string{}, c.IgnorePaths...)
	if !c.NoDefaults {
		list = append(list, DefaultIgnorePaths...)
	}
	n := &Normalizer{ignore: compilePats(list), unordered: compilePats(c.UnorderedPaths), skipHdr: map[string]bool{}}
	for _, h := range c.IgnoreHeaders {
		n.skipHdr[strings.ToLower(h)] = true
	}
	seen := map[string]bool{}
	for _, h := range append(append([]string{}, DefaultHeaders...), c.CompareHeaders...) {
		h = strings.ToLower(h)
		if !seen[h] && !n.skipHdr[h] {
			seen[h] = true
			n.headers = append(n.headers, h)
		}
	}
	return n
}

// num — каноник тоо: 12.5 == 12.50 == 1.25e1; 3 == 3.0.
type num string

// canonNum — big.Rat-аар каноник текст болгоно.
func canonNum(s string) num {
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return num(s)
	}
	if r.IsInt() {
		return num(r.Num().String())
	}
	f := r.FloatString(12)
	f = strings.TrimRight(f, "0")
	return num(strings.TrimSuffix(f, "."))
}

// ParseJSON — body нь ЯГ ганц JSON утга бол мод, үгүй бол ok=false.
func ParseJSON(b []byte) (v any, ok bool) {
	b = bytes.TrimSpace(b)
	if len(b) == 0 {
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return nil, false
	}
	if dec.More() {
		return nil, false
	}
	return v, true
}

// Normalize — ignore замуудыг хасч, тоог каноник болгож, нууц түлхүүрийг нуун, unordered массивыг эрэмбэлнэ.
func (n *Normalizer) Normalize(v any) any { return n.walk(v, nil) }

func (n *Normalizer) walk(v any, path []string) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, c := range t {
			cp := append(append([]string{}, path...), k)
			if n.ignore.any(cp) {
				continue
			}
			if sensitiveKeys[strings.ToLower(k)] {
				out[k] = redacted
				continue
			}
			out[k] = n.walk(c, cp)
		}
		return out
	case []any:
		out := make([]any, 0, len(t))
		for i, c := range t {
			cp := append(append([]string{}, path...), "#"+strconv.Itoa(i))
			if n.ignore.any(cp) {
				continue
			}
			out = append(out, n.walk(c, cp))
		}
		if n.unordered.any(path) {
			keys := make([]string, len(out))
			for i, e := range out {
				keys[i] = canonString(e)
			}
			idx := make([]int, len(out))
			for i := range idx {
				idx[i] = i
			}
			sort.SliceStable(idx, func(a, b int) bool { return keys[idx[a]] < keys[idx[b]] })
			sorted := make([]any, len(out))
			for i, j := range idx {
				sorted[i] = out[j]
			}
			out = sorted
		}
		return out
	case json.Number:
		return canonNum(t.String())
	default:
		return v
	}
}

// canonString — түлхүүр эрэмбэлсэн тогтвортой текст.
func canonString(v any) string {
	var sb strings.Builder
	writeCanon(&sb, v)
	return sb.String()
}

func writeCanon(sb *strings.Builder, v any) {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		sb.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				sb.WriteByte(',')
			}
			sb.WriteString(strconv.Quote(k))
			sb.WriteByte(':')
			writeCanon(sb, t[k])
		}
		sb.WriteByte('}')
	case []any:
		sb.WriteByte('[')
		for i, e := range t {
			if i > 0 {
				sb.WriteByte(',')
			}
			writeCanon(sb, e)
		}
		sb.WriteByte(']')
	case string:
		sb.WriteString(strconv.Quote(t))
	case num:
		sb.WriteString(string(t))
	case nil:
		sb.WriteString("null")
	default:
		fmt.Fprint(sb, t)
	}
}

// DiffJSON — хоёр normalise хийсэн модны ялгааг замтай нь жагсаана.
func DiffJSON(a, b any) []Diff {
	var out []Diff
	diffRec("$", a, b, &out)
	return out
}

func short(s string) string {
	if len(s) > 160 {
		return s[:160] + "..."
	}
	return s
}

func diffRec(path string, a, b any, out *[]Diff) {
	add := func(x, y string) { *out = append(*out, Diff{Kind: KindBody, Path: path, A: short(x), B: short(y)}) }
	switch ta := a.(type) {
	case map[string]any:
		tb, ok := b.(map[string]any)
		if !ok {
			add(canonString(a), canonString(b))
			return
		}
		keys := map[string]bool{}
		for k := range ta {
			keys[k] = true
		}
		for k := range tb {
			keys[k] = true
		}
		sorted := make([]string, 0, len(keys))
		for k := range keys {
			sorted = append(sorted, k)
		}
		sort.Strings(sorted)
		for _, k := range sorted {
			va, inA := ta[k]
			vb, inB := tb[k]
			p := path + "." + k
			switch {
			case !inA:
				*out = append(*out, Diff{Kind: KindBody, Path: p, A: "<байхгүй>", B: short(canonString(vb))})
			case !inB:
				*out = append(*out, Diff{Kind: KindBody, Path: p, A: short(canonString(va)), B: "<байхгүй>"})
			default:
				diffRec(p, va, vb, out)
			}
		}
	case []any:
		tb, ok := b.([]any)
		if !ok {
			add(canonString(a), canonString(b))
			return
		}
		if len(ta) != len(tb) {
			*out = append(*out, Diff{Kind: KindBody, Path: path + ".length", A: strconv.Itoa(len(ta)), B: strconv.Itoa(len(tb))})
		}
		for i := 0; i < len(ta) && i < len(tb); i++ {
			diffRec(fmt.Sprintf("%s[%d]", path, i), ta[i], tb[i], out)
		}
	default:
		if canonString(a) != canonString(b) {
			add(canonString(a), canonString(b))
		}
	}
}
