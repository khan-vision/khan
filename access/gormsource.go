package access

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"sort"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// GormConfig — [GormSource]-ийн тохиргоо. Хүснэгт/багана нэр нь ТОГТМОЛ (config-оос),
// хэзээ ч хэрэглэгчийн оролтоос биш; ямар ч тохиолдолд идентификаторын хэлбэр шалгагдаж,
// GORM-оор quote хийгдэнэ. Утга (org, user) нь үргэлж параметр.
type GormConfig struct {
	// Table — эрхийн JSON-тэй хүснэгт/view ("schema.table" байж болно). Заавал.
	Table string
	// OrgColumn (анхдагч "org_id"), UserColumn ("user_id"), PermissionsColumn ("permissions") —
	// JSON/JSONB/text багана. NULL/хоосон мөр алгасагдана.
	OrgColumn, UserColumn, PermissionsColumn string
	// Filter — мөрийг нэмэлтээр шүүнэ (ж: идэвхтэй ажилтан `status_id = 1`, `deleted_at IS NULL`).
	// Load болон ConfiguredModules хоёуланд хэрэглэгдэнэ. nil → шүүхгүй.
	Filter func(*gorm.DB) *gorm.DB
	// Owner — байгууллагын эзнийг өөр хүснэгтээс олох (ж: organization_users.access = 'owner').
	// nil → Owner үргэлж false.
	Owner *GormOwner
	// TrustJSONOwner — JSON доторх `"owner": true`-г хүлээн авах. АНХДАГЧ false: permissions
	// JSON-ийг HR хэрэглэгч засдаг бол өөртөө owner олгох боломж болно. Owner-ийг [GormOwner]-оос ав.
	TrustJSONOwner bool
}

// GormOwner — эзний хүснэгт: (org, user) мөр Filter-ийг хангаж байвал owner.
type GormOwner struct {
	Table                 string
	OrgColumn, UserColumn string // анхдагч "org_id", "user_id"
	Filter                func(*gorm.DB) *gorm.DB
}

