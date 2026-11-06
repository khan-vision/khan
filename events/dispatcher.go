package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
)

// Delivered — handler-т хүргэгдэж буй event.
type Delivered struct {
	ID            int64
	OrgID         string // NULL бол ""
	Topic         string
	AggregateType string
	AggregateID   string
	Headers       map[string]string
	// Attempts — энэ хүргэлтийн оролдлогын дугаар (1 = анхны).
	Attempts  int
	CreatedAt time.Time
	// Payload — түүхий JSON.
	Payload json.RawMessage
}

// Decode — payload-ийг v руу задлана.
func (d Delivered) Decode(v any) error {
	if err := json.Unmarshal(d.Payload, v); err != nil {
		return fmt.Errorf("events: payload задлах (id=%d): %w", d.ID, err)
	}
	return nil
}

// Handler — event боловсруулагч. Алдаа буцаавал (эсвэл panic хийвэл) event дахин
// оролдогдоно, тиймээс IDEMPOTENT байх ёстой.
type Handler func(ctx context.Context, e Delivered) error

// Анхдагч утгууд.
const (
	DefaultBatchSize    = 50
	DefaultPollInterval = time.Second
	DefaultMaxAttempts  = 10
	maxBackoff          = 5 * time.Minute
	maxErrorLen         = 2000
)

// DefaultBackoff — attempt (1-ээс эхэлсэн бүтэлгүйтлийн тоо) бүрд 1с, 2с, 4с …
// экспоненциал өсөж 5 минутаар хязгаарлагдана.
func DefaultBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 20 {
		return maxBackoff
	}
	d := time.Second << (attempt - 1)
	if d > maxBackoff {
		d = maxBackoff
	}
	return d
}

type route struct {
	exact  string // "" бол wildcard
	prefix string // "finance." (wildcard); "" + all бол "*"
	all    bool
	h      Handler
}

func (r route) matches(topic string) bool {
	switch {
	case r.all:
		return true
	case r.exact != "":
		return r.exact == topic
	default:
		return strings.HasPrefix(topic, r.prefix)
	}
}

// Dispatcher — outbox-оос event уншиж handler-т хүргэнэ. Тэг утга + DB ашиглахад
// бэлэн. Нэг Dispatcher-ийг олон процесс зэрэг ажиллуулж болно (FOR UPDATE SKIP
// LOCKED).
//
// Handler-ууд нь event-ийг түгжсэн транзакц дотор ажиллана (түгжээ commit хүртэл
// барих нь давхар хүргэлтээс хамгаалдаг). Тиймээс handler урт блоклох ёсгүй;
// handler өөрийн DB ажлаа өөр холболтоор хийнэ.
//
// Handler-гүй event: анхдагчаар боловсруулагдсанд тооцож (processed_at) тэмдэглэнэ
// — outbox хөөрөгдөхгүй, Logf-д сануулга гарна. Харин OnlyRegistered=true бол
// зөвхөн бүртгэлтэй topic-ийг claim хийнэ, бусад нь өөр Dispatcher-д үлдэнэ
// (олон процесс өөр өөр topic-ийн handler-тэй үед).
type Dispatcher struct {
	DB           *gorm.DB
	BatchSize    int           // 0 → 50
	PollInterval time.Duration // 0 → 1с
	MaxAttempts  int           // 0 → 10; хүрмэгц dead_at тавина
	// Backoff — attempt-р (бүтэлгүйтлийн тоо) дахин оролдох хүлээлт; nil → DefaultBackoff.
	Backoff        func(attempt int) time.Duration
	OnlyRegistered bool
	// Logf — nil бол чимээгүй.
	Logf func(format string, args ...any)

	mu     sync.RWMutex
	routes []route
}

// Handle — topic-д handler бүртгэнэ. topic нь яг таарах ("finance.invoice.posted"),
// "prefix.*" ("finance.*" нь "finance."-аар эхэлсэн бүгдийг, "finance"-ийг үгүй),
// эсвэл "*" (бүгд). Нэг event-д таарсан БҮХ handler бүртгэлийн дарааллаар
// ажиллана; аль нэг нь алдвал event бүхлээрээ дахин оролдогдоно.
func (d *Dispatcher) Handle(topic string, h Handler) error {
	if h == nil {
		return errors.New("events: handler nil")
	}
	var r route
	switch {
	case topic == "*":
		r = route{all: true, h: h}
	case strings.HasSuffix(topic, ".*"):
		p := strings.TrimSuffix(topic, ".*")
		if !topicRe.MatchString(p) {
			return fmt.Errorf("%w: %q", ErrInvalidTopic, topic)
		}
		r = route{prefix: p + ".", h: h}
	default:
		if !topicRe.MatchString(topic) {
			return fmt.Errorf("%w: %q", ErrInvalidTopic, topic)
		}
		r = route{exact: topic, h: h}
	}
	d.mu.Lock()
	d.routes = append(d.routes, r)
	d.mu.Unlock()
	return nil
}

