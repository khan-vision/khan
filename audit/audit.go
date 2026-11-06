// Package audit нь өөрчлөлтийн түүх (audit log): хэн, хэзээ, хаанаас, юуг, хуучин → шинээр
// ямар утгаар өөрчилсөн, мөн сэргээхэд хэрэгтэй snapshot.
//
// Хуучин Lambda `crudlogger` нь хүсэлтийн түүхий input-ийг транзакцийн ГАДНА, үйлдлийн
// дараа бичдэг байсан тул (a) бичилт амжилтгүй болсон ч лог үлддэг, (b) лог алдагдахад
// өгөгдөл өөрчлөгдсөн хэвээр, (c) нууц үг талбар логт орно, (d) хуучин утга байхгүй байв.
// Энд:
//
//  1. Record нь бизнес өгөгдөлтэй ИЖИЛ транзакцад бичигдэнэ — хамт commit/rollback
//     (events.Publish-тэй ижил гэрээ). Audit бичилт амжилтгүй бол бизнес бичилт ч буцна.
//  2. Diff нь зөвхөн ӨӨРЧЛӨГДСӨН талбарыг [old, new] хэлбэрээр, төрөл/формат нормальчилж
//     (12.5 = "12.50", өөр бүсийн ижил цаг) харьцуулна; нууц талбар «***».
//  3. Бичигдсэн мөр өөрчлөгддөггүй (DB trigger UPDATE/DELETE/TRUNCATE-ийг хориглоно); устгах = зөвхөн Purge.
//  4. created_at = clock_timestamp() (бодит бичилтийн агшин); түүхийн эрэмбэ = id (оруулалтын дараалал).
//
// crud-тэй холбох: crud.Resource.Audit (docs/CRUD.md). Гэрээ: docs/AUDIT.md.
//
//	err := db.Tx(ctx, gdb, func(tx *gorm.DB) error {
//	    if err := tx.Exec(`UPDATE finance.salary SET amount = ? WHERE id = ?`, ...).Error; err != nil {
//	        return err
//	    }
//	    return audit.Record(tx, audit.Entry{
//	        OrgID: orgID, Resource: "finance.salary", RecordID: id, Action: audit.ActionUpdate,
//	        Meta:    audit.MetaFrom(c),
//	        Changes: audit.Diff(oldRow, newRow, audit.DiffOptions{}),
//	    })
//	})
//
// Хүснэгтийг SchemaSQL-ээр үүсгэнэ (app-ийн migration-д оруулна).
package audit

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/khan-vision/khan/tenant"
	"gorm.io/gorm"
)

// Table — audit хүснэгтийн нэр.
const Table = "khan.audit_log"

