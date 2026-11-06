package crud

import (
	"fmt"
	"strings"

	"github.com/gofiber/fiber/v3"
	"github.com/khan-vision/khan/audit"
	"github.com/khan-vision/khan/validate"
	"gorm.io/gorm"
)

// AuditSpec — resource-ийн өөрчлөлтийн түүх (khan/audit). docs/AUDIT.md.
//
// Enabled үед create / update / delete / bulk бүр `khan.audit_log`-д БИЗНЕС ТРАНЗАКЦДАА
// бичигдэнэ (хамт commit/rollback): audit бичилт амжилтгүй бол бүх бичилт буцаж 500 болно, hook
// амжилтгүй бол audit мөр үлдэхгүй. Password төрлийн талбар хэзээ ч (hash ч) хадгалагдахгүй —
// өөрчлөгдсөн нь `["***","***"]` гэж л тэмдэглэгдэнэ. Хүснэгтийг audit.SchemaSQL-ээр урьдчилж үүсгэнэ.
//
// Mount нь `GET /:id/history` (нэг мөрийн түүх)-ийг `ActionRead` эрхээр, org-оор тусгаарлан,
// мөрийн харагдацыг (DetailQuery) хүндэтгэн нэмнэ; `GET /history` нь ListHistory үед л.
type AuditSpec struct {
	Enabled bool
	// ListHistory — `GET /history` (resource-ийн БҮХ мөрийн түүх)-ийг нээнэ. Энэ нь мөр бүрийн
	// харагдацыг шалгахгүй (org + ActionRead л) тул ListQuery/DetailQuery (мөрийн нууцлал) тохируулсан
	// resource-д ХОРИОТОЙ — Mount/Check алдаа буцаана (fail-closed). Анхдагч false: route байхгүй.
	ListHistory bool
	// DeletedHistory — устгагдсан (soft/hard) мөрийн `/:id/history`-г зөвшөөрнө. Анхдагч false:
	// мөр харагдахгүй (олдохгүй/устсан/DetailQuery-ээр нуугдсан) бол GET /:id-тэй ижил 404.
	// true үед мөр БАЙХГҮЙ (hard) эсвэл soft-delete хийгдсэн бол түүхийг өгнө; амьд боловч
	// DetailQuery-ээр харагдахгүй мөр ҮРГЭЛЖ 404.
	DeletedHistory bool
	// Snapshot — өөрчлөлт бүрт мөрийн бүтэн төлвийг (create/update: шинэ, delete: сүүлийн) хадгална.
	// Restore хийхэд хялбар боловч хадгалалт ихэснэ.
	Snapshot bool
	// Exclude — түүхэнд орохгүй талбарууд: Field.Name, child-ийн нэр (`lines`), эсвэл child-ийн
	// талбар (`lines.price`). Утга нь ЗАЙЛШГҮЙ нууц (token, PIN) биш бол хэрэггүй; өөрчлөлтийг
	// мөрдөх шаардлагагүй (ж: тооцоолсон/давтан шинэчлэгдэх) талбарт хэрэглэнэ.
	Exclude []string
}

type auditPlan struct {
	snapshot bool
	exclude  map[string]bool // top-level талбар, child нэр, "child.field"
	opts     audit.DiffOptions
	handlers audit.Handlers

	deletedHistory bool
}

