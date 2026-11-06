// bulk.go — сонгосон мөрүүдийг бөөнөөр устгах / засах (KvGrid v2 bulk action).
//
// Wire формат (POST body):
//
//	{ "action": "delete", "ids": ["<uuid>", ...] }
//	{ "action": "update", "ids": [...], "set": { "quantity_fit_number": 12 } }
//
// ⚠️ АЮУЛГҮЙ БАЙДЛЫН ХИЛ: SET-д ЗӨВХӨН BulkSpec.Fields-д сервер талд бүртгэсэн
// багана орно; утга бүр төрлөөрөө шалгагдаж `?` bind болно. WHERE нь ҮРГЭЛЖ
// `<OrgCol> = <JWT org>` AND `<IDCol> IN (?)` — id-ууд хэзээ ч SQL-д шууд орохгүй.
package query

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"gorm.io/gorm"
)

const (
	maxBulkBodyBytes = 64 << 10 // 64 KB
	defaultBulkIDs   = 200
	ceilingBulkIDs   = 1000
	maxBulkTextLen   = 2000
)

// identRe — хүснэгт / баганын нэр (schema.table зөвшөөрнө).
var identRe = regexp.MustCompile(`^[a-z_][a-z0-9_]*(\.[a-z_][a-z0-9_]*)?$`)

// BulkField — засаж болох нэг багана.
type BulkField struct {
	Col  string
	Kind FilterKind // text|number|bool|date|uuid|enum (account = uuid)
	// Enum — KindEnum үед зөвшөөрөх утгууд.
	Enum []string
	// Nullable — null утга оноож болох эсэх.
	Nullable bool
	// KindNumber: Integer → бүхэл тоо л; Min/Max (nil = хязгааргүй) — create/update-ийн шалгалттай ижил
	// хязгаарыг bulk-д ч тавина (үгүй бол -5 / 1e12 шиг утга DB-д орж эсвэл 500 болно).
	Integer  bool
	Min, Max *float64
}

// Bound — BulkField.Min/Max-д зориулсан туслах.
func Bound(v float64) *float64 { return &v }

// BulkSpec — bulk endpoint-ийн тохиргоо.
type BulkSpec struct {
	Table  string // ж: "organization.conference_room"
	IDCol  string // анхдагч "id"
	OrgCol string // анхдагч "org_id"
	// Fields — API түлхүүр → багана. Зөвхөн эдгээрийг update хийнэ.
	Fields      map[string]BulkField
	AllowDelete bool
	// SoftDelete — delete нь deleted_at=now(); бүх үйлдэл deleted_at IS NULL мөрөнд л.
	SoftDelete bool
	// MaxIDs — нэг удаагийн id-ийн дээд тоо (0→200, дээд тал нь 1000).
	MaxIDs int
	// IntIDs — id бүхэл тоо; false бол UUID шаардана.
	IntIDs bool
	// NoOrg — хүснэгтэд org_id багана БАЙХГҮЙ (global/platform хүснэгт): `<OrgCol> = ?` нөхцөл
	// нэмэхгүй. ЗӨВХӨН crud.ScopeGlobal тавина (эрхийг crud-ийн Can/GlobalWrites хаалга шалгана);
	// tenant хүснэгтэд тавьсан бол бүх org-ийн мөр нөлөөлнө — тиймээс ил тод нэртэй.
	NoOrg bool
}

// BulkRequest — шалгагдсан bulk хүсэлт.
type BulkRequest struct {
	Action string // "delete" | "update"
	IDs    []string
	// Set — API түлхүүр → шалгаж хөрвүүлсэн утга.
	Set map[string]interface{}
}

func (s BulkSpec) idCol() string {
	if s.IDCol == "" {
		return "id"
	}
	return s.IDCol
}

func (s BulkSpec) orgCol() string {
	if s.OrgCol == "" {
		return "org_id"
	}
	return s.OrgCol
}

func (s BulkSpec) maxIDs() int {
	n := s.MaxIDs
	if n <= 0 {
		n = defaultBulkIDs
	}
	if n > ceilingBulkIDs {
		n = ceilingBulkIDs
	}
	return n
}

// Validate — spec-ийн програмчлалын алдаа → panic (Mount үед дуудагдана).
// id / org_id / deleted_at-ийг Fields-д бүртгэвэл ч хэзээ ч SET хийлгэхгүй.
func (s BulkSpec) Validate() {
	if !identRe.MatchString(s.Table) {
		panic(fmt.Sprintf("khan/query: BulkSpec.Table буруу: %q", s.Table))
	}
	for _, c := range []string{s.idCol(), s.orgCol()} {
		if !identRe.MatchString(c) {
			panic(fmt.Sprintf("khan/query: BulkSpec багана буруу: %q", c))
		}
	}
	forbidden := map[string]bool{
		"id": true, "org_id": true, "deleted_at": true,
		s.idCol(): true, s.orgCol(): true,
	}
	for key, f := range s.Fields {
		if !identRe.MatchString(f.Col) || strings.Contains(f.Col, ".") {
			panic(fmt.Sprintf("khan/query: BulkField %q-ийн Col буруу: %q", key, f.Col))
		}
		if forbidden[f.Col] || forbidden[key] {
			panic(fmt.Sprintf("khan/query: %q баганыг bulk-аар засаж БОЛОХГҮЙ", f.Col))
		}
		switch f.Kind {
		case KindText, KindNumber, KindBool, KindDate, KindUUID, KindAccount:
		case KindEnum:
			if len(f.Enum) == 0 {
				panic(fmt.Sprintf("khan/query: BulkField %q enum утгагүй", key))
			}
		default:
			panic(fmt.Sprintf("khan/query: BulkField %q төрөл дэмжигдэхгүй: %q", key, f.Kind))
		}
	}
}

