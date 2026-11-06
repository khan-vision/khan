package crud

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/gofiber/fiber/v3"
	khandb "github.com/khan-vision/khan/db"
	"github.com/khan-vision/khan/events"
	"github.com/khan-vision/khan/tenant"
	"github.com/khan-vision/khan/validate"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func nowExpr() clause.Expr { return gorm.Expr("now()") }

// tx — write транзакц. RLS үед tenant.Tx (app.org_id тавигдана).
func (e *engine) tx(c fiber.Ctx, orgID string, fn func(tx *gorm.DB) error) error {
	gdb := e.db()
	if gdb == nil {
		return errors.New("khan/crud: DB nil")
	}
	if e.rls {
		return tenant.Tx(c.Context(), gdb, orgID, fn)
	}
	return khandb.Tx(c.Context(), gdb, fn)
}

func (e *engine) allowed(c fiber.Ctx, a Action) bool { return e.can != nil && e.can(c, a) }

func (e *engine) ctx(c fiber.Ctx, tx *gorm.DB, orgID, uid string, a Action, id string, old map[string]any) *Ctx {
	return &Ctx{Fiber: c, Tx: tx, OrgID: orgID, UserID: uid, Action: a, ID: id, Old: old, Resource: e.name}
}

// runValidate — cross-field Validate: validate.Errors → errs-д нэгтгэнэ, бусад алдаа буцна.
func (e *engine) runValidate(c fiber.Ctx, orgID, uid string, a Action, id string, old map[string]any, vals Values, errs validate.Errors) error {
	if e.validate == nil {
		return nil
	}
	err := e.validate(e.ctx(c, nil, orgID, uid, a, id, old), vals)
	var ve validate.Errors
	switch {
	case err == nil:
		return nil
	case errors.As(err, &ve):
		errs.Merge(ve)
		return nil
	}
	return err
}

// checkRefs — parent талбаруудын Ref утгууд тухайн org-д байгаа эсэх.
func (e *engine) checkRefs(tx *gorm.DB, orgID string, vals Values, errs validate.Errors) error {
	for i := range e.fields {
		f := &e.fields[i]
		if f.ref == nil {
			continue
		}
		v, ok := vals[f.col]
		if !ok || v == nil {
			continue
		}
		missing, err := f.ref.Exists(tx, orgID, []any{v})
		if err != nil {
			return err
		}
		if len(missing) > 0 {
			errs.Add(f.Name, f.label+" олдсонгүй")
		}
	}
	return nil
}

// checkUnique — Unique талбар тухайн org дотор давтагдаж байгаа эсэх (self-ийг оруулахгүй).
// DB-ийн unique index нь уралдааныг барина (23505 → 409).
func (e *engine) checkUnique(tx *gorm.DB, orgID string, selfID any, vals Values, errs validate.Errors) error {
	for i := range e.fields {
		f := &e.fields[i]
		if !f.Unique {
			continue
		}
		v, ok := vals[f.col]
		if !ok || v == nil {
			continue
		}
		q := tx.Table(e.table).Select("1").Where("org_id = ? AND "+quote(f.col)+" = ?", orgID, dbValue(v))
		if selfID != nil {
			q = q.Where(quote(e.idCol)+" <> ?", selfID)
		}
		if e.softDelete {
			q = q.Where("deleted_at IS NULL")
		}
		var hit []int
		if err := q.Limit(1).Scan(&hit).Error; err != nil {
			return err
		}
		if len(hit) > 0 {
			errs.Add(f.Name, f.label+" давхардсан байна")
		}
	}
	return nil
}

// lockRow — SELECT ... FOR UPDATE (org + soft-delete нөхцөлтэй). Олдохгүй бол errNotFound.
func (e *engine) lockRow(tx *gorm.DB, orgID string, idVal any) (map[string]any, error) {
	q := tx.Table(e.table).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("org_id = ? AND "+quote(e.idCol)+" = "+e.idParam(), orgID, idVal)
	if e.softDelete {
		q = q.Where("deleted_at IS NULL")
	}
	var rows []map[string]any
	if err := q.Limit(1).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, errNotFound
	}
	return rows[0], nil
}