// SchemaSQL — хүснэгт, индекс, өөрчлөлт/устгалт/TRUNCATE-ийг хориглох trigger-ийн идемпотент DDL.
// created_at-ийн default нь clock_timestamp() (now() нь транзакцийн ЭХЛЭЛ — зэрэг бичилтийн түүх
// урвуу харагддаг байсан); одоо байгаа хүснэгтийн default-ыг ч шинэчилнэ.
//
// Partition-д бэлэн: бүх уншилт `org_id` (+ resource/record_id) ба `created_at`-аар шүүгддэг.
// Том (сая мөрөөс дээш) болбол `created_at`-аар RANGE partition болгож, хуучин partition-ийг
// DETACH/DROP хийх нь Purge-ээс хямд. Partition хийхэд PRIMARY KEY нь (id, created_at) болно —
// docs/AUDIT.md «Partition» хэсгийг үз.
const SchemaSQL = `
SELECT pg_advisory_xact_lock(hashtext('khan.audit_log.schema'));
CREATE SCHEMA IF NOT EXISTS khan;
CREATE TABLE IF NOT EXISTS khan.audit_log (
	id         bigserial PRIMARY KEY,
	org_id     uuid NOT NULL,
	resource   text NOT NULL,
	record_id  text NOT NULL,
	action     text NOT NULL,
	actor_id   text,
	actor_role text,
	ip         text,
	user_agent text,
	request_id text,
	changes    jsonb NOT NULL DEFAULT '{}'::jsonb,
	snapshot   jsonb,
	created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
DO $$
BEGIN
	-- Хуучин (now()) default-ыг л солино: ALTER нь хүснэгтийг түгждэг тул дахин ажиллуулахад алгасна.
	IF EXISTS (SELECT 1 FROM pg_attrdef d JOIN pg_attribute a ON a.attrelid = d.adrelid AND a.attnum = d.adnum
			WHERE d.adrelid = 'khan.audit_log'::regclass AND a.attname = 'created_at'
			AND pg_get_expr(d.adbin, d.adrelid) NOT LIKE 'clock_timestamp()%') THEN
		ALTER TABLE khan.audit_log ALTER COLUMN created_at SET DEFAULT clock_timestamp();
	END IF;
END
$$;
DROP INDEX IF EXISTS khan.audit_log_record_idx;
DROP INDEX IF EXISTS khan.audit_log_resource_idx;
CREATE INDEX IF NOT EXISTS audit_log_record_id_idx
	ON khan.audit_log (org_id, resource, record_id, id DESC);
CREATE INDEX IF NOT EXISTS audit_log_resource_id_idx
	ON khan.audit_log (org_id, resource, id DESC);
CREATE INDEX IF NOT EXISTS audit_log_actor_idx
	ON khan.audit_log (org_id, actor_id, created_at DESC);
CREATE INDEX IF NOT EXISTS audit_log_created_idx
	ON khan.audit_log (created_at);
CREATE OR REPLACE FUNCTION khan.audit_log_immutable() RETURNS trigger AS $$
BEGIN
	IF TG_OP = 'DELETE' AND current_setting('khan.audit_purge', true) = 'on' THEN
		RETURN OLD;
	END IF;
	RAISE EXCEPTION 'khan.audit_log: мөрийг өөрчлөх/устгах боломжгүй (append-only)' USING ERRCODE = 'insufficient_privilege';
END;
$$ LANGUAGE plpgsql;
CREATE OR REPLACE FUNCTION khan.audit_log_no_truncate() RETURNS trigger AS $$
BEGIN
	RAISE EXCEPTION 'khan.audit_log: TRUNCATE хориотой (append-only)' USING ERRCODE = 'insufficient_privilege';
END;
$$ LANGUAGE plpgsql;
DO $$
BEGIN
	-- Trigger байхгүй/хуучин (зөвхөн UPDATE) үед л (дахин) үүсгэнэ: CREATE OR REPLACE TRIGGER нь
	-- хүснэгтийг түгждэг тул идемпотент дахин ажиллуулалт түгжээ авахгүй.
	IF NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgrelid = 'khan.audit_log'::regclass
			AND tgname = 'audit_log_no_update' AND (tgtype & 24) = 24 AND NOT tgisinternal) THEN
		CREATE OR REPLACE TRIGGER audit_log_no_update BEFORE UPDATE OR DELETE ON khan.audit_log
			FOR EACH ROW EXECUTE FUNCTION khan.audit_log_immutable();
	END IF;
	IF NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgrelid = 'khan.audit_log'::regclass
			AND tgname = 'audit_log_no_truncate' AND (tgtype & 32) = 32 AND NOT tgisinternal) THEN
		CREATE OR REPLACE TRIGGER audit_log_no_truncate BEFORE TRUNCATE ON khan.audit_log
			FOR EACH STATEMENT EXECUTE FUNCTION khan.audit_log_no_truncate();
	END IF;
END
$$;
`

