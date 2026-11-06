// Package events нь transactional outbox (domain event) суурь.
//
// Хуучин Lambda-ийн after-insert hook нь controller-ийг шууд дууддаг тул алдаа
// гарвал ажил (notify, cache invalidation, realtime, санхүүгийн журнал) чимээгүй
// алга болдог байв. Outbox-д:
//
//  1. Бизнес өгөгдөл бичдэг ТЭРЭЭР транзакцдаа Publish хийнэ — event бизнес
//     өгөгдөлтэй хамт commit/rollback болно.
//
//  2. Dispatcher нь commit болсон event-үүдийг уншиж handler-т хүргэнэ.
//     Хүргэлт AT-LEAST-ONCE: handler алдвал backoff-оор дахин оролдоно, тиймээс
//     handler IDEMPOTENT байх ёстой (Delivered.ID-г dedupe түлхүүр болгож болно).
//
//     err := db.Tx(ctx, gdb, func(tx *gorm.DB) error {
//     if err := tx.Create(&inv).Error; err != nil { return err }
//     return events.Publish(tx, events.Event{
//     OrgID: orgID, Topic: "finance.invoice.posted",
//     AggregateType: "invoice", AggregateID: inv.ID, Payload: inv,
//     })
//     })
//
//     d := &events.Dispatcher{DB: gdb}
//     d.Handle("finance.invoice.posted", postJournal)
//     d.Handle("realtime.*", pushCentrifugo)
//     go d.Run(ctx)
//
// Хүснэгтийг SchemaSQL-ээр үүсгэнэ (app-ийн migration-д оруулна).
package events

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

	"gorm.io/gorm"
)

// SchemaSQL — outbox хүснэгтийн идемпотент DDL.
const SchemaSQL = `
CREATE SCHEMA IF NOT EXISTS khan;
CREATE TABLE IF NOT EXISTS khan.outbox_events (
	id             bigserial PRIMARY KEY,
	org_id         uuid,
	topic          text NOT NULL,
	aggregate_type text,
	aggregate_id   text,
	payload        jsonb NOT NULL,
	headers        jsonb,
	created_at     timestamptz NOT NULL DEFAULT now(),
	available_at   timestamptz NOT NULL DEFAULT now(),
	attempts       int NOT NULL DEFAULT 0,
	last_error     text,
	processed_at   timestamptz,
	dead_at        timestamptz
);
CREATE INDEX IF NOT EXISTS outbox_events_pending_idx
	ON khan.outbox_events (available_at, id)
	WHERE processed_at IS NULL AND dead_at IS NULL;
`

const table = "khan.outbox_events"

var (
	// ErrInvalidTopic — topic `finance.invoice.posted` хэлбэртэй биш.
	ErrInvalidTopic = errors.New("events: topic буруу (жижиг үсэг, тоо, `_`; цэгээр тусгаарлана, жнь finance.invoice.posted)")
	// ErrNotInTx — Publish транзакцгүй *gorm.DB-ээр дуудагдсан.
	ErrNotInTx = errors.New("events: Publish нь бизнес өгөгдөлтэй ижил транзакцаар дуудагдах ёстой")

	topicRe = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)*$`)
)

// Event — outbox-д бичих domain event.
type Event struct {
	OrgID         string // хоосон бол NULL (системийн түвшний event)
	Topic         string // жнь "finance.invoice.posted"
	AggregateType string
	AggregateID   string
	Payload       any // JSON-д хөрвөнө; nil бол "null"
	Headers       map[string]string
}

// Publish — event-ийг caller-ийн транзакцад бичнэ. tx нь db.Tx / gorm
// Transaction доторх *gorm.DB байх ёстой; ингэснээр event бизнес өгөгдөлтэй
// атомар commit/rollback болно (транзакц биш бол ErrNotInTx).
func Publish(tx *gorm.DB, e Event) error {
	if tx == nil {
		return errors.New("events: *gorm.DB nil")
	}
	if !topicRe.MatchString(e.Topic) {
		return fmt.Errorf("%w: %q", ErrInvalidTopic, e.Topic)
	}
	if _, ok := tx.Statement.ConnPool.(gorm.TxCommitter); !ok {
		return ErrNotInTx
	}
	payload, err := json.Marshal(e.Payload)
	if err != nil {
		return fmt.Errorf("events: payload JSON болгож чадсангүй: %w", err)
	}
	var headers any // NULL
	if len(e.Headers) > 0 {
		b, err := json.Marshal(e.Headers)
		if err != nil {
			return fmt.Errorf("events: headers JSON болгож чадсангүй: %w", err)
		}
		headers = string(b)
	}
	err = tx.Exec(`INSERT INTO `+table+`
		(org_id, topic, aggregate_type, aggregate_id, payload, headers)
		VALUES (NULLIF(?, '')::uuid, ?, NULLIF(?, ''), NULLIF(?, ''), ?::jsonb, ?::jsonb)`,
		e.OrgID, e.Topic, e.AggregateType, e.AggregateID, string(payload), headers).Error
	if err != nil {
		return fmt.Errorf("events: %s бичиж чадсангүй: %w", e.Topic, err)
	}
	return nil
}
