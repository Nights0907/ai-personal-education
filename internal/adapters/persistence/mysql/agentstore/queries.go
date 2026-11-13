package agentstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/musi-ai/education/internal/platform/idgen"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/go-sql-driver/mysql"
	"github.com/musi-ai/education/internal/agent/runtime/state"
	"github.com/musi-ai/education/internal/query"
)

// Queries persists raw web inputs in the SAME three Agent Store tables.
// Pending user messages have no chain position until their answer is sealed.
type Queries struct {
	Store     *Store
	Namespace string
}

var _ query.Repository = (*Queries)(nil)

func NewQueries(s *Store) *Queries               { return &Queries{Store: s, Namespace: "teacher"} }
func (q *Queries) subject(student string) string { return state.Hash([]string{q.Namespace, student}) }
func (q *Queries) Create(ctx context.Context, student string, in query.Input) (query.Record, bool, error) {
	return q.create(ctx, student, in, time.Time{}, false)
}

// Import preserves legacy input time; no new writes to the old submissions table.
func (q *Queries) Import(ctx context.Context, student string, in query.Input, createdAt time.Time) (query.Record, bool, error) {
	return q.create(ctx, student, in, createdAt, true)
}

func (q *Queries) create(ctx context.Context, student string, in query.Input, createdAt time.Time, allowLegacyCreation bool) (query.Record, bool, error) {
	if !query.ValidID(student) {
		return query.Record{}, false, query.ErrInvalid
	}
	if err := in.Validate(); err != nil {
		return query.Record{}, false, err
	}
	scope := state.Scope{Namespace: q.Namespace, SubjectID: student, ConversationID: in.ConversationID}
	root := idgen.New("b")
	tx, err := q.Store.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return query.Record{}, false, err
	}
	defer tx.Rollback()
	c := conversation{Scope: scope, Branch: root, Epoch: 1}
	if allowLegacyCreation {
		_, err = tx.ExecContext(ctx, `INSERT INTO agent_conversation(scope_key,namespace,subject_id,conversation_id,current_branch_id,ext,created_at,updated_at) VALUES(?,?,?,?,?,?,UTC_TIMESTAMP(6),UTC_TIMESTAMP(6)) ON DUPLICATE KEY UPDATE scope_key=scope_key`, scope.Key(), scope.Namespace, student, in.ConversationID, root, jsonText(c))
		if err != nil {
			return query.Record{}, false, err
		}
	}
	err = decode(tx.QueryRowContext(ctx, "SELECT ext FROM agent_conversation WHERE scope_key=? FOR UPDATE", scope.Key()), &c)
	if errors.Is(err, sql.ErrNoRows) {
		return query.Record{}, false, query.ErrNotFound
	}
	if err != nil {
		return query.Record{}, false, err
	}
	if c.Deleted {
		return query.Record{}, false, state.ErrConflict
	}
	var old messageExt
	var oldID string
	err = tx.QueryRowContext(ctx, "SELECT message_id,ext FROM agent_message WHERE subject_scope=? AND client_request_id=?", q.subject(student), in.ClientRequestID).Scan(&oldID, newJSON(&old))
	if err == nil {
		if old.QueryHash != in.Hash() {
			return query.Record{}, false, query.ErrConflict
		}
		r, e := q.get(ctx, tx, student, oldID)
		if e != nil {
			return r, false, e
		}
		return r, false, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return query.Record{}, false, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO agent_branch(scope_key,branch_id,created_version,op,created_at) VALUES(?,?,0,'ROOT',UTC_TIMESTAMP(6)) ON DUPLICATE KEY UPDATE branch_id=branch_id`, scope.Key(), c.Branch)
	if err != nil {
		return query.Record{}, false, err
	}

	if in.RegenerateOf != "" {
		if c.Active != nil {
			return query.Record{}, false, state.ErrConflict
		}
		var parent string
		var position sql.NullInt64
		var source query.Record
		source, err = q.get(ctx, tx, student, in.RegenerateOf)
		if err != nil {
			return query.Record{}, false, err
		}
		if source.ConversationID != in.ConversationID || source.Query != in.Query || source.Mode != in.Mode || source.GuideQuestionID != in.GuideQuestionID {
			return query.Record{}, false, query.ErrConflict
		}
		err = tx.QueryRowContext(ctx, "SELECT branch_id,position FROM agent_message WHERE scope_key=? AND message_id=? AND role='user'", scope.Key(), in.RegenerateOf).Scan(&parent, &position)
		if err != nil {
			return query.Record{}, false, err
		}
		boundary := int64(0)
		message, prefix := "", ""
		if position.Valid && position.Int64 > 1 {
			boundary = position.Int64 - 1
			err = tx.QueryRowContext(ctx, "SELECT message_id,prefix_hash FROM agent_message WHERE scope_key=? AND branch_id=? AND position=? AND role='assistant'", scope.Key(), parent, boundary).Scan(&message, &prefix)
			if err != nil {
				return query.Record{}, false, err
			}
		}
		if !position.Valid {
			parent = c.Branch
			boundary = c.Tip.Position
			message = c.Tip.MessageID
			prefix = c.Tip.PrefixHash
		}
		branch := idgen.New("b")
		version := c.Version
		c.Version++
		c.Epoch++
		_, err = tx.ExecContext(ctx, `INSERT INTO agent_branch(scope_key,branch_id,parent_branch_id,fork_parent_message_id,fork_parent_position,fork_parent_version,created_version,op,created_at) VALUES(?,?,?,?,?,?,?,'REGENERATE',UTC_TIMESTAMP(6))`, scope.Key(), branch, parent, nullable(message), boundary, version, c.Version)
		if err != nil {
			return query.Record{}, false, err
		}
		c.Branch = branch
		c.Tip = state.MessageCursor{BranchID: branch, PrefixHash: prefix}
		c.Usage = state.ContextUsage{}
		c.Summary = state.Summary{}
		c.SummaryToken = state.ReadToken{}
		c.SummaryThrough = state.MessageCursor{}
	}
	turn := idgen.New("t")
	id := state.MessageID(scope, turn, "user")
	content := jsonText(struct {
		Items []*schema.Message `json:"items"`
	}{[]*schema.Message{schema.UserMessage(in.Query)}})
	hash := state.Hash(struct{ Schema, Payload string }{"eino-text-v1", content})
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO agent_message(scope_key,message_id,branch_id,parent_message_id,position,version,role,status,subject_scope,client_request_id,content,ext,turn_id,run_id,turn_cursor,content_hash,prefix_hash,created_at) VALUES(?,?,?,NULL,NULL,0,'user','RECEIVED',?,?,?,?,?,'',0,?,'',?)`, scope.Key(), id, c.Branch, q.subject(student), in.ClientRequestID, content, jsonText(messageExt{RadarSourceID: in.RadarSourceID, RadarSeed: in.RadarSeed, SubmittedAt: acceptedTime(in.SubmittedAt), RegenerateOf: in.RegenerateOf, QueryHash: in.Hash(), ImageAssetID: in.ImageAssetID, Mode: in.Mode, GuideQuestionID: in.GuideQuestionID}), turn, hash, createdAt.UTC())
	if err != nil {
		var e *mysql.MySQLError
		if errors.As(err, &e) && e.Number == 1062 {
			return query.Record{}, false, query.ErrConflict
		}
		return query.Record{}, false, err
	}
	if err = writeConversation(ctx, tx, c); err != nil {
		return query.Record{}, false, err
	}
	r, err := q.get(ctx, tx, student, id)
	if err != nil {
		return r, false, err
	}
	return r, true, tx.Commit()
}