func (d *Dispatcher) handlersFor(topic string) []Handler {
	d.mu.RLock()
	defer d.mu.RUnlock()
	var out []Handler
	for _, r := range d.routes {
		if r.matches(topic) {
			out = append(out, r.h)
		}
	}
	return out
}

func (d *Dispatcher) logf(format string, args ...any) {
	if d.Logf != nil {
		d.Logf(format, args...)
	}
}

func (d *Dispatcher) batchSize() int {
	if d.BatchSize > 0 {
		return d.BatchSize
	}
	return DefaultBatchSize
}

func (d *Dispatcher) maxAttempts() int {
	if d.MaxAttempts > 0 {
		return d.MaxAttempts
	}
	return DefaultMaxAttempts
}

func (d *Dispatcher) backoff(attempt int) time.Duration {
	if d.Backoff != nil {
		return d.Backoff(attempt)
	}
	return DefaultBackoff(attempt)
}

type outboxRow struct {
	ID            int64
	OrgID         *string
	Topic         string
	AggregateType *string
	AggregateID   *string
	Payload       []byte
	Headers       []byte
	CreatedAt     time.Time
	Attempts      int
}

func (r outboxRow) delivered() Delivered {
	e := Delivered{
		ID: r.ID, Topic: r.Topic, CreatedAt: r.CreatedAt,
		Attempts: r.Attempts + 1, Payload: json.RawMessage(r.Payload),
	}
	if r.OrgID != nil {
		e.OrgID = *r.OrgID
	}
	if r.AggregateType != nil {
		e.AggregateType = *r.AggregateType
	}
	if r.AggregateID != nil {
		e.AggregateID = *r.AggregateID
	}
	if len(r.Headers) > 0 {
		_ = json.Unmarshal(r.Headers, &e.Headers)
	}
	return e
}