// compileAudit — AuditSpec-ийг шалгаж auditPlan болгоно (nil/унтраалттай бол nil).
func (e *engine) compileAudit(spec *AuditSpec, runtime bool) (*auditPlan, error) {
	if spec == nil || !spec.Enabled {
		return nil, nil
	}
	p := &auditPlan{snapshot: spec.Snapshot, exclude: map[string]bool{}}
	for _, x := range spec.Exclude {
		name, field, nested := strings.Cut(x, ".")
		switch {
		case !nested && e.byName[name] != nil:
		case !nested && e.hasChild(name):
		case nested && e.childField(name, field):
		default:
			return nil, fmt.Errorf("Audit.Exclude: %q талбар/child олдсонгүй", x)
		}
		p.exclude[x] = true
	}
	p.opts.Partial = false
	for i := range e.fields {
		f := &e.fields[i]
		switch {
		case e.pwCols[f.col]:
			p.opts.Redact = append(p.opts.Redact, f.Name)
		case p.exclude[f.Name]:
			p.opts.Exclude = append(p.opts.Exclude, f.Name)
		}
	}
	for i := range e.children {
		if p.exclude[e.children[i].Name] {
			p.opts.Exclude = append(p.opts.Exclude, e.children[i].Name)
		}
	}
	if spec.ListHistory && (e.list != nil || e.detail != nil) {
		return nil, fmt.Errorf("Audit.ListHistory: ListQuery/DetailQuery-тэй resource-д (мөрийн харагдац) /history нээж болохгүй — зөвхөн /:id/history")
	}
	p.deletedHistory = spec.DeletedHistory
	if runtime {
		h, err := audit.Handler(audit.HandlerConfig{
			Resource: e.name,
			DB:       func() *gorm.DB { return e.db() },
			Org:      e.org,
			Can:      func(c fiber.Ctx) bool { return e.allowed(c, ActionRead) },
			RLS:      e.rls,
			Visible:  e.auditVisible,
			NotFound: func(c fiber.Ctx) error { return e.fail(c, ActionRead, errNotFound) },
			Forbidden: func(c fiber.Ctx) error {
				return e.forbidden(c, ActionRead)
			},
			ParseID: func(raw string) (string, bool) {
				_, s, ok := e.parseID(raw)
				return s, ok
			},
		})
		if err != nil {
			return nil, err
		}
		if !spec.ListHistory {
			// Route Mount-д үргэлж бүртгэгдэнэ; унтраалттай үед дараагийн route (/:id → 400) руу өнгөрнө.
			h.List = func(c fiber.Ctx) error { return c.Next() }
		}
		p.handlers = h
	}
	return p, nil
}

// auditVisible — /:id/history-д: мөр resource-ийн DetailQuery-ээр (org + soft-delete + custom
// нөхцөл) харагдаж байвал true. DeletedHistory үед мөр байхгүй/soft-delete хийгдсэн бол ч true;
// амьд боловч харагдахгүй мөр үргэлж false (мөрийн нууцлал).
func (e *engine) auditVisible(_ fiber.Ctx, tx *gorm.DB, orgID, recordID string) (bool, error) {
	idVal, _, ok := e.parseID(recordID)
	if !ok {
		return false, nil
	}
	var rows []map[string]any
	err := e.detailQuery(tx, orgID).Select("1 AS one").
		Where(e.alias+"."+e.idCol+" = "+e.idParam(), idVal).Limit(1).Find(&rows).Error
	if err != nil || len(rows) > 0 {
		return len(rows) > 0, err
	}
	if !e.audit.deletedHistory {
		return false, nil
	}
	// Амьд мөр (харагдахгүй байсан) байвал нуух; байхгүй эсвэл soft-delete хийгдсэн бол зөвшөөрнө.
	q := e.readWhere(tx.Table(e.table).Select("1 AS one"), "", orgID).
		Where(quote(e.idCol)+" = "+e.idParam(), idVal)
	if e.softDelete {
		q = q.Where("deleted_at IS NULL")
	}
	var live []map[string]any
	if err := q.Limit(1).Find(&live).Error; err != nil {
		return false, err
	}
	return len(live) == 0, nil
}

func (e *engine) hasChild(name string) bool {
	for i := range e.children {
		if e.children[i].Name == name {
			return true
		}
	}
	return false
}

func (e *engine) childField(child, field string) bool {
	for i := range e.children {
		if e.children[i].Name == child {
			return e.children[i].byName[field] != nil
		}
	}
	return false
}

// auditStates — id-уудын одоогийн төлөв (талбарын Name → утга; withChildren үед child жагсаалт).
// Түлхүүр = мөрийн id-ийн текст. Мөр байхгүй бол map-д орохгүй. Money/Number яг цифртэй
// (json.Number), Date "YYYY-MM-DD", JSON задалсан. Password-ийн ТҮҮХИЙ утга орно (өөрчлөгдсөн
// эсэхийг мэдэхэд) — audit.Diff/Snapshot түүнийг «***» болгоно, хэзээ ч DB-д очихгүй.
func (e *engine) auditStates(tx *gorm.DB, orgID string, ids []any, withChildren bool) (map[string]map[string]any, error) {
	out := make(map[string]map[string]any, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	q := e.readWhere(tx.Table(e.table).Select(append([]string{"*"}, decimalSelect(e.fields)...)), "", orgID).
		Where(quote(e.idCol)+" IN ?", ids)
	if e.softDelete {
		q = q.Where("deleted_at IS NULL")
	}
	var rows []map[string]any
	if err := q.Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		key := fmt.Sprint(normalizeScan(row[e.idCol]))
		st := e.auditShape(row)
		if withChildren {
			for ci := range e.children {
				cp := &e.children[ci]
				if e.audit.exclude[cp.Name] {
					continue
				}
				list, err := e.loadChildren(tx, cp, orgID, row[e.idCol])
				if err != nil {
					return nil, err
				}
				st[cp.Name] = e.auditChildRows(cp, list)
			}
		}
		out[key] = st
	}
	return out, nil
}