// JSONScanner allows decoding a structured ext alongside other SQL columns.
type JSONScanner struct{ Value any }

func newJSON(v any) *JSONScanner { return &JSONScanner{Value: v} }
func (s *JSONScanner) Scan(v any) error {
	switch b := v.(type) {
	case []byte:
		return json.Unmarshal(b, s.Value)
	case string:
		return json.Unmarshal([]byte(b), s.Value)
	default:
		return fmt.Errorf("agent store: invalid JSON column")
	}
}

const queryColumns = "m.message_id,c.conversation_id,m.client_request_id,m.turn_id,m.content,m.ext,m.status,m.created_at"

func scanQuery(row scanner) (r query.Record, err error) {
	var content []byte
	var status string
	var meta messageExt
	err = row.Scan(&r.ID, &r.ConversationID, &r.ClientRequestID, &r.TurnID, &content, newJSON(&meta), &status, &r.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return r, query.ErrNotFound
	}
	if err != nil {
		return r, err
	}
	var block struct {
		Items []*schema.Message `json:"items"`
	}
	if err = json.Unmarshal(content, &block); err != nil {
		return r, err
	}
	if len(block.Items) != 1 || block.Items[0].Role != schema.User {
		return r, state.ErrProtocol
	}
	r.Query = block.Items[0].Content
	r.ImageAssetID = meta.ImageAssetID
	r.RegenerateOf = meta.RegenerateOf
	r.RadarSourceID = meta.RadarSourceID
	r.RadarSeed = meta.RadarSeed
	r.Mode = meta.Mode
	r.GuideQuestionID = meta.GuideQuestionID
	if meta.SubmittedAt != nil {
		r.SubmittedAt = *meta.SubmittedAt
	}
	if meta.OriginalInput != nil {
		r.Query = meta.OriginalInput.Content
	}
	r.Status = "RECEIVED"
	if status == "SEALED" {
		r.Status = "ANSWERED"
	}
	return r, nil
}
func (q *Queries) get(ctx context.Context, db queryer, student, id string) (query.Record, error) {
	return scanQuery(db.QueryRowContext(ctx, "SELECT "+queryColumns+" FROM agent_message m JOIN agent_conversation c ON c.scope_key=m.scope_key WHERE m.subject_scope=? AND m.message_id=? AND m.role='user' AND m.client_request_id IS NOT NULL AND c.status='ACTIVE'", q.subject(student), id))
}
func (q *Queries) Get(ctx context.Context, student, id string) (query.Record, error) {
	return q.get(ctx, q.Store.DB, student, id)
}