// claimFilter — OnlyRegistered үед claim хийх topic-ийн SQL нөхцөл.
func (d *Dispatcher) claimFilter() (string, []any, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	var conds []string
	var args []any
	for _, r := range d.routes {
		switch {
		case r.all:
			return "", nil, true
		case r.exact != "":
			conds = append(conds, "topic = ?")
			args = append(args, r.exact)
		default:
			conds = append(conds, `topic LIKE ?`)
			args = append(args, strings.NewReplacer(`\`, `\\`, `_`, `\_`, `%`, `\%`).Replace(r.prefix)+"%")
		}
	}
	if len(conds) == 0 {
		return "FALSE", nil, false
	}
	return "(" + strings.Join(conds, " OR ") + ")", args, false
}

// ProcessOnce — нэг багц (BatchSize) бэлэн event-ийг түгжиж, боловсруулна.
// Боловсруулсан (амжилттай эсвэл handler-гүй) event-ийн тоог буцаана; алдаатай
// event-үүд дахин оролдохоор төлөвлөгдсөн (эсвэл dead болсон) ч тоонд ордоггүй.
func (d *Dispatcher) ProcessOnce(ctx context.Context) (int, error) {
	if d.DB == nil {
		return 0, errors.New("events: Dispatcher.DB nil")
	}
	processed := 0
	err := d.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		processed = 0
		q := `SELECT id, org_id::text AS org_id, topic, aggregate_type, aggregate_id,
			payload, headers, created_at, attempts
			FROM ` + table + `
			WHERE processed_at IS NULL AND dead_at IS NULL AND available_at <= now()`
		var args []any
		if d.OnlyRegistered {
			cond, a, all := d.claimFilter()
			if !all {
				q += " AND " + cond
				args = append(args, a...)
			}
		}
		q += " ORDER BY id LIMIT ? FOR UPDATE SKIP LOCKED"
		args = append(args, d.batchSize())

		var rows []outboxRow
		if err := tx.Raw(q, args...).Scan(&rows).Error; err != nil {
			return fmt.Errorf("events: claim: %w", err)
		}
		for _, row := range rows {
			ok, err := d.deliver(ctx, tx, row)
			if err != nil {
				return err
			}
			if ok {
				processed++
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return processed, nil
}

// deliver — нэг event-ийг handler-үүдэд өгч, үр дүнг тэмдэглэнэ. ok=true бол
// боловсруулагдсан.
func (d *Dispatcher) deliver(ctx context.Context, tx *gorm.DB, row outboxRow) (bool, error) {
	hs := d.handlersFor(row.Topic)
	if len(hs) == 0 {
		d.logf("events: #%d %s — handler алга, боловсруулсанд тооцлоо", row.ID, row.Topic)
	}
	var herr error
	ev := row.delivered()
	for _, h := range hs {
		if herr = safeCall(ctx, h, ev); herr != nil {
			break
		}
	}
	if herr == nil {
		err := tx.Exec(`UPDATE `+table+` SET processed_at = now(), last_error = NULL WHERE id = ?`, row.ID).Error
		if err != nil {
			return false, fmt.Errorf("events: #%d тэмдэглэх: %w", row.ID, err)
		}
		return true, nil
	}

	attempts := row.Attempts + 1
	msg := herr.Error()
	if len(msg) > maxErrorLen {
		msg = msg[:maxErrorLen]
	}
	if attempts >= d.maxAttempts() {
		d.logf("events: #%d %s — %d оролдлогын дараа dead: %v", row.ID, row.Topic, attempts, herr)
		err := tx.Exec(`UPDATE `+table+` SET attempts = ?, last_error = ?, dead_at = now() WHERE id = ?`,
			attempts, msg, row.ID).Error
		if err != nil {
			return false, fmt.Errorf("events: #%d dead тэмдэглэх: %w", row.ID, err)
		}
		return false, nil
	}
	wait := d.backoff(attempts)
	d.logf("events: #%d %s — алдаа (%d), %s дараа дахин: %v", row.ID, row.Topic, attempts, wait, herr)
	err := tx.Exec(`UPDATE `+table+` SET attempts = ?, last_error = ?,
		available_at = now() + make_interval(secs => ?) WHERE id = ?`,
		attempts, msg, wait.Seconds(), row.ID).Error
	if err != nil {
		return false, fmt.Errorf("events: #%d retry тэмдэглэх: %w", row.ID, err)
	}
	return false, nil
}

// safeCall — handler-ийн panic-ийг алдаа болгоно.
func safeCall(ctx context.Context, h Handler, e Delivered) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("events: handler panic: %v", r)
		}
	}()
	return h(ctx, e)
}

// Run — ctx дуустал ProcessOnce-ийг давтана. Багц дүүрэн байвал шууд үргэлжилнэ,
// үгүй бол PollInterval хүлээнэ. DB алдааг Logf-д бичээд үргэлжлүүлнэ. ctx
// дуусахад nil буцаана.
func (d *Dispatcher) Run(ctx context.Context) error {
	if d.DB == nil {
		return errors.New("events: Dispatcher.DB nil")
	}
	poll := d.PollInterval
	if poll <= 0 {
		poll = DefaultPollInterval
	}
	for ctx.Err() == nil {
		n, err := d.ProcessOnce(ctx)
		if err != nil && ctx.Err() == nil {
			d.logf("events: ProcessOnce: %v", err)
		}
		if err == nil && n >= d.batchSize() {
			continue
		}
		select {
		case <-ctx.Done():
		case <-time.After(poll):
		}
	}
	return nil
}

// Purge — olderThan-аас өмнө боловсруулагдсан event-үүдийг устгана (dead болон
// хүлээгдэж буй мөрийг хөдөлгөхгүй). Устгасан мөрийн тоог буцаана.
func Purge(ctx context.Context, gdb *gorm.DB, olderThan time.Duration) (int64, error) {
	if gdb == nil {
		return 0, errors.New("events: *gorm.DB nil")
	}
	res := gdb.WithContext(ctx).Exec(`DELETE FROM `+table+`
		WHERE processed_at IS NOT NULL AND processed_at < now() - make_interval(secs => ?)`,
		olderThan.Seconds())
	if res.Error != nil {
		return 0, fmt.Errorf("events: purge: %w", res.Error)
	}
	return res.RowsAffected, nil
}
