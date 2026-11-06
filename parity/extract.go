package parity

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Prefixes — extractor зөвхөн эдгээр угтвартай замыг endpoint гэж үзнэ.
var Prefixes = []string{"/api/", "/lambda/", "/mapserver/"}

// CorpusStats — extractor-ийн хамрах хүрээний статистик.
type CorpusStats struct {
	FilesScanned int            `json:"files_scanned"`
	Matches      int            `json:"matches"`    // давхардалтай, нийт таарсан дуудлага
	Endpoints    int            `json:"endpoints"`  // давхардалгүй endpoint
	Resolved     int            `json:"resolved"`   // placeholder-гүй / params-аар бөглөгдсөн
	Unresolved   int            `json:"unresolved"` // бөглөгдөөгүй {placeholder}-тэй
	ByModule     map[string]int `json:"by_module"`
}

// ExtractOptions — extractor-ийн тохиргоо.
type ExtractOptions struct {
	Params map[string]string // placeholder -> бодит id ("chosen.id" эсвэл "id")
	Auth   string            // үүсгэсэн мөр бүрт оноох auth profile
}

var (
	callRe = regexp.MustCompile("(\\.get|\\.head|\\$fetch|useFetch|useLazyFetch)\\(\\s*(`[^`]*`|'[^'\\n]*'|\"[^\"\\n]*\")(\\s*\\+)?")
	// $fetch/useFetch-ийн option дотор method: 'POST' гэх мэт байвал GET биш.
	methodOptRe = regexp.MustCompile(`(?i)method\s*:\s*['"` + "`" + `](post|put|patch|delete)`)
	identRe     = regexp.MustCompile(`[A-Za-z_$][A-Za-z0-9_$]*`)
	holderRe    = regexp.MustCompile(`\{[A-Za-z_][A-Za-z0-9_.]*\}`)
	skipDirs    = map[string]bool{"node_modules": true, ".nuxt": true, ".output": true, "dist": true, ".git": true}
)

// HasPlaceholder — path-д бөглөгдөөгүй {name} үлдсэн эсэх.
func HasPlaceholder(s string) bool { return holderRe.MatchString(s) }

// ExtractFrontend — front-end эх кодоос GET endpoint-уудыг статикаар гаргана.
// Бөглөгдсөн болон бөглөгдөөгүй (placeholder-тэй) хоёр жагсаалтыг тусад нь буцаана.
func ExtractFrontend(root string, opt ExtractOptions) (resolved, unresolved []Request, st CorpusStats, err error) {
	st.ByModule = map[string]int{}
	seen := map[string]bool{}
	werr := filepath.WalkDir(root, func(p string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		switch strings.ToLower(filepath.Ext(p)) {
		case ".vue", ".ts", ".js", ".mjs", ".tsx", ".jsx":
		default:
			return nil
		}
		b, e := os.ReadFile(p)
		if e != nil {
			return e
		}
		st.FilesScanned++
		src := string(b)
		rel, _ := filepath.Rel(root, p)
		for _, m := range callRe.FindAllStringSubmatchIndex(src, -1) {
			call := src[m[2]:m[3]]
			lit := src[m[4]:m[5]]
			concat := m[6] >= 0
			quote := lit[:1]
			raw := lit[1 : len(lit)-1]
			if !hasPrefix(raw) {
				continue
			}
			if call == "$fetch" || call == "useFetch" || call == "useLazyFetch" {
				if methodOptRe.MatchString(callArgs(src, m[1])) {
					continue
				}
			}
			st.Matches++
			if concat {
				raw += "${dynamic}"
				quote = "`"
			}
			line := 1 + strings.Count(src[:m[0]], "\n")
			req, un := buildRequest(raw, quote == "`", opt)
			req.Source = fmt.Sprintf("%s:%d", rel, line)
			req.Auth = opt.Auth
			req.Module = moduleOf(req.Path)
			k := req.Key()
			if seen[k] {
				continue
			}
			seen[k] = true
			if un {
				unresolved = append(unresolved, req)
			} else {
				resolved = append(resolved, req)
				st.ByModule[req.Module]++
			}
		}
		return nil
	})
	if werr != nil {
		return nil, nil, st, werr
	}
	sort.Slice(resolved, func(i, j int) bool { return resolved[i].Key() < resolved[j].Key() })
	sort.Slice(unresolved, func(i, j int) bool { return unresolved[i].Key() < unresolved[j].Key() })
	st.Resolved, st.Unresolved = len(resolved), len(unresolved)
	st.Endpoints = st.Resolved + st.Unresolved
	return resolved, unresolved, st, nil
}

