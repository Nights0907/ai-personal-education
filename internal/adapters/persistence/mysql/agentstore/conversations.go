package agentstore

import (
	"context"
	"database/sql"
	"errors"
	"github.com/go-sql-driver/mysql"
	"github.com/musi-ai/education/internal/agent/runtime/state"
	"github.com/musi-ai/education/internal/platform/idgen"
	"github.com/musi-ai/education/internal/query"
	"time"
)

func (q *Queries) findCreatedConversation(ctx context.Context, student, requestID string) (out query.Conversation, err error) {
	var status string
	err = q.Store.DB.QueryRowContext(ctx, "SELECT conversation_id,current_branch_id,status FROM agent_conversation WHERE namespace=? AND subject_id=? AND creation_request_id=?", q.Namespace, student, requestID).Scan(&out.ConversationID, &out.BranchID, &status)
	if err == nil && status != "ACTIVE" {
		err = query.ErrConflict
	}
	return out, err
}

// Creation has its own request key so a lost HTTP response does not produce a
// second conversation. The browser never chooses the persisted business ID.
func (q *Queries) CreateConversation(ctx context.Context, student, requestID string) (out query.Conversation, err error) {
	if !query.ValidID(student) || !query.ValidID(requestID) {
		return out, query.ErrInvalid
	}
	if out, err = q.findCreatedConversation(ctx, student, requestID); !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	scope := state.Scope{Namespace: q.Namespace, SubjectID: student, ConversationID: idgen.New("c")}
	root := idgen.New("b")
	c := conversation{Scope: scope, Branch: root, Epoch: 1}
	tx, err := q.Store.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO agent_conversation(scope_key,namespace,subject_id,conversation_id,current_branch_id,creation_request_id,ext,created_at,updated_at) VALUES(?,?,?,?,?,?,?,UTC_TIMESTAMP(6),UTC_TIMESTAMP(6))`, scope.Key(), scope.Namespace, student, scope.ConversationID, root, requestID, jsonText(c))
	if err != nil {
		var duplicate *mysql.MySQLError
		if errors.As(err, &duplicate) && duplicate.Number == 1062 {
			_ = tx.Rollback()
			return q.findCreatedConversation(ctx, student, requestID)
		}
		return out, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO agent_branch(scope_key,branch_id,created_version,op,created_at) VALUES(?,?,0,'ROOT',UTC_TIMESTAMP(6))`, scope.Key(), root); err != nil {
		return out, err
	}
	if err = tx.Commit(); err != nil {
		return out, err
	}
	return query.Conversation{ConversationID: scope.ConversationID, BranchID: root}, nil
}

// DeleteConversation preserves messages and receipts and prevents resurrection.
func (q *Queries) DeleteConversation(ctx context.Context, student, id string) error {
	if !query.ValidID(student) || !query.ValidID(id) {
		return query.ErrInvalid
	}
	scope := state.Scope{Namespace: q.Namespace, SubjectID: student, ConversationID: id}
	tx, e := q.Store.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var c conversation
	e = decode(tx.QueryRowContext(ctx, "SELECT ext FROM agent_conversation WHERE scope_key=? FOR UPDATE", scope.Key()), &c)
	if errors.Is(e, sql.ErrNoRows) {
		return query.ErrNotFound
	}
	if e != nil {
		return e
	}
	if c.Deleted {
		return nil
	}
	if c.Active != nil && c.Active.Input.Lease.Until.After(time.Now()) {
		return query.ErrConflict
	}
	c.Deleted = true
	c.Version++
	c.Epoch++
	c.Jobs = nil
	c.Event = nil
	if e = writeConversation(ctx, tx, c); e != nil {
		return e
	}
	return tx.Commit()
}
