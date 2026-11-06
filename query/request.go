// request.go — жагсаалтын хүсэлтийн параметрүүд (page/limit/search/sort/dir/filters)-ийг
// эх сурвалжаас (GET query эсвэл POST JSON body) салгасан нэг бүтэц.
//
// GET  /list?page=1&limit=25&search=..&sort=..&dir=asc&filters={...}   → FromQuery
// POST /list/query  {"page":1,"limit":25,"search":"","sort":"","dir":"asc","filters":{...}} → FromBody
//
// POST хэлбэр нь урт `filters` JSON-ыг URL-ын уртын хязгаараас чөлөөлнө.
package query

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/gofiber/fiber/v2"
)

// maxBodyBytes — POST /query body-ийн дээд хэмжээ.
const maxBodyBytes = 16 << 10 // 16 KB

// Request — жагсаалтын нэг хүсэлт. Limit/Page 0 бол Spec-ийн анхдагч утга авна.
type Request struct {
	Page, Limit       int
	Search, Sort, Dir string
	// Filters — түүхий JSON объект (filter.go-ийн wire формат).
	Filters string
}

// FromQuery — Run-ий өмнө нь уншдаг байсан query параметрүүдийг яг адилхан уншина.
func FromQuery(c *fiber.Ctx) Request {
	return Request{
		Page:    c.QueryInt("page", 1),
		Limit:   c.QueryInt("limit", 0),
		Search:  c.Query("search"),
		Sort:    c.Query("sort"),
		Dir:     c.Query("dir"),
		Filters: c.Query("filters"),
	}
}

// bodyReq — POST body-ийн wire хэлбэр. `filters` нь объект ЭСВЭЛ JSON мөр байж болно.
type bodyReq struct {
	Page    int             `json:"page"`
	Limit   int             `json:"limit"`
	Search  string          `json:"search"`
	Sort    string          `json:"sort"`
	Dir     string          `json:"dir"`
	Filters json.RawMessage `json:"filters"`
}

// FromBody — POST JSON body-г задална. Буруу JSON / хэт том body → *FilterError (400).
func FromBody(c *fiber.Ctx) (Request, error) {
	return parseBody(c.Body())
}

func parseBody(body []byte) (Request, error) {
	if len(body) > maxBodyBytes {
		return Request{}, ferr("хүсэлтийн хэмжээ хэт том")
	}
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return Request{Page: 1}, nil
	}
	var b bodyReq
	if err := json.Unmarshal(body, &b); err != nil {
		return Request{}, ferr("хүсэлтийн JSON буруу")
	}
	req := Request{Page: b.Page, Limit: b.Limit, Search: b.Search, Sort: b.Sort, Dir: b.Dir}
	if req.Page == 0 {
		req.Page = 1
	}
	raw := bytes.TrimSpace(b.Filters)
	switch {
	case len(raw) == 0 || string(raw) == "null":
	case raw[0] == '"':
		// filters нь JSON мөр хэлбэрээр ирсэн (GET-тэй ижил кодчилол).
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return Request{}, ferr("шүүлтийн JSON буруу")
		}
		req.Filters = s
	case raw[0] == '{':
		req.Filters = string(raw)
	default:
		return Request{}, ferr("шүүлтийн JSON буруу")
	}
	return req, nil
}

// RequestFrom — POST + body байвал FromBody, бусад үед FromQuery.
func RequestFrom(c *fiber.Ctx) (Request, error) {
	if c.Method() == fiber.MethodPost && len(bytes.TrimSpace(c.Body())) > 0 {
		return FromBody(c)
	}
	return FromQuery(c), nil
}

// prepared — Request + Spec-ээс гарсан, DB-д бэлэн параметрүүд.
type prepared struct {
	page, limit int
	search      string
	conds       []condSQL
	order       string
}

// prepare — хязгаар/анхдагч утга, шүүлт, whitelist эрэмбийг НЭГ газар тооцно
// (RunRequest ба ExportXLSX хоёулаа үүнийг ашиглана).
func (s Spec) prepare(req Request) (prepared, error) {
	defLimit := s.DefaultLimit
	if defLimit <= 0 {
		defLimit = 25
	}
	maxLimit := s.MaxLimit
	if maxLimit <= 0 {
		maxLimit = 500
	}
	p := prepared{page: req.Page, limit: req.Limit}
	if p.page < 1 {
		p.page = 1
	}
	if p.limit < 1 {
		p.limit = defLimit
	}
	if p.limit > maxLimit {
		p.limit = maxLimit
	}
	p.search = strings.TrimSpace(req.Search)

	conds, err := s.parseFilters(req.Filters)
	if err != nil {
		return prepared{}, err
	}
	p.conds = conds
	p.order = s.orderFor(req.Sort, req.Dir)
	return p, nil
}

// orderFor — whitelist-д байхгүй sort түлхүүр → DefaultOrder.
func (s Spec) orderFor(sortKey, dir string) string {
	if sortKey == "" || s.Sort == nil {
		return s.DefaultOrder
	}
	col, ok := s.Sort[sortKey]
	if !ok {
		return s.DefaultOrder
	}
	dir = strings.ToLower(dir)
	if dir != "asc" {
		dir = "desc"
	}
	return col + " " + strings.ToUpper(dir)
}