func hasPrefix(s string) bool {
	for _, p := range Prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// buildRequest — template literal-ыг path/query-д салгаж `${expr}`-ийг {name} болгон бөглөнө.
func buildRequest(raw string, tmpl bool, opt ExtractOptions) (Request, bool) {
	pathPart, queryPart := splitQuery(raw)
	// query доторх placeholder-тай хос нь нэмэлт параметр — хаяна.
	var qs []string
	for _, kv := range strings.Split(queryPart, "&") {
		if kv == "" || strings.Contains(kv, "${") {
			continue
		}
		qs = append(qs, kv)
	}
	path, unres := fillTemplate(pathPart, opt.Params)
	return Request{Method: "GET", Path: path, Query: strings.Join(qs, "&")}, unres
}

func splitQuery(s string) (string, string) {
	depth := 0
	for i := 0; i < len(s); i++ {
		switch {
		case strings.HasPrefix(s[i:], "${"):
			depth++
			i++
		case s[i] == '}' && depth > 0:
			depth--
		case s[i] == '?' && depth == 0:
			return s[:i], s[i+1:]
		}
	}
	return s, ""
}

// fillTemplate — `${a.b}`-ийг params["a.b"] эсвэл params["b"]-ээр, олдохгүй бол "{b}"-ээр солино.
func fillTemplate(s string, params map[string]string) (string, bool) {
	var sb strings.Builder
	unres := false
	for i := 0; i < len(s); {
		if strings.HasPrefix(s[i:], "${") {
			depth, j := 1, i+2
			for j < len(s) && depth > 0 {
				if s[j] == '{' {
					depth++
				} else if s[j] == '}' {
					depth--
				}
				j++
			}
			expr := strings.TrimSpace(s[i+2 : j-1])
			ids := identRe.FindAllString(expr, -1)
			name := "param"
			if len(ids) > 0 {
				name = ids[len(ids)-1]
			}
			if v, ok := params[expr]; ok {
				sb.WriteString(v)
			} else if v, ok := params[name]; ok {
				sb.WriteString(v)
			} else {
				sb.WriteString("{" + name + "}")
				unres = true
			}
			i = j
			continue
		}
		sb.WriteByte(s[i])
		i++
	}
	return sb.String(), unres
}

// moduleOf — замаас module нэр гаргана: /api/finance/x -> finance, /api/public/crm/x -> crm.
func moduleOf(p string) string {
	parts := strings.Split(strings.TrimPrefix(p, "/"), "/")
	if len(parts) == 0 {
		return "other"
	}
	switch parts[0] {
	case "lambda", "mapserver":
		return parts[0]
	case "api":
		i := 1
		if i < len(parts) && parts[i] == "public" {
			i++
		}
		if i < len(parts) && parts[i] != "" && !strings.HasPrefix(parts[i], "{") {
			return parts[i]
		}
		return "api"
	}
	return "other"
}

// callArgs — "(" дараах хаалттай дуудлагын үлдсэн аргументууд (дээд тал нь 400 тэмдэгт).
func callArgs(src string, from int) string {
	depth := 1
	end := from
	for end < len(src) && end < from+400 && depth > 0 {
		switch src[end] {
		case '(':
			depth++
		case ')':
			depth--
		}
		end++
	}
	return src[from:end]
}