// auditState — нэг мөрийн төлөв (олдохгүй бол nil).
func (e *engine) auditState(tx *gorm.DB, orgID string, idVal any, withChildren bool) (map[string]any, error) {
	m, err := e.auditStates(tx, orgID, []any{idVal}, withChildren)
	if err != nil {
		return nil, err
	}
	return m[fmt.Sprint(idVal)], nil
}

// auditShape — DB мөрийг (Name түлхүүртэй) аудитын төлөв болгоно. Зөвхөн зарласан талбарууд
// (id, org_id, timestamps, version орохгүй).
func (e *engine) auditShape(row map[string]any) map[string]any {
	pw := map[string]any{}
	for col := range e.pwCols {
		if v, ok := row[col]; ok {
			pw[col] = normalizeScan(v)
		}
	}
	shaped := shapeRow(row, e.fields, e.pwCols)
	out := make(map[string]any, len(e.fields))
	for i := range e.fields {
		f := &e.fields[i]
		if e.audit.exclude[f.Name] {
			continue
		}
		switch {
		case e.pwCols[f.col]:
			out[f.Name] = pw[f.col]
		case f.Kind == validate.Money || f.Kind == validate.Number:
			out[f.Name] = decimalNumber(shaped[f.Name])
		default:
			out[f.Name] = shaped[f.Name]
		}
	}
	return out
}

// auditChildRows — child мөрүүдээс id + зарласан талбарууд (timestamps, FK, org_id-гүй).
func (e *engine) auditChildRows(cp *childPlan, list []map[string]any) []any {
	out := make([]any, 0, len(list))
	for _, r := range list {
		m := make(map[string]any, len(cp.fields)+1)
		m[cp.idCol] = r[cp.idCol]
		for i := range cp.fields {
			f := &cp.fields[i]
			if e.audit.exclude[cp.Name+"."+f.Name] {
				continue
			}
			m[f.Name] = r[f.Name]
		}
		out = append(out, m)
	}
	return out
}

// auditWrite — өөрчлөлтийг хэлбэрт оруулж тухайн транзакцад бичнэ. before/after: nil = байхгүй
// (create: before nil; delete: after nil). Өөрчлөлтгүй update-ийг бичихгүй.
func (e *engine) auditWrite(tx *gorm.DB, c fiber.Ctx, orgID, id string, a audit.Action, before, after map[string]any) error {
	en, ok := e.auditEntry(c, orgID, id, a, before, after)
	if !ok {
		return nil
	}
	return audit.Record(tx, en)
}

func (e *engine) auditEntry(c fiber.Ctx, orgID, id string, a audit.Action, before, after map[string]any) (audit.Entry, bool) {
	changes := audit.Diff(before, after, e.audit.opts)
	if len(changes) == 0 && (a == audit.ActionUpdate || a == audit.ActionBulkUpdate) {
		return audit.Entry{}, false
	}
	en := audit.Entry{
		OrgID: orgID, Resource: e.name, RecordID: id, Action: a,
		Meta: audit.MetaFrom(c), Changes: changes,
	}
	if e.audit.snapshot {
		src := after
		if a == audit.ActionDelete || a == audit.ActionBulkDelete {
			src = before
		}
		en.Snapshot = audit.Snapshot(src, e.audit.opts)
	}
	return en, true
}

// auditBulk — bulk дараах мөр бүрийн entry-г нэг INSERT-ээр. after nil = delete.
func (e *engine) auditBulk(tx *gorm.DB, c fiber.Ctx, orgID string, a audit.Action, ids []any, before, after map[string]map[string]any) error {
	entries := make([]audit.Entry, 0, len(ids))
	for _, idv := range ids {
		id := fmt.Sprint(idv)
		var aft map[string]any
		if after != nil {
			aft = after[id]
		}
		if en, ok := e.auditEntry(c, orgID, id, a, before[id], aft); ok {
			entries = append(entries, en)
		}
	}
	return audit.RecordMany(tx, entries)
}
