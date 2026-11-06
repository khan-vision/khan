package crud

import (
	"fmt"

	"github.com/khan-vision/khan/tenant"
	"github.com/khan-vision/khan/validate"
	"gorm.io/gorm"
)

// childState — child бүрийн транзакц доторх шалгалтын үр дүн (write үе рүү дамжина).
type childState struct {
	in       *childInput
	existing map[string]bool // update: эцэг+org-д байгаа child id-ууд
}

// checkChildren — update үед өгөгдсөн id-ууд энэ эцэг+org-ынх эсэх, ref-үүд олдох эсэхийг шалгана.
// parentID nil (create) үед existing хоосон.
func (e *engine) checkChildren(tx *gorm.DB, o owner, parentID any, inputs map[string]*childInput, errs validate.Errors) (map[string]*childState, error) {
	states := map[string]*childState{}
	for ci := range e.children {
		cp := &e.children[ci]
		in := inputs[cp.Name]
		if in == nil {
			continue
		}
		st := &childState{in: in, existing: map[string]bool{}}
		if parentID != nil {
			ids, err := e.childIDs(tx, cp, o, parentID)
			if err != nil {
				return nil, err
			}
			st.existing = ids
			for _, r := range in.rows {
				if r.id != "" && !st.existing[r.id] {
					errs.Add(fmt.Sprintf("%s.%d.%s", cp.Name, r.idx, cp.idCol), cp.label+" мөр олдсонгүй")
				}
			}
		}
		if err := e.checkChildRefs(tx, o, cp, in, errs); err != nil {
			return nil, err
		}
		states[cp.Name] = st
	}
	return states, nil
}

// childIDs — эцэг + org-д хамаарах child id-ууд.
func (e *engine) childIDs(tx *gorm.DB, cp *childPlan, o owner, parentID any) (map[string]bool, error) {
	q := e.ownWhere(tx.Table(cp.Table).Select(cp.idCol), "", o).Where(cp.FK+" = ?", parentID)
	if cp.SoftDelete {
		q = q.Where("deleted_at IS NULL")
	}
	rows, err := q.Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var v any
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out[fmt.Sprint(normalizeScan(v))] = true
	}
	return out, rows.Err()
}

func normalizeScan(v any) any {
	if b, ok := v.([]byte); ok {
		return string(b)
	}
	return v
}

// checkChildRefs — child талбар бүрийн Ref утгуудыг НЭГ batch-аар шалгана.
func (e *engine) checkChildRefs(tx *gorm.DB, o owner, cp *childPlan, in *childInput, errs validate.Errors) error {
	for i := range cp.fields {
		f := &cp.fields[i]
		if f.ref == nil {
			continue
		}
		var vals []any
		for _, r := range in.rows {
			if v, ok := r.vals[f.col]; ok && v != nil {
				vals = append(vals, v)
			}
		}
		if o.system && f.ref.TenantScoped() && len(vals) > 0 { // системийн мөр org-ийн лавлах руу заахгүй
			for _, r := range in.rows {
				if v, ok := r.vals[f.col]; ok && v != nil {
					errs.Add(fmt.Sprintf("%s.%d.%s", cp.Name, r.idx, f.Name), f.label+": системийн мөрөнд байгууллагын лавлахыг холбох боломжгүй")
				}
			}
			continue
		}
		missing, err := f.ref.Exists(tx, o.org, vals)
		if err != nil {
			return err
		}
		if len(missing) == 0 {
			continue
		}
		bad := map[string]bool{}
		for _, m := range missing {
			bad[fmt.Sprint(m)] = true
		}
		for _, r := range in.rows {
			if v, ok := r.vals[f.col]; ok && v != nil && bad[fmt.Sprint(v)] {
				errs.Add(fmt.Sprintf("%s.%d.%s", cp.Name, r.idx, f.Name), f.label+" олдсонгүй")
			}
		}
	}
	return nil
}

// writeChildren — "бүрэн жагсаалт" семантик: id-тай мөр update, id-гүй insert, жагсаалтад
// ороогүй байгаа мөр delete. org_id, FK, id-г сервер тавина.
func (e *engine) writeChildren(tx *gorm.DB, o owner, parentID any, states map[string]*childState) error {
	for ci := range e.children {
		cp := &e.children[ci]
		st := states[cp.Name]
		if st == nil {
			continue
		}
		keep := map[string]bool{}
		for _, r := range st.in.rows {
			if r.id != "" {
				keep[r.id] = true
			}
		}
		for id := range st.existing {
			if !keep[id] {
				if err := e.deleteChild(tx, cp, o, parentID, id); err != nil {
					return err
				}
			}
		}
		for _, r := range st.in.rows {
			var err error
			if r.id != "" {
				err = e.updateChild(tx, cp, o, parentID, r)
			} else {
				err = e.insertChild(tx, cp, o, parentID, r)
			}
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (e *engine) insertChild(tx *gorm.DB, cp *childPlan, o owner, parentID any, r childRow) error {
	row := make(map[string]any, len(r.vals)+5)
	for k, v := range r.vals {
		row[k] = dbValue(v)
	}
	if e.scope != ScopeGlobal {
		row["org_id"] = o.orgValue() // child нь эцгийнхээ эзэмшигчийг дагана (системийн эцэг → NULL)
	}
	row[cp.FK] = parentID
	row[cp.idCol] = cp.gen()
	if e.timestamps {
		row["created_at"] = nowExpr()
		row["updated_at"] = nowExpr()
	}
	q := tx.Table(cp.Table)
	if o.system {
		q = tenant.SystemRow(q)
	}
	return q.Create(row).Error
}

func (e *engine) updateChild(tx *gorm.DB, cp *childPlan, o owner, parentID any, r childRow) error {
	upd := make(map[string]any, len(r.vals)+1)
	for k, v := range r.vals {
		upd[k] = dbValue(v)
	}
	if e.timestamps {
		upd["updated_at"] = nowExpr()
	}
	if len(upd) == 0 {
		return nil
	}
	q := e.ownWhere(tx.Table(cp.Table), "", o).Where(cp.FK+" = ? AND "+cp.idCol+" = ?", parentID, r.id)
	if cp.SoftDelete {
		q = q.Where("deleted_at IS NULL")
	}
	return q.Updates(upd).Error
}

func (e *engine) deleteChild(tx *gorm.DB, cp *childPlan, o owner, parentID any, id string) error {
	q := e.ownWhere(tx.Table(cp.Table), "", o).Where(cp.FK+" = ? AND "+cp.idCol+" = ?", parentID, id)
	if cp.SoftDelete {
		upd := map[string]any{"deleted_at": nowExpr()}
		if e.timestamps {
			upd["updated_at"] = nowExpr()
		}
		return q.Where("deleted_at IS NULL").Updates(upd).Error
	}
	return q.Delete(map[string]any{}).Error
}

// deleteAllChildren — эцгийг hard delete хийхэд бүх child мөрийг (soft эсэхээс үл хамааран) устгана.
func (e *engine) deleteAllChildren(tx *gorm.DB, o owner, parentID any) error {
	for ci := range e.children {
		cp := &e.children[ci]
		err := e.ownWhere(tx.Table(cp.Table), "", o).Where(cp.FK+" = ?", parentID).Delete(map[string]any{}).Error
		if err != nil {
			return err
		}
	}
	return nil
}
