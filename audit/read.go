package audit

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"gorm.io/gorm"
)

// Хуудаслалт: анхдагч / дээд хэмжээ.
const (
	DefaultLimit = 20
	MaxLimit     = 100
	// MaxPage — offset хуудаслалтын дээд хуудас (MaxPage×MaxLimit = 100 мянга хүртэл мөр алгасна).
	// Үүнээс гүн уншихад `before_id` (keyset) ашиглана.
	MaxPage = 1000
	// MaxTotal — нийт тооны дээд хязгаар: Count нь хамгийн ихдээ MaxTotal+1 мөр л тоолно.
	MaxTotal = 10000
)

// ErrPageTooDeep — Filter.Page > MaxPage (гүн offset үнэтэй; before_id ашигла).
var ErrPageTooDeep = errors.New("audit: page хэт гүн — before_id (keyset) ашиглана уу")

// Filter — түүх унших шүүлт. OrgID ЗААВАЛ (fail-closed): байгууллага хоорондын түүх хэзээ ч холилдохгүй.
type Filter struct {
	OrgID     string
	NoOrg     bool   // true бол org_id IS NULL мөрүүд (OrgID хоосон байх ёстой); org-гүй actor
	Resource  string // хоосон бол бүх resource
	RecordID  string
	ActorID   string
	Action    string
	RequestID string
	From      time.Time // created_at >= From
	To        time.Time // created_at <  To (хоосон бол хязгааргүй)

	// BeforeID > 0 бол зөвхөн id < BeforeID мөрүүд (keyset хуудаслалт: өмнөх хуудасны
	// NextBeforeID-г дамжуулна; гүн хуудсанд offset-оос хурдан). Page-тэй хамт ч болно.
	BeforeID int64

	Page  int // 1-ээс (≤ MaxPage); 0 → 1
	Limit int // 0 → DefaultLimit; MaxLimit-ээс их бол MaxLimit

	IncludeSnapshot bool // snapshot-ийг хариунд оруулах (том байж болно)
}