func (e *engine) publish(tx *gorm.DB, orgID, suffix, id string, vals Values) error {
	if !e.events {
		return nil
	}
	payload := map[string]any{"id": id, "org_id": orgID}
	if vals != nil {
		out := make(map[string]any, len(vals))
		for k, v := range vals {
			if !e.pwCols[k] {
				out[k] = v
			}
		}
		payload["values"] = out
	}
	return events.Publish(tx, events.Event{
		OrgID: orgID, Topic: e.name + "." + suffix, AggregateType: e.name, AggregateID: id, Payload: payload,
	})
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// ─── POST / ───

func (e *engine) create(c fiber.Ctx) error {
	orgID, ok := e.org(c)
	if !ok {
		return nil
	}
	if !e.allowed(c, ActionCreate) {
		return jsonErr(c, fiber.StatusForbidden, msgForbidden)
	}
	body, err := e.readBody(c)
	if err != nil {
		return e.fail(c, ActionCreate, err)
	}
	errs := validate.NewErrors()
	vals := parseFields(e.fields, body, true, errs, "")
	inputs := e.parseChildren(body, true, errs)
	if e.strictUnknown {
		checkUnknown(body, e.known, errs, "")
	}
	if len(errs) > 0 {
		return validate.Write(c, errs)
	}
	uid := userID(c)

	var out map[string]any
	err = e.tx(c, orgID, func(tx *gorm.DB) error {
		id := ""
		if e.idKind == IDUUID {
			id = e.gen()
		}
		if err := e.runValidate(c, orgID, uid, ActionCreate, id, nil, vals, errs); err != nil {
			return err
		}
		if err := e.checkRefs(tx, orgID, vals, errs); err != nil {
			return err
		}
		if err := e.checkUnique(tx, orgID, nil, vals, errs); err != nil {
			return err
		}
		states, err := e.checkChildren(tx, orgID, nil, inputs, errs)
		if err != nil {
			return err
		}
		if len(errs) > 0 {
			return errs
		}
		x := e.ctx(c, tx, orgID, uid, ActionCreate, id, nil)
		if e.hooks.BeforeCreate != nil {
			if err := e.hooks.BeforeCreate(x, vals); err != nil {
				return err
			}
		}

		row := make(map[string]any, len(vals)+8)
		for k, v := range vals {
			if !e.protected[k] { // hook-оор id/org_id/timestamps-ийг дарж чадахгүй
				row[k] = dbValue(v)
			}
		}
		row["org_id"] = orgID // hook/body дарж чадахгүй
		if e.idKind == IDUUID {
			row[e.idCol] = id
		}
		if e.timestamps {
			row["created_at"], row["updated_at"] = nowExpr(), nowExpr()
		}
		if e.trackUser {
			row["created_by"], row["updated_by"] = nullable(uid), nullable(uid)
		}
		if e.versionCol != "" {
			row[e.versionCol] = int64(1)
		}
		q := tx.Table(e.table)
		if e.idKind == IDInt {
			q = q.Clauses(clause.Returning{Columns: []clause.Column{{Name: e.idCol}}})
		}
		if err := q.Create(row).Error; err != nil {
			return err
		}
		var idVal any = id
		if e.idKind == IDInt {
			n, ok := asInt64(row[e.idCol])
			if !ok {
				return fmt.Errorf("khan/crud: INSERT RETURNING %s: %v", e.idCol, row[e.idCol])
			}
			idVal, id, x.ID = n, fmt.Sprint(n), fmt.Sprint(n)
		}
		if err := e.writeChildren(tx, orgID, idVal, states); err != nil {
			return err
		}
		if e.hooks.AfterCreate != nil {
			if err := e.hooks.AfterCreate(x, vals); err != nil {
				return err
			}
		}
		if err := e.publish(tx, orgID, "created", id, vals); err != nil {
			return err
		}
		out, err = e.loadDetail(tx, orgID, idVal)
		return err
	})
	if err != nil {
		return e.fail(c, ActionCreate, err)
	}
	return c.Status(http.StatusCreated).JSON(out)
}

// ─── PUT /:id ───

func (e *engine) update(c fiber.Ctx) error {
	orgID, ok := e.org(c)
	if !ok {
		return nil
	}
	if !e.allowed(c, ActionUpdate) {
		return jsonErr(c, fiber.StatusForbidden, msgForbidden)
	}
	idVal, id, ok := e.parseID(c.Params("id"))
	if !ok {
		return jsonErr(c, fiber.StatusBadRequest, msgBadID)
	}
	body, err := e.readBody(c)
	if err != nil {
		return e.fail(c, ActionUpdate, err)
	}
	errs := validate.NewErrors()
	vals := parseFields(e.fields, body, false, errs, "")
	inputs := e.parseChildren(body, false, errs)
	var version int64
	if e.versionCol != "" {
		version, _ = e.parseVersion(body, errs)
	}
	if e.strictUnknown {
		checkUnknown(body, e.known, errs, "")
	}
	if len(errs) > 0 {
		return validate.Write(c, errs)
	}
	uid := userID(c)

	var out map[string]any
	err = e.tx(c, orgID, func(tx *gorm.DB) error {
		old, err := e.lockRow(tx, orgID, idVal)
		if err != nil {
			return err
		}
		if e.versionCol != "" {
			cur, _ := asInt64(old[e.versionCol])
			if cur != version {
				return Conflict(msgVersion)
			}
		}
		if err := e.runValidate(c, orgID, uid, ActionUpdate, id, old, vals, errs); err != nil {
			return err
		}
		if err := e.checkRefs(tx, orgID, vals, errs); err != nil {
			return err
		}
		if err := e.checkUnique(tx, orgID, idVal, vals, errs); err != nil {
			return err
		}
		states, err := e.checkChildren(tx, orgID, idVal, inputs, errs)
		if err != nil {
			return err
		}
		if len(errs) > 0 {
			return errs
		}
		x := e.ctx(c, tx, orgID, uid, ActionUpdate, id, old)
		if e.hooks.BeforeUpdate != nil {
			if err := e.hooks.BeforeUpdate(x, vals); err != nil {
				return err
			}
		}

		upd := make(map[string]any, len(vals)+3)
		for k, v := range vals {
			if !e.protected[k] { // hook-оор org_id/deleted_at/version-ийг дарж чадахгүй
				upd[k] = dbValue(v)
			}
		}
		if e.timestamps {
			upd["updated_at"] = nowExpr()
		}
		if e.trackUser {
			upd["updated_by"] = nullable(uid)
		}
		if e.versionCol != "" {
			upd[e.versionCol] = gorm.Expr(quote(e.versionCol) + " + 1")
		}
		if len(upd) > 0 {
			q := tx.Table(e.table).Where("org_id = ? AND "+quote(e.idCol)+" = "+e.idParam(), orgID, idVal)
			if e.softDelete {
				q = q.Where("deleted_at IS NULL")
			}
			res := q.Updates(upd)
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected == 0 {
				return errNotFound
			}
		}
		if err := e.writeChildren(tx, orgID, idVal, states); err != nil {
			return err
		}
		if e.hooks.AfterUpdate != nil {
			if err := e.hooks.AfterUpdate(x, vals); err != nil {
				return err
			}
		}
		if err := e.publish(tx, orgID, "updated", id, vals); err != nil {
			return err
		}
		out, err = e.loadDetail(tx, orgID, idVal)
		return err
	})
	if err != nil {
		return e.fail(c, ActionUpdate, err)
	}
	return c.JSON(out)
}

// ─── DELETE /:id ───

func (e *engine) remove(c fiber.Ctx) error {
	orgID, ok := e.org(c)
	if !ok {
		return nil
	}
	if !e.allowed(c, ActionDelete) {
		return jsonErr(c, fiber.StatusForbidden, msgForbidden)
	}
	idVal, id, ok := e.parseID(c.Params("id"))
	if !ok {
		return jsonErr(c, fiber.StatusBadRequest, msgBadID)
	}
	uid := userID(c)
	err := e.tx(c, orgID, func(tx *gorm.DB) error {
		return e.deleteOne(c, tx, orgID, uid, idVal, id)
	})
	if err != nil {
		return e.fail(c, ActionDelete, err)
	}
	return c.JSON(fiber.Map{"deleted": id})
}

// deleteOne — нэг мөрийг устгах бүрэн дүрэм (түгжих, BeforeDelete, children cascade, soft/hard,
// AfterDelete, event). DELETE /:id болон hook/children-тэй bulk delete хоёулаа үүнийг дуудна.
func (e *engine) deleteOne(c fiber.Ctx, tx *gorm.DB, orgID, uid string, idVal any, id string) error {
	old, err := e.lockRow(tx, orgID, idVal)
	if err != nil {
		return err
	}
	x := e.ctx(c, tx, orgID, uid, ActionDelete, id, old)
	if e.hooks.BeforeDelete != nil {
		if err := e.hooks.BeforeDelete(x); err != nil {
			return err
		}
	}
	q := tx.Table(e.table).Where("org_id = ? AND "+quote(e.idCol)+" = "+e.idParam(), orgID, idVal)
	var res *gorm.DB
	if e.softDelete {
		upd := map[string]any{"deleted_at": nowExpr()}
		if e.timestamps {
			upd["updated_at"] = nowExpr()
		}
		if e.trackUser {
			upd["updated_by"] = nullable(uid)
		}
		res = q.Where("deleted_at IS NULL").Updates(upd)
	} else {
		if err := e.deleteAllChildren(tx, orgID, idVal); err != nil {
			return err
		}
		res = q.Delete(map[string]any{})
	}
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errNotFound
	}
	if e.hooks.AfterDelete != nil {
		if err := e.hooks.AfterDelete(x); err != nil {
			return err
		}
	}
	return e.publish(tx, orgID, "deleted", id, nil)
}

// ─── GET /:id ───

func (e *engine) get(c fiber.Ctx) error {
	orgID, ok := e.org(c)
	if !ok {
		return nil
	}
	if !e.allowed(c, ActionRead) {
		return jsonErr(c, fiber.StatusForbidden, msgForbidden)
	}
	idVal, _, ok := e.parseID(c.Params("id"))
	if !ok {
		return jsonErr(c, fiber.StatusBadRequest, msgBadID)
	}
	var out map[string]any
	err := e.read(c.Context(), orgID, func(db *gorm.DB) (err error) {
		out, err = e.loadDetail(db, orgID, idVal)
		return err
	})
	if err != nil {
		return e.fail(c, ActionRead, err)
	}
	return c.JSON(out)
}
