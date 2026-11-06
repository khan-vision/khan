package audit

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
)

// ErrPurgeCutoff — Purge-ийн хугацаа хоосон эсвэл ирээдүйд (бүх түүхийг санамсаргүй
// устгахаас сэргийлнэ).
var ErrPurgeCutoff = errors.New("audit: Purge-ийн olderThan хоосон эсвэл ирээдүйд байна")

// ErrPurgeScope — Purge-д хамрах хүрээ заагаагүй: ForOrg эсвэл AllOrgs() заавал (fail-closed).
var ErrPurgeScope = errors.New("audit: Purge-д ForOrg(orgID) эсвэл AllOrgs() заавал")

type purgeCfg struct {
	orgID, resource string
	batch           int
	allOrgs         bool
}

// PurgeOption — Purge-ийн нэмэлт шүүлт.
type PurgeOption func(*purgeCfg)

// ForOrg — зөвхөн нэг байгууллагын бичлэгийг устгана.
func ForOrg(orgID string) PurgeOption { return func(c *purgeCfg) { c.orgID = orgID } }

// AllOrgs — БҮХ байгууллагын бичлэгийг устгахыг ил зөвшөөрнө (зөвхөн системийн retention cron).
// ForOrg/AllOrgs аль нь ч өгөөгүй бол Purge ErrPurgeScope буцаана.
func AllOrgs() PurgeOption { return func(c *purgeCfg) { c.allOrgs = true } }

// ForResource — зөвхөн нэг resource-ийн бичлэгийг устгана.
func ForResource(resource string) PurgeOption { return func(c *purgeCfg) { c.resource = resource } }

// Batch — нэг DELETE-ийн дээд мөр (анхдагч 5000): удаан түгжээ, том WAL-аас сэргийлнэ.
func Batch(n int) PurgeOption { return func(c *purgeCfg) { c.batch = n } }

// Purge — olderThan-аас ӨМНӨХ created_at-тай бичлэгүүдийг багцлан устгаж, нийт устгасан тоог
// буцаана. audit_log-ийг устгах цорын ганц албан ёсны зам: багц бүрийг өөрийн транзакцад
// `khan.audit_purge = on` (set_config, local) тавьж DELETE trigger-ийг нэвтрүүлнэ. Хүрээ (ForOrg
// эсвэл AllOrgs) заавал.
// Retention-ийн cron-оос дуудна; ctx цуцлагдвал тэр хүртэл устгасныг буцаана. Байгууллага
// тус бүрд өөр хугацаа хэрэгтэй бол ForOrg-оор дуудна.
func Purge(ctx context.Context, db *gorm.DB, olderThan time.Time, opts ...PurgeOption) (int64, error) {
	if db == nil {
		return 0, errors.New("audit: *gorm.DB nil")
	}
	if olderThan.IsZero() || olderThan.After(time.Now()) {
		return 0, ErrPurgeCutoff
	}
	cfg := purgeCfg{batch: 5000}
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.batch <= 0 {
		cfg.batch = 5000
	}
	if cfg.orgID == "" && !cfg.allOrgs {
		return 0, ErrPurgeScope
	}
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		var n int64
		err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			// Зөвхөн энэ транзакцад (local) trigger-ийг нээнэ.
			if err := tx.Exec("SELECT set_config('khan.audit_purge', 'on', true)").Error; err != nil {
				return err
			}
			sub := tx.Table(Table).Select("id").Where("created_at < ?", olderThan)
			if cfg.orgID != "" {
				sub = sub.Where("org_id = ?", cfg.orgID)
			}
			if cfg.resource != "" {
				sub = sub.Where("resource = ?", cfg.resource)
			}
			res := tx.Exec("DELETE FROM "+Table+" WHERE id IN (?)", sub.Order("id").Limit(cfg.batch))
			n = res.RowsAffected
			return res.Error
		})
		if err != nil {
			return total, err
		}
		total += n
		if n < int64(cfg.batch) {
			return total, nil
		}
	}
}