// Item — унших үеийн нэг бичлэг (JSON-д шууд гарна).
type Item struct {
	ID          int64           `json:"id"`
	Resource    string          `json:"resource"`
	RecordID    string          `json:"record_id"`
	Action      string          `json:"action"`
	ActionLabel string          `json:"action_label"`
	ActorID     string          `json:"actor_id,omitempty"`
	ActorRole   string          `json:"actor_role,omitempty"`
	IP          string          `json:"ip,omitempty"`
	UserAgent   string          `json:"user_agent,omitempty"`
	RequestID   string          `json:"request_id,omitempty"`
	Changes     json.RawMessage `json:"changes"`
	Snapshot    json.RawMessage `json:"snapshot,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
}

// ParsedChanges — Changes талбарыг Changes төрөл болгон задалж өгнө.
func (it Item) ParsedChanges() (Changes, error) {
	var cs Changes
	if err := json.Unmarshal(it.Changes, &cs); err != nil {
		return nil, err
	}
	return cs, nil
}

// Page — хуудаслагдсан хариу.
//
// Total нь MaxTotal-аар хязгаарлагдсан (TotalCapped=true бол бодит тоо түүнээс их). HasMore =
// дараагийн хуудас байна; NextBeforeID = энэ хуудасны хамгийн жижиг id (HasMore үед) — дараагийн
// keyset хүсэлтийн `before_id`.
type Page struct {
	Data         []Item `json:"data"`
	Total        int64  `json:"total"`
	TotalCapped  bool   `json:"total_capped"`
	HasMore      bool   `json:"has_more"`
	NextBeforeID int64  `json:"next_before_id,omitempty"`
	Page         int    `json:"page"`
	Limit        int    `json:"limit"`
}

type dbRow struct {
	ID        int64
	Resource  string
	RecordID  string
	Action    string
	ActorID   *string
	ActorRole *string
	IP        *string
	UserAgent *string
	RequestID *string
	Changes   []byte
	Snapshot  []byte
	CreatedAt time.Time
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// List — шүүлтэд таарах бичлэгүүдийг ШИНЭ нь эхэнд (created_at DESC, id DESC) буцаана.
// db нь RLS-тэй бол дуудагч tenant.Tx доторх *gorm.DB өгнө.
func List(ctx context.Context, db *gorm.DB, f Filter) (Page, error) {
	if db == nil {
		return Page{}, errors.New("audit: *gorm.DB nil")
	}
	if f.OrgID == "" && !f.NoOrg {
		return Page{}, errors.New("audit: Filter.OrgID заавал (org-гүй бол NoOrg)")
	}
	if f.OrgID != "" && f.NoOrg {
		return Page{}, errors.New("audit: Filter.NoOrg үед OrgID хоосон байх ёстой")
	}
	if f.Page < 1 {
		f.Page = 1
	}
	if f.Limit <= 0 {
		f.Limit = DefaultLimit
	}
	if f.Limit > MaxLimit {
		f.Limit = MaxLimit
	}
	if f.Page > MaxPage {
		return Page{}, ErrPageTooDeep
	}
	base := func() *gorm.DB {
		q := db.WithContext(ctx).Table(Table)
		if f.NoOrg {
			q = q.Where("org_id IS NULL")
		} else {
			q = q.Where("org_id = ?", f.OrgID)
		}
		if f.Resource != "" {
			q = q.Where("resource = ?", f.Resource)
		}
		if f.RecordID != "" {
			q = q.Where("record_id = ?", f.RecordID)
		}
		if f.ActorID != "" {
			q = q.Where("actor_id = ?", f.ActorID)
		}
		if f.Action != "" {
			q = q.Where("action = ?", f.Action)
		}
		if f.RequestID != "" {
			q = q.Where("request_id = ?", f.RequestID)
		}
		if !f.From.IsZero() {
			q = q.Where("created_at >= ?", f.From)
		}
		if !f.To.IsZero() {
			q = q.Where("created_at < ?", f.To)
		}
		if f.BeforeID > 0 {
			q = q.Where("id < ?", f.BeforeID)
		}
		return q
	}
	out := Page{Data: []Item{}, Page: f.Page, Limit: f.Limit}
	// Хязгаарлагдсан тоо: хамгийн ихдээ MaxTotal+1 мөр (индексээр) — гүн/том түүхэнд Count(*) үнэтэй.
	if err := db.WithContext(ctx).Table("(?) AS c", base().Select("1").Limit(MaxTotal+1)).Count(&out.Total).Error; err != nil {
		return Page{}, err
	}
	if out.Total > MaxTotal {
		out.Total, out.TotalCapped = MaxTotal, true
	}
	if out.Total == 0 {
		return out, nil
	}
	cols := "id, resource, record_id, action, actor_id, actor_role, ip, user_agent, request_id, changes, created_at"
	if f.IncludeSnapshot {
		cols += ", snapshot"
	}
	var rows []dbRow
	err := base().Select(cols).Order("id DESC").
		Offset((f.Page - 1) * f.Limit).Limit(f.Limit + 1).Find(&rows).Error
	if err != nil {
		return Page{}, err
	}
	if len(rows) > f.Limit { // +1 мөр = дараагийн хуудас байгаа эсэх
		rows, out.HasMore = rows[:f.Limit], true
		out.NextBeforeID = rows[f.Limit-1].ID
	}
	for _, r := range rows {
		it := Item{
			ID: r.ID, Resource: r.Resource, RecordID: r.RecordID, Action: r.Action,
			ActionLabel: Action(r.Action).Label(),
			ActorID:     deref(r.ActorID), ActorRole: deref(r.ActorRole), IP: deref(r.IP),
			UserAgent: deref(r.UserAgent), RequestID: deref(r.RequestID),
			Changes: json.RawMessage(r.Changes), CreatedAt: r.CreatedAt,
		}
		if len(it.Changes) == 0 {
			it.Changes = json.RawMessage("{}")
		}
		if len(r.Snapshot) > 0 {
			it.Snapshot = json.RawMessage(r.Snapshot)
		}
		out.Data = append(out.Data, it)
	}
	return out, nil
}
