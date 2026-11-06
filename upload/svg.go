package upload

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// maxSVGDepth — XML-ийн үүрлэлтийн дээд гүн.
const maxSVGDepth = 256

const nsXHTML = "http://www.w3.org/1999/xhtml"

// svgForbiddenElements — SVG-д ХЭЗЭЭ Ч зөвшөөрөхгүй элемент (жижиг үсгээр, namespace-гүй
// нэр). Скрипт ажиллуулах, гадны агуулга шигтгэх, DOM-д HTML оруулах боломжууд.
var svgForbiddenElements = map[string]bool{
	"script": true, "foreignobject": true, "iframe": true, "embed": true, "object": true,
	"applet": true, "link": true, "meta": true, "base": true, "handler": true,
	"listener": true, "audio": true, "video": true, "canvas": true, "form": true,
	"input": true, "button": true, "textarea": true, "body": true, "html": true, "head": true,
}

// svgURLAttrs — URL агуулдаг attribute-ууд: зөвхөн `#id`, http(s) эсвэл растр data: URI.
var svgURLAttrs = map[string]bool{
	"href": true, "src": true, "action": true, "formaction": true, "data": true, "poster": true,
}

var dataImageRe = regexp.MustCompile(`^data:image/(png|jpe?g|gif|webp)(;base64)?,`)

// sanitizeSVG — SVG-г XML tokenizer-ээр шалгаж, аюултай ямар нэг зүйл олдвол
// алдаа буцаана (засч өөрчлөхгүй — цэвэр бол яг эх хэвээр хадгалагдана).
//
// Татгалзах зүйлс: DOCTYPE/ENTITY (XXE, billion laughs), `<?xml-stylesheet?>`
// зэрэг processing instruction, svgForbiddenElements, XHTML namespace-ийн элемент,
// `on*` attribute, `javascript:`/`vbscript:` агуулсан дурын attribute утга,
// href/src зэрэг URL-ийн allowlist-д үл таарах утга, animate/set-ээр href эсвэл
// on*-ийг тохируулах, CSS (`style` элемент/attribute)-д `@import`, `\` escape,
// `expression(`, `behavior`, `-moz-binding`, `#`/растр data: биш `url()`.
func sanitizeSVG(data []byte) error {
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = true
	dec.Entity = nil // зөвхөн 5 стандарт entity

	var (
		depth     int
		sawRoot   bool
		stack     []string
		styleText strings.Builder
	)
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("SVG XML буруу байна: %w", err)
		}
		switch t := tok.(type) {
		case xml.Directive:
			return errors.New("SVG-д DOCTYPE/ENTITY зөвшөөрөгдөхгүй")
		case xml.ProcInst:
			if t.Target != "xml" {
				return errors.New("SVG-д processing instruction зөвшөөрөгдөхгүй")
			}
		case xml.StartElement:
			name := strings.ToLower(t.Name.Local)
			if depth == 0 {
				if sawRoot {
					return errors.New("SVG-д нэгээс олон root элемент байна")
				}
				sawRoot = true
				if name != "svg" {
					return errors.New("SVG-ийн root элемент <svg> биш байна")
				}
			}
			depth++
			if depth > maxSVGDepth {
				return errors.New("SVG хэт гүн үүрлэсэн байна")
			}
			if svgForbiddenElements[name] || t.Name.Space == nsXHTML {
				return fmt.Errorf("SVG-д <%s> элемент зөвшөөрөгдөхгүй", name)
			}
			if err := checkSVGAttrs(name, t.Attr); err != nil {
				return err
			}
			stack = append(stack, name)
		case xml.EndElement:
			depth--
			if n := len(stack); n > 0 {
				if stack[n-1] == "style" {
					if err := checkCSS(styleText.String()); err != nil {
						return err
					}
					styleText.Reset()
				}
				stack = stack[:n-1]
			}
		case xml.CharData:
			if depth == 0 {
				if len(bytes.TrimSpace(t)) > 0 {
					return errors.New("SVG root-ийн гадна текст байна")
				}
				continue
			}
			if stack[len(stack)-1] == "style" {
				if styleText.Len()+len(t) > 256<<10 {
					return errors.New("SVG <style> хэт том байна")
				}
				styleText.Write(t)
			}
		}
	}
	if !sawRoot {
		return errors.New("SVG-д root элемент алга")
	}
	return nil
}

func checkSVGAttrs(element string, attrs []xml.Attr) error {
	for _, a := range attrs {
		name := strings.ToLower(a.Name.Local)
		if strings.HasPrefix(name, "on") {
			return fmt.Errorf("SVG-д %q event attribute зөвшөөрөгдөхгүй", a.Name.Local)
		}
		val := normalizeAttr(a.Value)
		if strings.Contains(val, "javascript:") || strings.Contains(val, "vbscript:") {
			return fmt.Errorf("SVG-ийн %q attribute-д скрипт URL байна", a.Name.Local)
		}
		switch {
		case svgURLAttrs[name]:
			if !allowedURL(a.Value) {
				return fmt.Errorf("SVG-ийн %q attribute-ийн URL зөвшөөрөгдөхгүй", a.Name.Local)
			}
		case name == "style":
			if err := checkCSS(a.Value); err != nil {
				return err
			}
		case name == "attributename" && (element == "animate" || element == "set" ||
			element == "animatetransform" || element == "animatemotion"):
			target := val
			if i := strings.LastIndexByte(target, ':'); i >= 0 {
				target = target[i+1:]
			}
			if strings.HasPrefix(target, "on") || target == "href" {
				return errors.New("SVG animation нь href/event attribute-ийг өөрчилж болохгүй")
			}
		}
	}
	return nil
}

// normalizeAttr — хоосон зай, control тэмдэгтийг хасаж жижигрүүлнэ
// (`java\tscript:` мэт далдлалтыг илрүүлэх).
func normalizeAttr(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if r <= 0x20 || r == 0x7F {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func allowedURL(v string) bool {
	v = strings.TrimSpace(v)
	if v == "" {
		return true
	}
	n := normalizeAttr(v)
	switch {
	case strings.HasPrefix(n, "#"):
		return true
	case strings.HasPrefix(n, "https://"), strings.HasPrefix(n, "http://"):
		return true
	}
	return dataImageRe.MatchString(n)
}

// checkCSS — <style> эсвэл style="" агуулгын шалгалт.
func checkCSS(css string) error {
	l := strings.ToLower(css)
	if strings.Contains(l, `\`) {
		return errors.New("SVG-ийн CSS-д `\\` escape зөвшөөрөгдөхгүй")
	}
	n := normalizeAttr(l)
	for _, bad := range []string{"@import", "javascript:", "vbscript:", "expression(", "behavior:", "-moz-binding"} {
		if strings.Contains(n, bad) {
			return fmt.Errorf("SVG-ийн CSS-д %q зөвшөөрөгдөхгүй", bad)
		}
	}
	rest := n
	for {
		i := strings.Index(rest, "url(")
		if i < 0 {
			return nil
		}
		rest = rest[i+4:]
		end := strings.IndexByte(rest, ')')
		if end < 0 {
			return errors.New("SVG-ийн CSS url() хаагдаагүй байна")
		}
		inner := strings.Trim(rest[:end], `'"`)
		if !strings.HasPrefix(inner, "#") && !dataImageRe.MatchString(inner) {
			return errors.New("SVG-ийн CSS url() зөвхөн #id эсвэл растр data: байж болно")
		}
		rest = rest[end:]
	}
}