var tableRe = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*(\.[a-zA-Z_][a-zA-Z0-9_]*)?$`)
var columnRe = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// GormSource — GORM + Postgres дээрх лавлах [Source]: нэг хүснэгтийн JSON баганаас
// `{"modules": {...}}` уншина. (org, user)-ийн нэгээс олон мөр байвал (олон ажлын байр)
// эрхүүд НЭГДЭНЭ. [OrgConfigurer]-ийг бас хэрэгжүүлнэ.
type GormSource struct {
	db  *gorm.DB
	cfg GormConfig
}

// NewGormSource — тохиргоог шалгаж Source үүсгэнэ.
func NewGormSource(db *gorm.DB, cfg GormConfig) (*GormSource, error) {
	if db == nil {
		return nil, errors.New("access: GormSource: db nil")
	}
	def := func(p *string, v string) {
		if *p == "" {
			*p = v
		}
	}
	def(&cfg.OrgColumn, "org_id")
	def(&cfg.UserColumn, "user_id")
	def(&cfg.PermissionsColumn, "permissions")
	if !tableRe.MatchString(cfg.Table) {
		return nil, fmt.Errorf("access: GormSource: Table буруу (%q)", cfg.Table)
	}
	for _, c := range []string{cfg.OrgColumn, cfg.UserColumn, cfg.PermissionsColumn} {
		if !columnRe.MatchString(c) {
			return nil, fmt.Errorf("access: GormSource: багана буруу (%q)", c)
		}
	}
	if o := cfg.Owner; o != nil {
		oc := *o
		def(&oc.OrgColumn, "org_id")
		def(&oc.UserColumn, "user_id")
		if !tableRe.MatchString(oc.Table) || !columnRe.MatchString(oc.OrgColumn) || !columnRe.MatchString(oc.UserColumn) {
			return nil, fmt.Errorf("access: GormSource: Owner хүснэгт/багана буруу (%q)", oc.Table)
		}
		cfg.Owner = &oc
	}
	return &GormSource{db: db, cfg: cfg}, nil
}

func col(name string) clause.Column { return clause.Column{Name: name} }

func (s *GormSource) base(ctx context.Context, orgID string) *gorm.DB {
	q := s.db.WithContext(ctx).Table(s.cfg.Table).Where("? = ?", col(s.cfg.OrgColumn), orgID)
	if s.cfg.Filter != nil {
		q = s.cfg.Filter(q)
	}
	return q
}

type permRow struct {
	Perm sql.NullString `gorm:"column:perm"`
}

// Load — Source. orgID хоосон бол ErrNoOrg (хэзээ ч org-гүй query хийхгүй).
func (s *GormSource) Load(ctx context.Context, orgID, userID string) (Grants, error) {
	if orgID == "" {
		return Grants{}, ErrNoOrg
	}
	if userID == "" {
		return Grants{}, errors.New("access: user_id хоосон")
	}
	var rows []permRow
	err := s.base(ctx, orgID).
		Where("? = ?", col(s.cfg.UserColumn), userID).
		Select("?::text AS perm", col(s.cfg.PermissionsColumn)).
		Scan(&rows).Error
	if err != nil {
		return Grants{}, fmt.Errorf("access: эрх унших: %w", err)
	}
	var out Grants
	for _, r := range rows {
		if !r.Perm.Valid || r.Perm.String == "" {
			continue
		}
		g, err := ParseGrants([]byte(r.Perm.String))
		if err != nil {
			return Grants{}, err // эвдэрхий JSON: эрх тааж олгохгүй, алдаа (fail-closed)
		}
		if !s.cfg.TrustJSONOwner {
			g.Owner = false
		}
		out = out.Merge(g)
	}
	if o := s.cfg.Owner; o != nil && !out.Owner {
		q := s.db.WithContext(ctx).Table(o.Table).
			Where("? = ?", col(o.OrgColumn), orgID).
			Where("? = ?", col(o.UserColumn), userID)
		if o.Filter != nil {
			q = o.Filter(q)
		}
		var n int64
		if err := q.Count(&n).Error; err != nil {
			return Grants{}, fmt.Errorf("access: owner унших: %w", err)
		}
		out.Owner = n > 0
	}
	return out, nil
}

// ConfiguredModules — OrgConfigurer: байгууллагын мөрүүдийн JSON-оос аль модуль(ууд)-д
// хоосон биш блок байгааг олно. Зөвхөн `"modules"` агуулсан мөрийг уншина.
func (s *GormSource) ConfiguredModules(ctx context.Context, orgID string) (map[string]bool, error) {
	if orgID == "" {
		return nil, ErrNoOrg
	}
	var rows []permRow
	err := s.base(ctx, orgID).
		Where("?::text LIKE ?", col(s.cfg.PermissionsColumn), `%"modules"%`).
		Select("?::text AS perm", col(s.cfg.PermissionsColumn)).
		Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("access: тохируулсан модуль унших: %w", err)
	}
	out := map[string]bool{}
	for _, r := range rows {
		if !r.Perm.Valid {
			continue
		}
		g, err := ParseGrants([]byte(r.Perm.String))
		if err != nil {
			continue // нэг эвдэрхий мөр бусдыг "тохируулаагүй" болгохгүй; Load дээр тухайн хэрэглэгч алдаа авна
		}
		for mod, mg := range g.Modules {
			if !mg.Empty() {
				out[mod] = true
			}
		}
	}
	return out, nil
}

// ListUsers — [Lister]: org-ийн (Filter-ийг хангасан) бүх мөрийг НЭГ query-ээр уншиж, user-ээр
// нэгтгэнэ; Owner хүснэгтийн хэрэглэгчид (permissions мөргүй ч) бас орно. Эвдэрхий JSON нь
// [GormSource.Load]-той адил алдаа (fail-closed). Эрэмбэлсэн.
func (s *GormSource) ListUsers(ctx context.Context, orgID string) ([]UserGrants, error) {
	if orgID == "" {
		return nil, ErrNoOrg
	}
	var rows []struct {
		UID  sql.NullString `gorm:"column:uid"`
		Perm sql.NullString `gorm:"column:perm"`
	}
	err := s.base(ctx, orgID).
		Select("?::text AS uid, ?::text AS perm", col(s.cfg.UserColumn), col(s.cfg.PermissionsColumn)).
		Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("access: хэрэглэгчид унших: %w", err)
	}
	byUser := map[string]Grants{}
	for _, r := range rows {
		if !r.UID.Valid || r.UID.String == "" {
			continue
		}
		g := Grants{}
		if r.Perm.Valid && r.Perm.String != "" {
			if g, err = ParseGrants([]byte(r.Perm.String)); err != nil {
				return nil, err
			}
			if !s.cfg.TrustJSONOwner {
				g.Owner = false
			}
		}
		byUser[r.UID.String] = byUser[r.UID.String].Merge(g)
	}
	if o := s.cfg.Owner; o != nil {
		q := s.db.WithContext(ctx).Table(o.Table).Where("? = ?", col(o.OrgColumn), orgID)
		if o.Filter != nil {
			q = o.Filter(q)
		}
		var owners []sql.NullString
		if err := q.Select("DISTINCT ?::text", col(o.UserColumn)).Scan(&owners).Error; err != nil {
			return nil, fmt.Errorf("access: owner унших: %w", err)
		}
		for _, u := range owners {
			if u.Valid && u.String != "" {
				g := byUser[u.String]
				g.Owner = true
				byUser[u.String] = g
			}
		}
	}
	out := make([]UserGrants, 0, len(byUser))
	for u, g := range byUser {
		out = append(out, UserGrants{UserID: u, Grants: g})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UserID < out[j].UserID })
	return out, nil
}

var (
	_ Lister        = (*GormSource)(nil)
	_ Lister        = (*MemorySource)(nil)
	_ Source        = (*GormSource)(nil)
	_ OrgConfigurer = (*GormSource)(nil)
	_ Source        = (*MemorySource)(nil)
	_ OrgConfigurer = (*MemorySource)(nil)
)
