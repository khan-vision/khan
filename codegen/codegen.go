// Package codegen нь crud.Meta-аас front_end-д зориулсан TypeScript (interface, zod schema,
// KvFieldDef[], endpoint) болон OpenAPI 3.1 JSON үүсгэнэ.
//
// Codegen нь зөвхөн OFFLINE ажиллана: апп өөрийн resource-уудаа импортолсон жижиг командтай
// байх ёстой (khan CLI апп-ийн resource-ийг импортлож чадахгүй). Гаралтыг git-д commit хийнэ.
//
//	// cmd/kvgen/main.go
//	metas := []crud.Meta{rooms.Meta(), invoices.Meta()}
//	ts, err := codegen.TS(metas, codegen.TSOptions{})
//	if err != nil { log.Fatal(err) }
//	if err := codegen.WriteFile("front_end/src/generated/crud.ts", ts); err != nil { log.Fatal(err) }
//
// Гаралт тогтвортой (resource нэрээр эрэмблэгдсэн, талбар зарласан дарааллаараа) тул
// өөрчлөлтгүй үед файл дахин бичигдэхгүй.
package codegen

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// WriteFile — data-г path-д бичнэ, гэхдээ агуулга өөрчлөгдөөгүй бол файлыг хөндөхгүй
// (mtime хэвээр → watcher/CI дахин ажиллахгүй). Хавтас байхгүй бол үүсгэнэ; бичихдээ
// түр файл + rename (хагас бичигдсэн файл үлдэхгүй).
func WriteFile(path string, data []byte) error {
	if path == "" {
		return fmt.Errorf("khan/codegen: path хоосон")
	}
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, data) {
		return nil
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("khan/codegen: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".khan-codegen-*")
	if err != nil {
		return fmt.Errorf("khan/codegen: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // rename амжилттай бол алдаа үл тооно
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("khan/codegen: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("khan/codegen: %w", err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return fmt.Errorf("khan/codegen: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("khan/codegen: %w", err)
	}
	return nil
}

var pascalRe = regexp.MustCompile(`^[A-Z][A-Za-z0-9]*$`)

// pascalOf — "organization.conference_room" → "ConferenceRoom" (сүүлийн сегмент, `_`-ээр тасалж).
// Давхцлыг дуудагч илрүүлнэ.
func pascalOf(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("khan/codegen: resource нэр хоосон")
	}
	seg := name
	if i := strings.LastIndex(name, "."); i >= 0 {
		seg = name[i+1:]
	}
	var b strings.Builder
	for _, part := range strings.Split(seg, "_") {
		if part == "" {
			continue
		}
		for i, r := range part {
			if !(r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r))) {
				return "", fmt.Errorf("khan/codegen: %q нэрнээс TS identifier гаргах боломжгүй (тэмдэгт %q)", name, r)
			}
			if i == 0 {
				r = unicode.ToUpper(r)
			}
			b.WriteRune(r)
		}
	}
	p := b.String()
	if !pascalRe.MatchString(p) {
		return "", fmt.Errorf("khan/codegen: %q нэрнээс TS identifier гаргах боломжгүй (%q)", name, p)
	}
	return p, nil
}

// resourcePascal — Names override-тай Pascal нэр.
func resourcePascal(name string, names map[string]string) (string, error) {
	if o, ok := names[name]; ok {
		if !pascalRe.MatchString(o) {
			return "", fmt.Errorf("khan/codegen: Names[%q]=%q буруу identifier (PascalCase)", name, o)
		}
		return o, nil
	}
	return pascalOf(name)
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToLower(r[0])
	return string(r)
}

// jsStr — JS мөр литерал (JSON кодчилол, HTML escape-гүй; Кирилл хэвээр).
func jsStr(s string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s) // string кодлоход алдаа гарахгүй
	return strings.TrimRight(buf.String(), "\n")
}

// jsonLit — дурын JSON литерал (тогтвортой: map түлхүүр эрэмблэгдэнэ).
func jsonLit(v any) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	return strings.TrimRight(buf.String(), "\n"), nil
}

// docText — `/** ... */` comment-д аюулгүй текст.
func docText(s string) string {
	s = strings.NewReplacer("\r", " ", "\n", " ", "*/", "*\\/").Replace(s)
	return strings.TrimSpace(s)
}

func fmtNum(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

var identRe = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*$`)

// propKey — object property түлхүүр (identifier бол хэвээр, үгүй бол quote).
func propKey(s string) string {
	if identRe.MatchString(s) {
		return s
	}
	return jsStr(s)
}
