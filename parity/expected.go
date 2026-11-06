package parity

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// ExpectedEntry — санаатай (зориудын) ялгааны allowlist мөр. Reason ЗААВАЛ.
type ExpectedEntry struct {
	Method   string `json:"method,omitempty"`    // хоосон = дурын
	Path     string `json:"path"`                // glob: "*" дурын тэмдэгт ("/api/hr/*")
	Kind     string `json:"kind,omitempty"`      // status|content-type|header|body|error; хоосон = бүгд
	BodyPath string `json:"body_path,omitempty"` // body ялгааны JSON зам (pattern, "$..x"); хоосон = бүгд
	Header   string `json:"header,omitempty"`    // header ялгааны нэр
	Reason   string `json:"reason"`

	re   *regexp.Regexp
	bp   pathPat
	used int
}

// Expected — allowlist.
type Expected struct{ Entries []*ExpectedEntry }

// LoadExpected — expected.json: {"expected":[...]} эсвэл [...]. Reason-гүй мөр = алдаа.
func LoadExpected(path string) (*Expected, error) {
	e := &Expected{}
	if path == "" {
		return e, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var wrap struct {
		Expected []*ExpectedEntry `json:"expected"`
	}
	if err := json.Unmarshal(b, &wrap); err != nil || wrap.Expected == nil {
		if err2 := json.Unmarshal(b, &wrap.Expected); err2 != nil {
			return nil, fmt.Errorf("expected: %w", err2)
		}
	}
	for i, x := range wrap.Expected {
		if strings.TrimSpace(x.Reason) == "" {
			return nil, fmt.Errorf("expected[%d] (%s): reason заавал байх ёстой", i, x.Path)
		}
		if x.Path == "" {
			return nil, fmt.Errorf("expected[%d]: path хоосон", i)
		}
		x.re = regexp.MustCompile("^" + strings.ReplaceAll(regexp.QuoteMeta(x.Path), `\*`, ".*") + "$")
		if x.BodyPath != "" {
			x.bp = parsePat(x.BodyPath)
		}
		e.Entries = append(e.Entries, x)
	}
	return e, nil
}

// Mark — таарсан ялгаа бүрд Expected шалтгааныг бичнэ. Бүгд expected бол true.
func (e *Expected) Mark(r Request, diffs []Diff) bool {
	if len(diffs) == 0 {
		return false
	}
	all := true
	for i := range diffs {
		matched := false
		for _, x := range e.Entries {
			if x.matches(r, diffs[i]) {
				diffs[i].Expected = x.Reason
				x.used++
				matched = true
				break
			}
		}
		all = all && matched
	}
	return all
}

// Unused — ямар ч ялгаанд таараагүй allowlist мөрүүд (хуучирсан байж болно).
func (e *Expected) Unused() []string {
	var out []string
	for _, x := range e.Entries {
		if x.used == 0 {
			out = append(out, strings.TrimSpace(x.Method+" "+x.Path+" "+x.Kind))
		}
	}
	return out
}

func (x *ExpectedEntry) matches(r Request, d Diff) bool {
	if x.Method != "" && !strings.EqualFold(x.Method, r.M()) {
		return false
	}
	if !x.re.MatchString(r.Path) {
		return false
	}
	if x.Kind != "" && x.Kind != d.Kind {
		return false
	}
	if x.Header != "" && !(d.Kind == KindHeader && strings.EqualFold(d.Path, x.Header)) {
		return false
	}
	if x.BodyPath != "" {
		if d.Kind != KindBody {
			return false
		}
		return x.bp.match(splitDiffPath(d.Path))
	}
	return true
}

// splitDiffPath — "$.a[0].b" -> [a #0 b]; ".length" төгсгөлийг массивын зам гэж үзнэ.
func splitDiffPath(p string) []string {
	p = strings.TrimSuffix(strings.TrimPrefix(p, "$"), ".length")
	var out []string
	for _, seg := range strings.Split(p, ".") {
		if seg == "" {
			continue
		}
		for seg != "" {
			i := strings.IndexByte(seg, '[')
			if i < 0 {
				out = append(out, seg)
				break
			}
			if i > 0 {
				out = append(out, seg[:i])
			}
			j := strings.IndexByte(seg, ']')
			if j < 0 {
				break
			}
			out = append(out, "#"+seg[i+1:j])
			seg = seg[j+1:]
		}
	}
	return out
}