// bulkWire — body-ийн түүхий хэлбэр.
type bulkWire struct {
	Action string                     `json:"action"`
	IDs    []json.RawMessage          `json:"ids"`
	Set    map[string]json.RawMessage `json:"set"`
}

// ParseBulk — body-г задалж бүрэн шалгана. Хэрэглэгчийн алдаа бүр *FilterError (400).
func ParseBulk(body []byte, spec BulkSpec) (BulkRequest, error) {
	spec.Validate()
	if len(body) > maxBulkBodyBytes {
		return BulkRequest{}, ferr("хүсэлтийн хэмжээ хэт том")
	}
	var w bulkWire
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(&w); err != nil {
		return BulkRequest{}, ferr("хүсэлтийн JSON буруу")
	}
	req := BulkRequest{Action: strings.TrimSpace(w.Action)}
	switch req.Action {
	case "delete":
		if !spec.AllowDelete {
			return BulkRequest{}, ferr("бөөнөөр устгах зөвшөөрөгдөөгүй")
		}
		if len(w.Set) > 0 {
			return BulkRequest{}, ferr("устгах үйлдэлд set байх ёсгүй")
		}
	case "update":
		if len(w.Set) == 0 {
			return BulkRequest{}, ferr("засах талбар (set) хоосон")
		}
	default:
		return BulkRequest{}, ferr("үл мэдэгдэх үйлдэл: %q", req.Action)
	}

	ids, err := parseBulkIDs(w.IDs, spec)
	if err != nil {
		return BulkRequest{}, err
	}
	req.IDs = ids

	if req.Action == "update" {
		req.Set = make(map[string]interface{}, len(w.Set))
		for key, raw := range w.Set {
			f, ok := spec.Fields[key]
			if !ok {
				return BulkRequest{}, ferr("зөвшөөрөгдөөгүй талбар: %s", key)
			}
			v, err := bulkValue(key, f, raw)
			if err != nil {
				return BulkRequest{}, err
			}
			req.Set[key] = v
		}
	}
	return req, nil
}

