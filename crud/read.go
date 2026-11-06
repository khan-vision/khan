package crud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/khan-vision/khan/tenant"
	"github.com/khan-vision/khan/validate"
	"gorm.io/gorm"
)

// scopedQuery — custom (эсвэл анхдагч) query дээр org + soft-delete нөхцөлийг ҮРГЭЛЖ
// нэмнэ (custom query мартсан ч fail-closed). custom query нь Alias-аар хүснэгтээ нэрлэх ёстой.
func (e *engine) scopedQuery(db *gorm.DB, orgID string, custom func(*gorm.DB, string) *gorm.DB) *gorm.DB {
	var q *gorm.DB
	if custom != nil {
		q = custom(db, orgID)
	} else {
		q = db.Table(e.table + " " + e.alias)
	}
	q = e.readWhere(q, e.alias+".", orgID)
	if e.softDelete {
		q = q.Where(e.alias + ".deleted_at IS NULL")
	}
	return q
}

func (e *engine) listQuery(db *gorm.DB, orgID string) *gorm.DB {
	return e.scopedQuery(db, orgID, e.list)
}

func (e *engine) detailQuery(db *gorm.DB, orgID string) *gorm.DB {
	if e.detail != nil {
		return e.scopedQuery(db, orgID, e.detail)
	}
	return e.scopedQuery(db, orgID, e.list)
}

// read — унших үйлдэл. RLS үед tenant.Tx дотор (app.org_id тавигдана).
func (e *engine) read(ctx context.Context, orgID string, fn func(db *gorm.DB) error) error {
	gdb := e.db()
	if gdb == nil {
		return errors.New("khan/crud: DB nil")
	}
	if e.rls {
		return tenant.Tx(ctx, gdb, orgID, fn)
	}
	return fn(gdb.WithContext(ctx))
}

// loadDetail — мөр + children массивууд. Олдохгүй бол errNotFound.
func (e *engine) loadDetail(db *gorm.DB, orgID string, idVal any) (map[string]any, error) {
	var rows []map[string]any
	err := e.detailQuery(db, orgID).Where(e.alias+"."+e.idCol+" = "+e.idParam(), idVal).Limit(1).Find(&rows).Error
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, errNotFound
	}
	row := shapeRow(rows[0], e.fields, e.pwCols)
	if err := e.overlayDecimals(db, orgID, idVal, row); err != nil {
		return nil, err
	}
	for ci := range e.children {
		cp := &e.children[ci]
		list, err := e.loadChildren(db, cp, orgID, idVal)
		if err != nil {
			return nil, err
		}
		row[cp.Name] = list
	}
	return row, nil
}

func (e *engine) loadChildren(db *gorm.DB, cp *childPlan, orgID string, parentID any) ([]map[string]any, error) {
	q := e.readWhere(db.Table(cp.Table), "", orgID).Where(cp.FK+" = ?", parentID)
	if cp.SoftDelete {
		q = q.Where("deleted_at IS NULL")
	}
	if dec := decimalSelect(cp.fields); len(dec) > 0 {
		q = q.Select(append([]string{"*"}, dec...)) // давхар нэртэй багана: сүүлийнх (text) нь map-д үлдэнэ
	}
	var rows []map[string]any
	if err := q.Order(cp.order + " ASC, " + cp.idCol + " ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		row := shapeRow(r, cp.fields, cp.pwCols)
		for i := range cp.fields {
			if f := &cp.fields[i]; f.Kind == validate.Money || f.Kind == validate.Number {
				row[f.Name] = decimalNumber(row[f.Name])
			}
		}
		out = append(out, row)
	}
	return out, nil
}

// decimalSelect — Money/Number баганыг яг тексттэй хэлбэрээр сонгох илэрхийллүүд.
// Драйвер numeric-г float64 болгодог тул 9999999999999999.99 гэх мэт утга алдагдана.
func decimalSelect(fields []fieldPlan) []string {
	var sel []string
	for i := range fields {
		if f := &fields[i]; f.Kind == validate.Money || f.Kind == validate.Number {
			sel = append(sel, "CAST("+quote(f.col)+" AS text) AS "+quote(f.col))
		}
	}
	return sel
}

var plainNumberRe = regexp.MustCompile(`^-?[0-9]+(\.[0-9]+)?$`)

// decimalNumber — PG-ийн numeric::text-ийг JSON тоо (json.Number, яг тэр цифрүүд) болгоно.
// NaN гэх мэт JSON тоо биш бол мөр хэвээр.
func decimalNumber(v any) any {
	s, ok := v.(string)
	if !ok {
		return v
	}
	if plainNumberRe.MatchString(s) {
		return json.Number(s)
	}
	return s
}

// overlayDecimals — parent мөрийн Money/Number талбарыг яг тексттэй хэлбэрээр дахин уншиж
// (custom DetailQuery-д ч ажиллана) row-д давхарлана.
func (e *engine) overlayDecimals(db *gorm.DB, orgID string, idVal any, row map[string]any) error {
	if len(e.decimals) == 0 {
		return nil
	}
	sel := make([]string, 0, len(e.decimals))
	for _, f := range e.decimals {
		sel = append(sel, "CAST("+quote(f.col)+" AS text) AS "+quote(f.Name))
	}
	var rows []map[string]any
	err := e.readWhere(db.Table(e.table).Select(strings.Join(sel, ", ")), "", orgID).
		Where(quote(e.idCol)+" = "+e.idParam(), idVal).Limit(1).Find(&rows).Error
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	for _, f := range e.decimals {
		row[f.Name] = decimalNumber(rows[0][f.Name])
	}
	return nil
}

// overlayListDecimals — жагсаалтын мөрүүдийн Money/Number баганыг яг decimal-аар дахин
// уншиж давхарлана (драйвер numeric-г float64 болгодог). Зөвхөн dest нь
// *[]map[string]any ба мөрөнд id байгаа үед; typed (struct) мөрийг өөрчлөхгүй.
// Custom ListQuery-д ч ажиллана: мөрийн түлхүүр нь Field.Name эсвэл багана.
func (e *engine) overlayListDecimals(db *gorm.DB, orgID string, dest any) error {
	if len(e.decimals) == 0 {
		return nil
	}
	p, ok := dest.(*[]map[string]any)
	if !ok || p == nil || len(*p) == 0 {
		return nil
	}
	ids := make([]any, 0, len(*p))
	for _, row := range *p {
		if id, ok := row[e.idCol]; ok && id != nil {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	sel := []string{quote(e.idCol) + " AS " + quote("__khan_id")}
	for _, f := range e.decimals {
		sel = append(sel, "CAST("+quote(f.col)+" AS text) AS "+quote(f.Name))
	}
	var dec []map[string]any
	err := e.readWhere(db.Table(e.table).Select(strings.Join(sel, ", ")), "", orgID).
		Where(quote(e.idCol)+" IN ?", ids).Find(&dec).Error
	if err != nil {
		return err
	}
	byID := make(map[string]map[string]any, len(dec))
	for _, d := range dec {
		byID[fmt.Sprint(d["__khan_id"])] = d
	}
	for _, row := range *p {
		d, ok := byID[fmt.Sprint(row[e.idCol])]
		if !ok {
			continue
		}
		for _, f := range e.decimals {
			key := f.Name
			if _, has := row[key]; !has {
				if _, hasCol := row[f.col]; !hasCol {
					continue // жагсаалт энэ баганыг сонгоогүй
				}
				key = f.col
			}
			if row[key] != nil {
				row[key] = decimalNumber(d[f.Name])
			}
		}
	}
	return nil
}