// ConversationByRequest resolves the conversation that already holds the message
// written for this client request id. Callers that regenerate content on retry
// (for example model-authored openings) use it to stay idempotent instead of
// colliding with their own earlier write.
func (q *Queries) ConversationByRequest(ctx context.Context, student, clientRequestID string) (string, error) {
	if !query.ValidID(student) || !query.ValidID(clientRequestID) {
		return "", query.ErrInvalid
	}
	var conversationID string
	err := q.Store.DB.QueryRowContext(ctx, "SELECT c.conversation_id FROM agent_message m JOIN agent_conversation c ON c.scope_key=m.scope_key WHERE m.subject_scope=? AND m.client_request_id=? AND c.status='ACTIVE' LIMIT 1", q.subject(student), clientRequestID).Scan(&conversationID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", query.ErrNotFound
	}
	return conversationID, err
}
func (q *Queries) List(ctx context.Context, student, conversationID string, before *query.Cursor, limit int) ([]query.Record, bool, error) {
	if limit < 1 || limit > 100 {
		return nil, false, query.ErrInvalid
	}
	sqlText := "SELECT " + queryColumns + " FROM agent_message m JOIN agent_conversation c ON c.scope_key=m.scope_key WHERE m.subject_scope=? AND m.role='user' AND m.client_request_id IS NOT NULL AND c.status='ACTIVE'"
	args := []any{q.subject(student)}
	if conversationID != "" {
		sqlText += " AND c.conversation_id=?"
		args = append(args, conversationID)
	}
	if before != nil {
		sqlText += " AND (m.created_at<? OR (m.created_at=? AND m.message_id<?))"
		args = append(args, before.CreatedAt, before.CreatedAt, before.ID)
	}
	sqlText += " ORDER BY m.created_at DESC,m.message_id DESC LIMIT ?"
	args = append(args, limit+1)
	rows, err := q.Store.DB.QueryContext(ctx, sqlText, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	records := []query.Record{}
	for rows.Next() {
		r, e := scanQuery(rows)
		if e != nil {
			return nil, false, e
		}
		records = append(records, r)
	}
	if err = rows.Err(); err != nil {
		return nil, false, err
	}
	more := len(records) > limit
	if more {
		records = records[:limit]
	}
	return records, more, nil
}

func acceptedTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	t = t.UTC()
	return &t
}
