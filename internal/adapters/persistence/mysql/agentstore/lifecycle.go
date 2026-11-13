package agentstore

import (
	"context"
	"database/sql"
	"errors"
	"github.com/cloudwego/eino/schema"
	"github.com/musi-ai/education/internal/agent/runtime/state"
	"github.com/musi-ai/education/internal/agent/runtime/storage"
	"strings"
)

var _ storage.RecoveryReader = (*Store)(nil)

func (s *Store) Forget(ctx context.Context, scope state.Scope) error {
	if err := scope.Validate(); err != nil {
		return err
	}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var c conversation
	err = decode(tx.QueryRowContext(ctx, "SELECT ext FROM agent_conversation WHERE scope_key=? FOR UPDATE", scope.Key()), &c)
	if errors.Is(err, sql.ErrNoRows) {
		c = conversation{Scope: scope, Epoch: 1, Deleted: true}
		_, err = tx.ExecContext(ctx, `INSERT INTO agent_conversation(scope_key,namespace,subject_id,conversation_id,current_branch_id,status,ext,created_at,updated_at) VALUES(?,?,?,?,'','DELETED',?,UTC_TIMESTAMP(6),UTC_TIMESTAMP(6))`, scope.Key(), scope.Namespace, scope.SubjectID, scope.ConversationID, jsonText(c))
		if err != nil {
			return err
		}
		return tx.Commit()
	}
	if err != nil {
		return err
	}
	if c.Deleted {
		return nil
	}
	for _, table := range []string{"agent_memory_compact", "agent_message", "agent_branch"} {
		if _, err = tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE scope_key=?", scope.Key()); err != nil {
			return err
		}
	}
	c = conversation{Scope: scope, Version: c.Version + 1, Epoch: c.Epoch + 1, Deleted: true}
	if err = writeConversation(ctx, tx, c); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for key := range s.buffers {
		if strings.HasPrefix(key, scope.Key()+":") {
			delete(s.buffers, key)
		}
	}
	return nil
}

// There is deliberately no durable execution buffer in the three-table design.
// A running process can repair from its own buffer; after restart the business
// receipt must provide the frozen canonical prefix, or repair remains pending.
func (s *Store) LoadRecovery(ctx context.Context, in state.Input) ([]*schema.Message, error) {
	if err := in.Identity.Scope.Validate(); err != nil {
		return nil, err
	}
	c, err := readConversation(ctx, s.DB, in.Identity.Scope)
	if err != nil {
		return nil, err
	}
	if c.Deleted {
		return nil, state.ErrConflict
	}
	s.mu.Lock()
	b := s.buffers[bufferKey(in)]
	var ms []*schema.Message
	if b != nil {
		snap, ok := b.Snapshots["assembled:0"]
		if ok && (snap.OriginalInput != nil || len(snap.Messages) > 0) {
			input := snap.CanonicalInput
			if input == nil {
				input = snap.OriginalInput
			}
			if input == nil {
				input = snap.Messages[len(snap.Messages)-1]
			}
			ms = state.CleanMessages([]*schema.Message{input})
			ms = append(ms, state.CleanMessages(b.Items)...)
		}
	}
	s.mu.Unlock()
	if len(ms) > 0 {
		if err = validRun(&c, in, false); err != nil {
			return nil, err
		}
		return ms, nil
	}
	var ext messageExt
	err = decode(s.DB.QueryRowContext(ctx, "SELECT ext FROM agent_message WHERE scope_key=? AND turn_id=? AND role='assistant'", in.Identity.Scope.Key(), in.Identity.TurnID), &ext)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, state.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if state.Hash(ext.Input) != state.Hash(in) {
		return nil, state.ErrConflict
	}
	rows, err := s.DB.QueryContext(ctx, "SELECT content FROM agent_message WHERE scope_key=? AND turn_id=? ORDER BY position", in.Identity.Scope.Key(), in.Identity.TurnID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var block struct {
			Items []*schema.Message `json:"items"`
		}
		if err = decode(rows, &block); err != nil {
			return nil, err
		}
		ms = append(ms, block.Items...)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return state.CleanMessages(ms), state.ValidateProtocol(ms)
}
