package parity

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"mime"
	"net/http"
	"sort"
	"strings"
)

// Diff-ийн төрлүүд.
const (
	KindStatus      = "status"
	KindContentType = "content-type"
	KindHeader      = "header"
	KindBody        = "body"
	KindError       = "error"
)

// Diff — нэг ялгаа.
type Diff struct {
	Kind     string `json:"kind"`
	Path     string `json:"path,omitempty"` // JSON зам эсвэл header нэр
	A        string `json:"a"`
	B        string `json:"b"`
	Expected string `json:"expected,omitempty"` // allowlist-ийн шалтгаан
}

// Response — нэг талын хариу.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
	Err    string
}

// ContentFamily — content-type-ийг json/html/text/xml/binary/none гэсэн бүлэгт оруулна.
func ContentFamily(ct string) string {
	if ct == "" {
		return "none"
	}
	mt, _, err := mime.ParseMediaType(ct)
	if err != nil {
		mt = strings.ToLower(strings.TrimSpace(strings.SplitN(ct, ";", 2)[0]))
	}
	switch {
	case mt == "application/json" || strings.HasSuffix(mt, "+json"):
		return "json"
	case mt == "text/html":
		return "html"
	case mt == "text/xml" || mt == "application/xml" || strings.HasSuffix(mt, "+xml"):
		return "xml"
	case strings.HasPrefix(mt, "text/"):
		return "text"
	}
	return "binary:" + mt
}

// CookieShape — Set-Cookie-г утгагүйгээр: нэр + атрибутын нэр (Path/Domain/SameSite-ийн утгатай, Expires/Max-Age-ийн утгагүй).
func CookieShape(sc string) string {
	parts := strings.Split(sc, ";")
	name := strings.TrimSpace(parts[0])
	if i := strings.IndexByte(name, '='); i >= 0 {
		name = name[:i]
	}
	var attrs []string
	for _, p := range parts[1:] {
		p = strings.TrimSpace(p)
		k, v, _ := strings.Cut(p, "=")
		lk := strings.ToLower(k)
		switch lk {
		case "expires", "max-age":
			attrs = append(attrs, lk)
		case "path", "domain", "samesite":
			attrs = append(attrs, lk+"="+strings.ToLower(v))
		default:
			attrs = append(attrs, lk)
		}
	}
	sort.Strings(attrs)
	return name + "; " + strings.Join(attrs, "; ")
}

func cookieShapes(h http.Header) string {
	var s []string
	for _, c := range h.Values("Set-Cookie") {
		s = append(s, CookieShape(c))
	}
	sort.Strings(s)
	return strings.Join(s, " | ")
}

// BodyHash — JSON бус биеийн хэш+урт.
func BodyHash(b []byte) string {
	h := sha256.Sum256(b)
	return fmt.Sprintf("sha256:%s len=%d", hex.EncodeToString(h[:8]), len(b))
}

// Compare — хоёр хариуг харьцуулж ялгааны жагсаалт буцаана (хоосон = ижил).
func (n *Normalizer) Compare(a, b Response) []Diff {
	var out []Diff
	switch {
	case a.Err != "" && b.Err != "":
		if a.Err != b.Err {
			out = append(out, Diff{Kind: KindError, A: a.Err, B: b.Err})
		}
		return out
	case a.Err != "" || b.Err != "":
		return []Diff{{Kind: KindError, A: orOK(a.Err), B: orOK(b.Err)}}
	}
	if a.Status != b.Status {
		out = append(out, Diff{Kind: KindStatus, A: fmt.Sprint(a.Status), B: fmt.Sprint(b.Status)})
	}
	fa, fb := ContentFamily(a.Header.Get("Content-Type")), ContentFamily(b.Header.Get("Content-Type"))
	if fa != fb {
		out = append(out, Diff{Kind: KindContentType, A: fa, B: fb})
	}
	for _, h := range n.headers {
		va, vb := strings.Join(a.Header.Values(h), ", "), strings.Join(b.Header.Values(h), ", ")
		if va != vb {
			out = append(out, Diff{Kind: KindHeader, Path: h, A: short(va), B: short(vb)})
		}
	}
	if !n.skipHdr["set-cookie"] {
		if ca, cb := cookieShapes(a.Header), cookieShapes(b.Header); ca != cb {
			out = append(out, Diff{Kind: KindHeader, Path: "set-cookie", A: ca, B: cb})
		}
	}
	// Body
	if fa == "json" && fb == "json" {
		ja, okA := ParseJSON(a.Body)
		jb, okB := ParseJSON(b.Body)
		if okA && okB {
			out = append(out, DiffJSON(n.Normalize(ja), n.Normalize(jb))...)
			return out
		}
	}
	if ha, hb := BodyHash(a.Body), BodyHash(b.Body); ha != hb {
		out = append(out, Diff{Kind: KindBody, Path: "(raw)", A: ha, B: hb})
	}
	return out
}

func orOK(s string) string {
	if s == "" {
		return "(алдаагүй)"
	}
	return s
}
