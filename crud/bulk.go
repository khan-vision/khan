package crud

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v3"
	"github.com/khan-vision/khan/query"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// lockBulkIDs — хүсэлтийн id-уудаас ЗӨВХӨН тухайн org-ийн (soft-delete хийгдээгүй) мөрийг
// `FOR UPDATE`-ээр түгжиж, тэдгээрийн id-г буцаана. Өөр org-ийн id чимээгүй алгасагдана
// (ExecBulk-тэй ижил нөхцөл) — version/updated_at/event зөвхөн эдгээрт.
//
// ScopeShared: харагдах мөрийн дунд системийн (org_id IS NULL) мөр байвал БҮТЭН хүсэлт 403
// (юу ч өөрчлөгдөхгүй) — системийн мөр зөвхөн PUT/DELETE /:id-ээр (SystemCan) удирдагдана.
func (e *engine) lockBulkIDs(tx *gorm.DB, orgID string, raw []string) ([]any, error) {
	ids := make([]any, 0, len(raw))
	for _, s := range raw {
		if e.idKind == IDInt {
			n, err := strconv.ParseInt(s, 10, 64)
			if err != nil {
				return nil, err // query.ParseBulk аль хэдийн шалгасан — энд хүрэхгүй
			}
			ids = append(ids, n)
			continue
		}
		ids = append(ids, s)
	}
	if len(ids) == 0 {
		return nil, nil
	}
	q := e.readWhere(tx.Table(e.table).Select(quote(e.idCol)+" AS id"), "", orgID)
	if e.scope == ScopeShared {
		q = q.Select(quote(e.idCol) + " AS id, org_id")
	}
	if e.idKind == IDInt {
		// int4 баганад int4-өөс их id encode алдаа (500) өгөхгүйн тулд bigint-ээр харьцуулна.
		q = q.Where(quote(e.idCol)+" IN ("+strings.TrimSuffix(strings.Repeat("CAST(? AS bigint),", len(ids)), ",")+")", ids...)
	} else {
		q = q.Where(quote(e.idCol)+" IN ?", ids)
	}
	if e.softDelete {
		q = q.Where("deleted_at IS NULL")
	}
	var rows []map[string]any
	if err := q.Clauses(clause.Locking{Strength: "UPDATE"}).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]any, 0, len(rows))
	for _, r := range rows {
		if e.scope == ScopeShared {
			if o, err := e.rowOwner(orgID, r); err != nil {
				return nil, err
			} else if o.system {
				return nil, Forbidden(msgSystemRow)
			}
		}
		out = append(out, r["id"])
	}
	return out, nil
}

// afterBulk — bulk-ийг энгийн update/delete-тэй ижил дүрэмд оруулна:
//   - update: updated_at = now(), updated_by, version = version + 1 (optimistic lock-ийг
//     тойрохгүй — bulk-ийн дараа хуучин version-оор PUT хийвэл 409);
//   - Events асаалттай бол мөр бүрт `<Name>.updated|deleted` event (нэг транзакцад).
func (e *engine) afterBulk(c fiber.Ctx, tx *gorm.DB, orgID string, req query.BulkRequest, ids []any) error {
	if len(ids) == 0 {
		return nil
	}
	if req.Action == "update" {
		upd := map[string]any{}
		if e.timestamps {
			upd["updated_at"] = gorm.Expr("now()")
		}
		if e.trackUser {
			upd["updated_by"] = nullable(userID(c))
		}
		if e.versionCol != "" {
			upd[e.versionCol] = gorm.Expr(quote(e.versionCol) + " + 1")
		}
		if len(upd) > 0 {
			err := e.ownWhere(tx.Table(e.table), "", owner{org: orgID}).Where(quote(e.idCol)+" IN ?", ids).
				Updates(upd).Error
			if err != nil {
				return err
			}
		}
	}
	if !e.events {
		return nil
	}
	suffix, vals := "deleted", Values(nil)
	if req.Action == "update" {
		suffix, vals = "updated", Values{}
		for key, v := range req.Set {
			if f, ok := e.bulk.Fields[key]; ok {
				vals[f.Col] = v
			}
		}
	}
	for _, id := range ids {
		if err := e.publish(tx, owner{org: orgID}, suffix, fmt.Sprint(id), vals); err != nil {
			return err
		}
	}
	return nil
}

// bulkDeleteNeedsRows — bulk delete-ийг мөр бүрээр (deleteOne) гүйцэтгэх шаардлагатай эсэх:
// delete hook эсвэл (hard delete үед cascade хийх) children байвал.
func (e *engine) bulkDeleteNeedsRows() bool {
	return e.hooks.BeforeDelete != nil || e.hooks.AfterDelete != nil || (!e.softDelete && len(e.children) > 0)
}