// parseBulkIDs — хоосон биш, давхардалгүй (давхардлыг хасна), тоо хязгаартай, хэлбэр зөв.
func parseBulkIDs(raws []json.RawMessage, spec BulkSpec) ([]string, error) {
	if len(raws) == 0 {
		return nil, ferr("сонгосон мөр (ids) хоосон")
	}
	max := spec.maxIDs()
	if len(raws) > max {
		return nil, ferr("нэг удаад дээд тал нь %d мөр", max)
	}
	seen := make(map[string]bool, len(raws))
	out := make([]string, 0, len(raws))
	for _, raw := range raws {
		var id string
		if spec.IntIDs {
			var n json.Number
			d := json.NewDecoder(bytes.NewReader(raw))
			d.UseNumber()
			if err := d.Decode(&n); err != nil {
				var s string
				if json.Unmarshal(raw, &s) != nil {
					return nil, ferr("id буруу")
				}
				n = json.Number(strings.TrimSpace(s))
			}
			v, err := strconv.ParseInt(n.String(), 10, 64)
			if err != nil || v <= 0 {
				return nil, ferr("id буруу: %s", n.String())
			}
			id = strconv.FormatInt(v, 10)
		} else {
			var s string
			if err := json.Unmarshal(raw, &s); err != nil {
				return nil, ferr("id буруу")
			}
			s = strings.ToLower(strings.TrimSpace(s))
			if !isUUID(s) {
				return nil, ferr("uuid id буруу: %s", s)
			}
			id = s
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out, nil
}

// bulkValue — нэг талбарын утгыг төрлөөр нь шалгаж хөрвүүлнэ.
func bulkValue(key string, f BulkField, raw json.RawMessage) (interface{}, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		if !f.Nullable {
			return nil, ferr("«%s» хоосон байж болохгүй", key)
		}
		return nil, nil
	}
	var s string
	isStr := json.Unmarshal(raw, &s) == nil

	switch f.Kind {
	case KindText:
		if !isStr {
			return nil, ferr("«%s» текст байх ёстой", key)
		}
		if utf8.RuneCountInString(s) > maxBulkTextLen {
			return nil, ferr("«%s» хэт урт (дээд тал нь %d)", key, maxBulkTextLen)
		}
		return s, nil

	case KindNumber:
		txt := string(raw)
		if isStr {
			txt = strings.TrimSpace(s)
		}
		fv, err := strconv.ParseFloat(txt, 64)
		if err != nil || math.IsInf(fv, 0) || math.IsNaN(fv) {
			return nil, ferr("«%s» тоо байх ёстой", key)
		}
		if f.Integer && fv != math.Trunc(fv) {
			return nil, ferr("«%s» бүхэл тоо байх ёстой", key)
		}
		if (f.Min != nil && fv < *f.Min) || (f.Max != nil && fv > *f.Max) {
			return nil, ferr("«%s» зөвшөөрөгдөх хязгаараас гадуур", key)
		}
		if fv == math.Trunc(fv) && math.Abs(fv) < 1<<53 {
			return int64(fv), nil
		}
		return fv, nil

	case KindBool:
		var b bool
		if json.Unmarshal(raw, &b) == nil {
			return b, nil
		}
		if isStr && (s == "true" || s == "false") {
			return s == "true", nil
		}
		return nil, ferr("«%s» true/false байх ёстой", key)

	case KindDate:
		if !isStr {
			return nil, ferr("«%s» огноо (YYYY-MM-DD) байх ёстой", key)
		}
		s = strings.TrimSpace(s)
		if _, err := time.Parse("2006-01-02", s); err != nil {
			return nil, ferr("«%s» огноо (YYYY-MM-DD) байх ёстой", key)
		}
		return s, nil

	case KindUUID, KindAccount:
		s = strings.ToLower(strings.TrimSpace(s))
		if !isStr || !isUUID(s) {
			return nil, ferr("«%s» uuid байх ёстой", key)
		}
		return s, nil

	case KindEnum:
		if !isStr {
			return nil, ferr("«%s» сонголтын утга буруу", key)
		}
		for _, e := range f.Enum {
			if e == s {
				return s, nil
			}
		}
		return nil, ferr("«%s» сонголтын утга буруу: %s", key, s)
	}
	return nil, ferr("«%s» төрөл дэмжигдэхгүй", key)
}

// BulkQuery — WHERE <OrgCol> = org AND <IDCol> IN (ids) [AND deleted_at IS NULL].
func BulkQuery(db *gorm.DB, orgID string, req BulkRequest, spec BulkSpec) *gorm.DB {
	spec.Validate()
	var ids interface{} = req.IDs
	if spec.IntIDs {
		n := make([]int64, 0, len(req.IDs))
		for _, s := range req.IDs {
			v, _ := strconv.ParseInt(s, 10, 64)
			n = append(n, v)
		}
		ids = n
	}
	q := db.Table(spec.Table)
	if !spec.NoOrg {
		q = q.Where(spec.orgCol()+" = ?", orgID)
	}
	q = q.Where(spec.idCol()+" IN ?", ids)
	if spec.SoftDelete {
		q = q.Where("deleted_at IS NULL")
	}
	return q
}

// ErrBulkNoOrg — org_id хоосон (JWT-гүй) үед bulk ажиллахгүй.
var ErrBulkNoOrg = errors.New("байгууллагын мэдээлэл олдсонгүй")

// ExecBulk — транзакц дотор delete / soft-delete / update хийж нөлөөлсөн мөрийн тоог буцаана.
func ExecBulk(db *gorm.DB, orgID string, req BulkRequest, spec BulkSpec) (int64, error) {
	if strings.TrimSpace(orgID) == "" {
		return 0, ErrBulkNoOrg
	}
	if len(req.IDs) == 0 {
		return 0, ferr("сонгосон мөр (ids) хоосон")
	}
	run := func(tx *gorm.DB) (int64, error) {
		q := BulkQuery(tx, orgID, req, spec)
		var res *gorm.DB
		switch req.Action {
		case "delete":
			if !spec.AllowDelete {
				return 0, ferr("бөөнөөр устгах зөвшөөрөгдөөгүй")
			}
			if spec.SoftDelete {
				res = q.Update("deleted_at", gorm.Expr("now()"))
			} else {
				res = q.Delete(map[string]interface{}{})
			}
		case "update":
			set := make(map[string]interface{}, len(req.Set))
			for key, v := range req.Set {
				f, ok := spec.Fields[key]
				if !ok {
					return 0, ferr("зөвшөөрөгдөөгүй талбар: %s", key)
				}
				set[f.Col] = v
			}
			if len(set) == 0 {
				return 0, ferr("засах талбар (set) хоосон")
			}
			res = q.Updates(set)
		default:
			return 0, ferr("үл мэдэгдэх үйлдэл: %q", req.Action)
		}
		return res.RowsAffected, res.Error
	}
	// DryRun (тест) үед холболт огт нээхгүй — default транзакцыг ч алгасаж statement-ийг л бүтээнэ.
	if db.DryRun {
		return run(db.Session(&gorm.Session{SkipDefaultTransaction: true}))
	}
	var n int64
	err := db.Transaction(func(tx *gorm.DB) error {
		var err error
		n, err = run(tx)
		return err
	})
	return n, err
}