var (
	// ErrNotInTx — Record транзакцгүй *gorm.DB-ээр дуудагдсан (audit нь бизнес бичилттэй атомар байх ёстой).
	ErrNotInTx = errors.New("audit: Record нь бизнес өгөгдөлтэй ижил транзакцаар дуудагдах ёстой")
	// ErrInvalidEntry — Entry-ийн заавал талбар буруу/дутуу.
	ErrInvalidEntry = errors.New("audit: Entry буруу")

	resourceRe = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)*$`)
	actionRe   = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
)

// Action — үйлдлийн төрөл. Дээрх нь crud-ийн ашигладаг; апп өөрийн (custom) нэр ашиглаж
// болно (жижиг үсэг, тоо, `_`; ж: "post", "approve").
type Action string

const (
	ActionCreate     Action = "create"
	ActionUpdate     Action = "update"
	ActionDelete     Action = "delete"
	ActionRestore    Action = "restore"
	ActionBulkUpdate Action = "bulk_update"
	ActionBulkDelete Action = "bulk_delete"
)

// Label — Монгол шошго. Үл мэдэгдэх (custom) үйлдэл нэрээрээ буцна.
func (a Action) Label() string {
	switch a {
	case ActionCreate:
		return "Үүсгэсэн"
	case ActionUpdate:
		return "Засварласан"
	case ActionDelete:
		return "Устгасан"
	case ActionRestore:
		return "Сэргээсэн"
	case ActionBulkUpdate:
		return "Бөөнөөр засварласан"
	case ActionBulkDelete:
		return "Бөөнөөр устгасан"
	}
	return string(a)
}

// Entry — нэг audit бичлэг.
type Entry struct {
	OrgID    string // заавал (uuid); zero UUID хориотой
	Resource string // "finance.invoice" (crud.Resource.Name)
	RecordID string // мөрийн id (текст)
	Action   Action
	Meta     // хэн/хаанаас (MetaFrom)

	Changes  Changes        // Diff-ийн үр дүн; nil → {}
	Snapshot map[string]any // сонголттой: мөрийн бүтэн төлөв (restore-д)
}

// Хадгалах дээд урт (тэмдэгтээр) — хэт урт толгой хүснэгтийг дүүргэхээс сэргийлнэ.
const (
	maxActorID   = 128
	maxActorRole = 64
	maxIP        = 64
	maxUserAgent = 255
	maxRequestID = 64
	maxRecordID  = 200
	maxAction    = 40
	maxResource  = 200
)

func (e Entry) validate() error {
	switch {
	case e.OrgID == "" || e.OrgID == tenant.ZeroUUID:
		return fmt.Errorf("%w: OrgID хоосон", ErrInvalidEntry)
	case len(e.Resource) > maxResource || !resourceRe.MatchString(e.Resource):
		return fmt.Errorf("%w: Resource %q", ErrInvalidEntry, e.Resource)
	case e.RecordID == "" || utf8.RuneCountInString(e.RecordID) > maxRecordID:
		return fmt.Errorf("%w: RecordID хоосон эсвэл хэт урт", ErrInvalidEntry)
	case len(e.Action) > maxAction || !actionRe.MatchString(string(e.Action)):
		return fmt.Errorf("%w: Action %q", ErrInvalidEntry, e.Action)
	}
	return nil
}

// clean — текстийг DB-д аюулгүй болгоно: буруу UTF-8, NUL (Postgres текстэд хориотой)-г
// авч, max тэмдэгтээр таслана.
func clean(s string, max int) string {
	s = strings.ToValidUTF8(s, "")
	if strings.IndexByte(s, 0) >= 0 {
		s = strings.ReplaceAll(s, "\x00", "")
	}
	if utf8.RuneCountInString(s) > max {
		r := []rune(s)
		s = string(r[:max])
	}
	return s
}

// Record — entry-г caller-ийн транзакцад бичнэ. tx нь db.Tx / tenant.Tx / gorm Transaction
// доторх *gorm.DB байх ёстой (үгүй бол ErrNotInTx). Changes/Snapshot-ийн нууц түлхүүр
// (password, token, secret, …) энд дахин «***» болно — Diff-ээр дамжаагүй гар бүтээсэн
// Changes ч нууц задруулахгүй.
func Record(tx *gorm.DB, e Entry) error { return RecordMany(tx, []Entry{e}) }

// RecordMany — олон entry-г нэг INSERT-ээр (chunk-лан) бичнэ. Нэг нь буруу бол юу ч бичигдэхгүй.
func RecordMany(tx *gorm.DB, entries []Entry) error {
	if tx == nil {
		return errors.New("audit: *gorm.DB nil")
	}
	if _, ok := tx.Statement.ConnPool.(gorm.TxCommitter); !ok {
		return ErrNotInTx
	}
	if len(entries) == 0 {
		return nil
	}
	type row struct {
		args []any
	}
	rows := make([]row, 0, len(entries))
	for i := range entries {
		e := entries[i]
		if err := e.validate(); err != nil {
			return err
		}
		changes := e.Changes
		if changes == nil {
			changes = Changes{}
		}
		changesJSON, err := json.Marshal(sanitizeChanges(changes))
		if err != nil {
			return fmt.Errorf("audit: changes JSON болгож чадсангүй: %w", err)
		}
		var snapshot any // NULL
		if e.Snapshot != nil {
			b, err := json.Marshal(Snapshot(e.Snapshot, DiffOptions{}))
			if err != nil {
				return fmt.Errorf("audit: snapshot JSON болгож чадсангүй: %w", err)
			}
			snapshot = string(b)
		}
		rows = append(rows, row{args: []any{
			e.OrgID, e.Resource, e.RecordID, string(e.Action),
			clean(e.ActorID, maxActorID), clean(e.ActorRole, maxActorRole), clean(e.IP, maxIP),
			clean(e.UserAgent, maxUserAgent), clean(e.RequestID, maxRequestID),
			string(changesJSON), snapshot,
		}})
	}

	const cols = `(org_id, resource, record_id, action, actor_id, actor_role, ip, user_agent, request_id, changes, snapshot)`
	const ph = `(?::uuid, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), ?::jsonb, ?::jsonb)`
	const chunk = 500 // 11 параметр × 500 < 65535
	for start := 0; start < len(rows); start += chunk {
		end := min(start+chunk, len(rows))
		var sb strings.Builder
		sb.WriteString("INSERT INTO " + Table + " " + cols + " VALUES ")
		args := make([]any, 0, (end-start)*11)
		for i := start; i < end; i++ {
			if i > start {
				sb.WriteByte(',')
			}
			sb.WriteString(ph)
			args = append(args, rows[i].args...)
		}
		if err := tx.Exec(sb.String(), args...).Error; err != nil {
			return fmt.Errorf("audit: бичиж чадсангүй: %w", err)
		}
	}
	return nil
}

// sanitizeChanges — Record-д орж ирсэн Changes-ийг дахин шүүнэ (нууц түлхүүр «***», мөр дэх token).
func sanitizeChanges(cs Changes) Changes {
	o := DiffOptions{}.compile()
	out := make(Changes, len(cs))
	for k, c := range cs {
		out[k] = Change{Old: o.render(k, normalize(c.Old)), New: o.render(k, normalize(c.New))}
	}
	return out
}
