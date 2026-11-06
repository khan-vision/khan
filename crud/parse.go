package crud

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/gofiber/fiber/v3"
	"github.com/khan-vision/khan/validate"
)

// readBody — JSON объект body-г задална (хэмжээ: MaxBody → 400).
func (e *engine) readBody(c fiber.Ctx) (map[string]json.RawMessage, error) {
	body := bytes.TrimSpace(c.Body())
	if len(body) > e.maxBody {
		return nil, BadRequest(msgTooLarge)
	}
	return decodeObject(body)
}

func decodeObject(body []byte) (map[string]json.RawMessage, error) {
	if len(body) == 0 {
		return nil, BadRequest(msgBadJSON)
	}
	var m map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(&m); err != nil || m == nil {
		return nil, BadRequest(msgBadJSON)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) { // арын хогийн өгөгдөл
		return nil, BadRequest(msgBadJSON)
	}
	return m, nil
}

// parseFields — талбаруудыг allowlist-ээр parse хийнэ (багана → утга). create=false үед
// зөвхөн body-д байгаа талбар, CreateOnly үл тооно. Алдааг errs-д prefix+Name түлхүүрээр.
func parseFields(fields []fieldPlan, body map[string]json.RawMessage, create bool, errs validate.Errors, prefix string) Values {
	vals := Values{}
	for i := range fields {
		f := &fields[i]
		if !create && f.CreateOnly {
			continue
		}
		v, present, msg := validate.Parse(f.Kind, body[f.Name], f.Rules, f.label)
		switch {
		case msg != "":
			errs.Add(prefix+f.Name, msg)
		case present:
			vals[f.col] = v
		case create && f.hasDef:
			vals[f.col] = f.def
		case create && f.Rules.Required:
			errs.Add(prefix+f.Name, f.label+" заавал")
		}
	}
	return vals
}

// checkUnknown — StrictUnknown үед зарлаагүй түлхүүр бүрт 422.
func checkUnknown(body map[string]json.RawMessage, known map[string]bool, errs validate.Errors, prefix string) {
	keys := make([]string, 0)
	for k := range body {
		if !known[k] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		errs.Add(prefix+k, "Үл мэдэгдэх талбар")
	}
}

// parseVersion — update body дахь version (бүхэл, >= 0). Алга бол 422.
func (e *engine) parseVersion(body map[string]json.RawMessage, errs validate.Errors) (int64, bool) {
	raw := body[e.versionCol]
	v, present, msg := validate.Parse(validate.Int, raw, validate.Rules{Required: true, Min: validate.Bound(0)}, e.versionCol)
	switch {
	case msg != "":
		errs.Add(e.versionCol, msg)
	case !present || v == nil:
		errs.Add(e.versionCol, e.versionCol+" заавал")
	default:
		return v.(int64), true
	}
	return 0, false
}

// childRow — body дахь child-ийн нэг мөр.
type childRow struct {
	idx  int
	id   string // хоосон = шинэ мөр
	vals Values
}

// childInput — нэг child-ийн жагсаалт (body-д түлхүүр байгаа үед).
type childInput struct {
	rows []childRow
}

// parseChildren — body-д массив байгаа child бүрийг задална. Түлхүүр алга (эсвэл null)
// бол child өөрчлөгдөхгүй. create=true үед id-г үл тооно (бүх мөр шинэ).
func (e *engine) parseChildren(body map[string]json.RawMessage, create bool, errs validate.Errors) map[string]*childInput {
	out := map[string]*childInput{}
	for ci := range e.children {
		cp := &e.children[ci]
		raw := bytes.TrimSpace(body[cp.Name])
		if len(raw) == 0 || string(raw) == "null" {
			continue
		}
		var items []json.RawMessage
		if raw[0] != '[' || json.Unmarshal(raw, &items) != nil {
			errs.Add(cp.Name, cp.label+" жагсаалт (массив) байх ёстой")
			continue
		}
		if len(items) > cp.maxRows {
			errs.Add(cp.Name, fmt.Sprintf("%s хамгийн ихдээ %d мөртэй байх ёстой", cp.label, cp.maxRows))
			continue
		}
		in := &childInput{rows: make([]childRow, 0, len(items))}
		seen := map[string]bool{}
		known := map[string]bool{cp.idCol: true}
		for i := range cp.fields {
			known[cp.fields[i].Name] = true
		}
		for i, item := range items {
			prefix := fmt.Sprintf("%s.%d.", cp.Name, i)
			obj, err := decodeObject(bytes.TrimSpace(item))
			if err != nil {
				errs.Add(fmt.Sprintf("%s.%d", cp.Name, i), "Мөр буруу (объект байх ёстой)")
				continue
			}
			row := childRow{idx: i}
			if !create {
				if id, ok := parseChildID(obj[cp.idCol], errs, prefix+cp.idCol); ok && id != "" {
					if seen[id] {
						errs.Add(prefix+cp.idCol, "Давхардсан id")
					}
					seen[id] = true
					row.id = id
				}
			}
			row.vals = parseFields(cp.fields, obj, create || row.id == "", errs, prefix)
			if e.strictUnknown {
				checkUnknown(obj, known, errs, prefix)
			}
			in.rows = append(in.rows, row)
		}
		out[cp.Name] = in
	}
	return out
}

// parseChildID — мөрийн id: хоосон/null → шинэ мөр; UUID бол жижиг үсэгтэй.
func parseChildID(raw json.RawMessage, errs validate.Errors, field string) (string, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return "", true
	}
	var s string
	if raw[0] != '"' || json.Unmarshal(raw, &s) != nil {
		errs.Add(field, "id буруу")
		return "", false
	}
	if s == "" {
		return "", true
	}
	if !validate.IsUUID(s) {
		errs.Add(field, "id буруу")
		return "", false
	}
	return lowerUUID(s), true
}

func lowerUUID(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'F' {
			b[i] = c + 32
		}
	}
	return string(b)
}
