package codegen

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/khan-vision/khan/crud"
	"github.com/khan-vision/khan/validate"
)

// Info — OpenAPI document-ийн мэдээлэл.
type Info struct {
	Title       string // анхдагч "Khan API"
	Version     string // анхдагч "1.0.0"
	Description string
	Servers     []string // server URL-ууд
	// MetaRoutes — true бол БҮХ resource-д GET <endpoint>/meta бичнэ. false бол зөвхөн
	// crud.Meta.ExposeMeta=true resource-д.
	MetaRoutes bool
	// Names — resource Name → schema нэр (PascalCase) override (TSOptions.Names-тэй ижил).
	Names map[string]string
}

type obj = map[string]any

// OpenAPI — metas-аас OpenAPI 3.1 JSON (deterministic, indent-тэй). crud-ийн mount хийдэг route
// бүр (list, query, export.xlsx, bulk, meta, options, CRUD) болон 400/401/403/404/409/422/500
// алдааны envelope (docs/CRUD.md §1), bearerAuth security scheme-тэй.
//
// Money/Number: хүсэлтэд JSON тоо ЭСВЭЛ тоон мөр, хариунд JSON тоо.
func OpenAPI(metas []crud.Meta, info Info) ([]byte, error) {
	if info.Title == "" {
		info.Title = "Khan API"
	}
	if info.Version == "" {
		info.Version = "1.0.0"
	}
	sorted := append([]crud.Meta(nil), metas...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	paths := obj{}
	schemas := sharedSchemas()
	claimed := map[string]string{}
	for k := range schemas {
		claimed[k] = "shared"
	}
	endpoints := map[string]string{}
	for _, m := range sorted {
		pascal, err := resourcePascal(m.Name, info.Names)
		if err != nil {
			return nil, err
		}
		if !strings.HasPrefix(m.Endpoint, "/") || strings.ContainsAny(m.Endpoint, "{} ") || (len(m.Endpoint) > 1 && strings.HasSuffix(m.Endpoint, "/")) {
			return nil, fmt.Errorf("khan/codegen: %s: Endpoint буруу: %q", m.Name, m.Endpoint)
		}
		if prev, ok := endpoints[m.Endpoint]; ok {
			return nil, fmt.Errorf("khan/codegen: Endpoint %q давхцав (%s, %s)", m.Endpoint, prev, m.Name)
		}
		endpoints[m.Endpoint] = m.Name
		g := &oaGen{m: m, pascal: pascal, info: info}
		for _, f := range m.Fields {
			if !f.Kind.Valid() {
				return nil, fmt.Errorf("khan/codegen: %s.%s: үл мэдэгдэх kind %q", m.Name, f.Name, f.Kind)
			}
		}
		for _, c := range m.Children {
			if _, err := pascalOf("x." + c.Name); err != nil {
				return nil, fmt.Errorf("khan/codegen: %s: child %q: %w", m.Name, c.Name, err)
			}
			for _, f := range c.Fields {
				if !f.Kind.Valid() {
					return nil, fmt.Errorf("khan/codegen: %s.%s.%s: үл мэдэгдэх kind %q", m.Name, c.Name, f.Name, f.Kind)
				}
			}
		}
		own := g.schemas()
		for k, v := range own {
			if prev, ok := claimed[k]; ok {
				return nil, fmt.Errorf("khan/codegen: schema %q давхцав (%s ба %s) — Info.Names-ээр ялга", k, prev, m.Name)
			}
			claimed[k] = m.Name
			schemas[k] = v
		}
		g.addPaths(paths)
	}

	doc := obj{
		"openapi": "3.1.0",
		"info":    obj{"title": info.Title, "version": info.Version},
		"paths":   paths,
		"components": obj{
			"schemas":   schemas,
			"responses": errorResponses(),
			"securitySchemes": obj{
				"bearerAuth": obj{"type": "http", "scheme": "bearer", "bearerFormat": "JWT",
					"description": "JWT (Authorization: Bearer). org_id зөвхөн token-оос авагдана."},
			},
		},
		"security": []any{obj{"bearerAuth": []any{}}},
	}
	if info.Description != "" {
		doc["info"].(obj)["description"] = info.Description
	}
	if len(info.Servers) > 0 {
		servers := make([]any, len(info.Servers))
		for i, s := range info.Servers {
			servers[i] = obj{"url": s}
		}
		doc["servers"] = servers
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("khan/codegen: %w", err)
	}
	return append(out, '\n'), nil
}

func ref(name string) obj { return obj{"$ref": "#/components/schemas/" + name} }

func respRef(name string) obj { return obj{"$ref": "#/components/responses/" + name} }

func jsonContent(schema any) obj {
	return obj{"application/json": obj{"schema": schema}}
}

func sharedSchemas() obj {
	return obj{
		"Error": obj{"type": "object", "required": []any{"error"}, "properties": obj{
			"error": obj{"type": "string"}}},
		"ValidationError": obj{"type": "object", "required": []any{"error", "errors"}, "properties": obj{
			"error": obj{"type": "string", "examples": []any{"Мэдээлэл буруу"}},
			"errors": obj{"type": "object", "description": "талбар → мессежүүд. Child талбарын түлхүүр: `lines.0.qty`",
				"additionalProperties": obj{"type": "array", "items": obj{"type": "string"}}}}},
		"Can": obj{"type": "object", "properties": obj{
			"read": obj{"type": "boolean"}, "create": obj{"type": "boolean"},
			"update": obj{"type": "boolean"}, "delete": obj{"type": "boolean"}}},
		"Option": obj{"type": "object", "required": []any{"value", "label"}, "properties": obj{
			"value":        obj{"type": []any{"string", "integer"}},
			"label":        obj{"type": "string"},
			"parent_value": obj{"type": []any{"string", "integer", "null"}}}},
		"OptionList": obj{"type": "object", "required": []any{"data"}, "properties": obj{
			"data": obj{"type": "array", "items": ref("Option")}}},
		"DeleteResult": obj{"type": "object", "required": []any{"deleted"}, "properties": obj{
			"deleted": obj{"type": []any{"string", "integer"}}}},
		"BulkResult": obj{"type": "object", "required": []any{"affected"}, "properties": obj{
			"affected": obj{"type": "integer", "format": "int64"}}},
		"ListQuery": obj{"type": "object", "properties": obj{
			"page":    obj{"type": "integer", "minimum": 1},
			"limit":   obj{"type": "integer", "minimum": 0},
			"search":  obj{"type": "string"},
			"sort":    obj{"type": "string"},
			"dir":     obj{"type": "string", "enum": []any{"asc", "desc"}},
			"filters": obj{"description": "listquery шүүлт: объект эсвэл түүнийг JSON мөр болгосон", "oneOf": []any{obj{"type": "object", "additionalProperties": true}, obj{"type": "string"}}}}},
	}
}

func errorResponses() obj {
	e := func(desc string, schema string) obj {
		return obj{"description": desc, "content": jsonContent(ref(schema))}
	}
	return obj{
		"BadRequest":    e("JSON/id/шүүлтийн формат буруу, body хэт том", "Error"),
		"Unauthorized":  e("org/JWT алга", "Error"),
		"Forbidden":     e("Can(action) == false", "Error"),
		"NotFound":      e("Мөр байхгүй ЭСВЭЛ өөр org-ийнх (ялгахгүй)", "Error"),
		"Conflict":      e("Version зөрсөн, unique/FK constraint, hook conflict", "Error"),
		"Unprocessable": e("Талбарын шалгалт, ref-ийн org, unique, children", "ValidationError"),
		"Internal":      e("Дотоод алдаа (жинхэнэ алдаа body-д гарахгүй)", "Error"),
	}
}

type oaGen struct {
	m      crud.Meta
	pascal string
	info   Info
}

// ---- schema ---------------------------------------------------------------------

func nullable(s obj) obj {
	if t, ok := s["type"].(string); ok {
		s["type"] = []any{t, "null"}
		if e, ok := s["enum"].([]any); ok {
			s["enum"] = append(e, nil)
		}
		return s
	}
	if one, ok := s["oneOf"].([]any); ok {
		s["oneOf"] = append(one, obj{"type": "null"})
		return s
	}
	return s // JSON (any) — аль хэдийн null зөвшөөрнө
}

func decScale(f crud.FieldMeta) int { return decimalScale(f) }

// fieldSchema — талбарын schema. write=true: хүсэлт (decimal → тоо | мөр), false: хариу (decimal → тоо).
func fieldSchema(f crud.FieldMeta, write bool) obj {
	var s obj
	switch f.Kind {
	case validate.String, validate.Text:
		s = obj{"type": "string"}
		if f.MinLen > 0 {
			s["minLength"] = f.MinLen
		}
		if f.MaxLen > 0 {
			s["maxLength"] = f.MaxLen
		}
	case validate.Phone:
		s = obj{"type": "string", "description": "Монгол 8 оронтой дугаар (+976 хасагдана) эсвэл E.164 `+…`"}
	case validate.Email:
		s = obj{"type": "string", "format": "email"}
		if f.MaxLen > 0 {
			s["maxLength"] = f.MaxLen
		}
	case validate.Password:
		s = obj{"type": "string", "format": "password", "writeOnly": true, "minLength": maxInt(f.MinLen, validate.MinPasswordLen)}
	case validate.Int:
		s = obj{"type": "integer", "format": "int64"}
		if f.Min != nil {
			s["minimum"] = *f.Min
		}
		if f.Max != nil {
			s["maximum"] = *f.Max
		}
	case validate.Money, validate.Number:
		sc := decScale(f)
		num := obj{"type": "number"}
		if f.Min != nil {
			num["minimum"] = *f.Min
		}
		if f.Max != nil {
			num["maximum"] = *f.Max
		}
		if write {
			s = obj{"oneOf": []any{num, obj{"type": "string", "pattern": `^-?\d+(\.\d+)?$`}},
				"description": fmt.Sprintf("Decimal: JSON тоо эсвэл тоон мөр; бутархай ≤ %d орон. Том дүнг (>15 цифр) тоон мөрөөр илгээ", sc)}
		} else {
			num["description"] = fmt.Sprintf("Decimal, яг цифртэй JSON тоо; бутархай ≤ %d орон", sc)
			s = num
		}
	case validate.Bool:
		s = obj{"type": "boolean"}
	case validate.Date:
		s = obj{"type": "string", "format": "date"}
	case validate.DateTime:
		s = obj{"type": "string", "format": "date-time"}
	case validate.Time:
		s = obj{"type": "string", "pattern": `^\d{2}:\d{2}(:\d{2})?$`}
	case validate.UUID:
		s = obj{"type": "string", "format": "uuid"}
	case validate.Enum:
		s = obj{"type": "string"}
		if len(f.Enum) > 0 {
			vals := make([]any, len(f.Enum))
			for i, v := range f.Enum {
				vals[i] = v
			}
			s["enum"] = vals
		}
	case validate.JSON:
		s = obj{"description": "Дурын JSON"}
	}
	if f.Pattern != "" {
		if _, ok := s["type"]; ok {
			s["pattern"] = f.Pattern
		}
	}
	if f.Label != "" {
		if _, has := s["description"]; has {
			s["description"] = f.Label + ". " + s["description"].(string)
		} else {
			s["description"] = f.Label
		}
	}
	if f.Default != nil && write {
		s["default"] = f.Default
	}
	if f.Nullable || (!write && !f.Required) {
		s = nullable(s)
	}
	return s
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func idSchema(k crud.IDKind) obj {
	if k == crud.IDInt {
		return obj{"type": "integer", "format": "int64"}
	}
	return obj{"type": "string", "format": "uuid"}
}

func (g *oaGen) writeObject(fields []crud.FieldMeta, write bool, update bool) (obj, []any) {
	props := obj{}
	var required []any
	for _, f := range fields {
		if !write && f.Kind == validate.Password {
			continue
		}
		if update && f.CreateOnly {
			continue
		}
		props[f.Name] = fieldSchema(f, write)
		if write && !update && f.Required && f.Default == nil {
			required = append(required, f.Name)
		}
	}
	return props, required
}

func (g *oaGen) schemas() obj {
	m, p := g.m, g.pascal
	out := obj{}

	for _, c := range m.Children {
		cp, _ := pascalOf("x." + c.Name) // plan-д шалгагдсан биш: доор алдааг барина
		reqProps, req := g.writeObject(c.Fields, true, false)
		idKey := childIDColumn(c)
		reqProps[idKey] = obj{"type": "string", "format": "uuid", "description": "Байгаа мөрийг засахад; байхгүй бол шинэ мөр"}
		in := obj{"type": "object", "properties": reqProps, "description": c.Label}
		if len(req) > 0 {
			in["required"] = req
		}
		out[p+cp+"Input"] = in
		rowProps, _ := g.writeObject(c.Fields, false, false)
		rowProps[idKey] = obj{"type": "string", "format": "uuid"}
		out[p+cp] = obj{"type": "object", "description": c.Label, "required": []any{idKey}, "properties": rowProps, "additionalProperties": true}
	}

	// create
	props, req := g.writeObject(m.Fields, true, false)
	for _, c := range m.Children {
		cp, _ := pascalOf("x." + c.Name)
		props[c.Name] = obj{"type": "array", "items": ref(p + cp + "Input"), "description": c.Label + ": бүрэн жагсаалт",
			"maxItems": c.MaxRows}
	}
	create := obj{"type": "object", "description": m.Label + " — үүсгэх хүсэлт", "properties": props}
	if len(req) > 0 {
		create["required"] = req
	}
	if m.StrictUnknown {
		create["additionalProperties"] = false
	}
	out[p+"Create"] = create

	// update
	uprops, _ := g.writeObject(m.Fields, true, true)
	for _, c := range m.Children {
		cp, _ := pascalOf("x." + c.Name)
		uprops[c.Name] = obj{"type": "array", "items": ref(p + cp + "Input"), "description": c.Label + ": бүрэн жагсаалт (түлхүүр алга/null бол өөрчлөгдөхгүй)",
			"maxItems": c.MaxRows}
	}
	update := obj{"type": "object", "description": m.Label + " — засах хүсэлт (хэсэгчилсэн)", "properties": uprops}
	if m.Version != "" {
		uprops[m.Version] = obj{"type": "integer", "minimum": 0, "description": "Optimistic lock: одоогийн version (зөрвөл 409)"}
		update["required"] = []any{m.Version}
	}
	if m.StrictUnknown {
		update["additionalProperties"] = false
	}
	out[p+"Update"] = update

	// row
	rprops, _ := g.writeObject(m.Fields, false, false)
	rprops[m.ID.Column] = idSchema(m.ID.Kind)
	rprops["org_id"] = obj{"type": "string", "format": "uuid"}
	if m.Version != "" {
		rprops[m.Version] = obj{"type": "integer"}
	}
	for _, s := range []string{"created_at", "updated_at", "deleted_at"} {
		rprops[s] = obj{"type": []any{"string", "null"}, "format": "date-time"}
	}
	for _, s := range []string{"created_by", "updated_by"} {
		rprops[s] = obj{"type": []any{"string", "null"}}
	}
	for _, c := range m.Children {
		cp, _ := pascalOf("x." + c.Name)
		rprops[c.Name] = obj{"type": "array", "items": ref(p + cp)}
	}
	out[p] = obj{"type": "object", "description": m.Label + " — дэлгэрэнгүй мөр", "required": []any{m.ID.Column}, "properties": rprops, "additionalProperties": true}

	// list
	out[p+"List"] = obj{"type": "object", "required": []any{"data", "total"}, "properties": obj{
		"data":       obj{"type": "array", "items": obj{"type": "object", "additionalProperties": true, "description": "Жагсаалтын мөр (resource-ийн ListQuery-ийн SELECT-ээр тодорхойлогдоно)"}},
		"total":      obj{"type": "integer", "format": "int64"},
		"page":       obj{"type": "integer"},
		"limit":      obj{"type": "integer"},
		"totals":     obj{"type": "object", "additionalProperties": true},
		"filterable": obj{"type": "object", "additionalProperties": obj{"type": "string"}},
		"can":        ref("Can"),
	}}
	return out
}

// ---- paths ----------------------------------------------------------------------

func errs(codes ...string) obj {
	names := map[string]string{"400": "BadRequest", "401": "Unauthorized", "403": "Forbidden", "404": "NotFound",
		"409": "Conflict", "422": "Unprocessable", "500": "Internal"}
	out := obj{}
	for _, c := range codes {
		out[c] = respRef(names[c])
	}
	return out
}

func merge(a obj, bs ...obj) obj {
	for _, b := range bs {
		for k, v := range b {
			a[k] = v
		}
	}
	return a
}

func okResp(desc string, schema any) obj {
	return obj{"description": desc, "content": jsonContent(schema)}
}

func (g *oaGen) addPaths(paths obj) {
	m, p := g.m, g.pascal
	base := m.Endpoint
	tag := []any{m.Label}
	if m.Label == "" {
		tag = []any{m.Name}
	}
	opID := func(verb string) string { return verb + p }

	listParams := []any{
		obj{"name": "page", "in": "query", "schema": obj{"type": "integer", "minimum": 1}},
		obj{"name": "limit", "in": "query", "schema": obj{"type": "integer", "minimum": 0}},
	}
	if m.Search {
		listParams = append(listParams, obj{"name": "search", "in": "query", "schema": obj{"type": "string"}})
	}
	sortSchema := obj{"type": "string"}
	if len(m.Sort) > 0 {
		vals := make([]any, len(m.Sort))
		for i, s := range m.Sort {
			vals[i] = s
		}
		sortSchema["enum"] = vals
	}
	listParams = append(listParams,
		obj{"name": "sort", "in": "query", "schema": sortSchema},
		obj{"name": "dir", "in": "query", "schema": obj{"type": "string", "enum": []any{"asc", "desc"}}})
	filters := obj{"name": "filters", "in": "query", "schema": obj{"type": "string"},
		"description": "listquery шүүлт (JSON мөр)" + filterableDesc(m)}
	listParams = append(listParams, filters)

	bodyListQuery := ref("ListQuery")

	paths[base] = obj{
		"get": obj{"operationId": opID("list"), "summary": m.Label + " — жагсаалт", "tags": tag, "parameters": listParams,
			"responses": merge(obj{"200": okResp("Жагсаалт", ref(p+"List"))}, errs("400", "401", "403", "500"))},
		"post": obj{"operationId": opID("create"), "summary": m.Label + " — үүсгэх", "tags": tag,
			"requestBody": obj{"required": true, "content": jsonContent(ref(p + "Create"))},
			"responses":   merge(obj{"201": okResp("Үүссэн мөр", ref(p))}, errs("400", "401", "403", "409", "422", "500"))},
	}
	paths[base+"/query"] = obj{
		"post": obj{"operationId": opID("query"), "summary": m.Label + " — жагсаалт (JSON body)", "tags": tag,
			"requestBody": obj{"content": jsonContent(bodyListQuery)},
			"responses":   merge(obj{"200": okResp("Жагсаалт", ref(p+"List"))}, errs("400", "401", "403", "500"))},
	}
	if m.Export {
		paths[base+"/export.xlsx"] = obj{
			"get": obj{"operationId": opID("export"), "summary": m.Label + " — Excel export", "tags": tag, "parameters": listParams,
				"responses": merge(obj{"200": obj{"description": "XLSX файл", "content": obj{
					"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet": obj{"schema": obj{"type": "string", "format": "binary"}}}}},
					errs("400", "401", "403", "500"))},
		}
	}
	if m.Bulk {
		actions := []any{"update"}
		if m.BulkDelete {
			actions = []any{"delete", "update"}
		}
		setProps := obj{}
		for _, name := range m.BulkFields {
			var sch obj = obj{}
			for _, f := range m.Fields {
				if f.Name == name {
					sch = fieldSchema(f, true)
				}
			}
			setProps[name] = sch
		}
		paths[base+"/bulk"] = obj{
			"post": obj{"operationId": opID("bulk"), "summary": m.Label + " — бөөнөөр засах/устгах", "tags": tag,
				"description": "delete → delete эрх, update → update эрх шаардана. Бусад org-ийн id нөлөөлөхгүй (affected-д орохгүй).",
				"requestBody": obj{"required": true, "content": jsonContent(obj{
					"type": "object", "required": []any{"action", "ids"}, "properties": obj{
						"action": obj{"type": "string", "enum": actions},
						"ids":    obj{"type": "array", "minItems": 1, "items": idSchema(m.ID.Kind)},
						"set":    obj{"type": "object", "properties": setProps, "additionalProperties": false},
					}})},
				"responses": merge(obj{"200": okResp("Нөлөөлсөн мөрийн тоо", ref("BulkResult"))}, errs("400", "401", "403", "500"))},
		}
	}
	if m.ExposeMeta || g.info.MetaRoutes {
		paths[base+"/meta"] = obj{
			"get": obj{"operationId": opID("meta"), "summary": m.Label + " — meta", "tags": tag,
				"description": "ExposeMeta: true үед л mount хийгдэнэ.",
				"responses": merge(obj{"200": okResp("crud.Meta + can", obj{"type": "object", "additionalProperties": true})},
					errs("401", "403", "500"))},
		}
	}
	if keys, parent := refKeys(m); len(keys) > 0 {
		vals := make([]any, len(keys))
		for i, k := range keys {
			vals[i] = k
		}
		params := []any{
			obj{"name": "field", "in": "path", "required": true, "schema": obj{"type": "string", "enum": vals},
				"description": "Ref талбарын нэр; child-д `<child>.<field>`"},
			obj{"name": "search", "in": "query", "schema": obj{"type": "string", "maxLength": 100}},
			obj{"name": "ids", "in": "query", "schema": obj{"type": "string"}, "description": "a,b,c (дээд 200); search/parent-ийг үл тооно"},
			obj{"name": "limit", "in": "query", "schema": obj{"type": "integer", "minimum": 1, "maximum": 200}},
		}
		if len(parent) > 0 {
			params = append(params, obj{"name": "parent", "in": "query", "schema": obj{"type": "string"},
				"description": "Parent-тэй талбарууд: " + strings.Join(parent, ", ")})
		}
		paths[base+"/options/{field}"] = obj{
			"get": obj{"operationId": opID("options"), "summary": m.Label + " — ref сонголтууд", "tags": tag, "parameters": params,
				"responses": merge(obj{"200": okResp("Сонголтууд (өөрийн org-ийнх)", ref("OptionList"))}, errs("400", "401", "403", "404", "500"))},
		}
	}

	idParam := []any{obj{"name": "id", "in": "path", "required": true, "schema": idSchema(m.ID.Kind)}}
	paths[base+"/{id}"] = obj{
		"get": obj{"operationId": opID("get"), "summary": m.Label + " — дэлгэрэнгүй", "tags": tag, "parameters": idParam,
			"responses": merge(obj{"200": okResp("Мөр + children", ref(p))}, errs("400", "401", "403", "404", "500"))},
		"put": obj{"operationId": opID("update"), "summary": m.Label + " — засах", "tags": tag, "parameters": idParam,
			"requestBody": obj{"required": true, "content": jsonContent(ref(p + "Update"))},
			"responses":   merge(obj{"200": okResp("Засагдсан мөр", ref(p))}, errs("400", "401", "403", "404", "409", "422", "500"))},
		"delete": obj{"operationId": opID("delete"), "summary": m.Label + " — устгах", "tags": tag, "parameters": idParam,
			"responses": merge(obj{"200": okResp("Устсан id", ref("DeleteResult"))}, errs("400", "401", "403", "404", "409", "500"))},
	}
}

func filterableDesc(m crud.Meta) string {
	if len(m.Filterable) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m.Filterable))
	for k := range m.Filterable {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + ": " + string(m.Filterable[k])
	}
	return ". Шүүх боломжтой: " + strings.Join(parts, ", ")
}

// refKeys — options route-ийн :field түлхүүрүүд (эрэмблэгдсэн) ба parent-тэй түлхүүрүүд.
func refKeys(m crud.Meta) (keys, parent []string) {
	for _, f := range m.Fields {
		if f.Ref != nil {
			keys = append(keys, f.Name)
			if f.Ref.Parent {
				parent = append(parent, f.Name)
			}
		}
	}
	for _, c := range m.Children {
		for _, f := range c.Fields {
			if f.Ref != nil {
				keys = append(keys, c.Name+"."+f.Name)
				if f.Ref.Parent {
					parent = append(parent, c.Name+"."+f.Name)
				}
			}
		}
	}
	sort.Strings(keys)
	sort.Strings(parent)
	return keys, parent
}
