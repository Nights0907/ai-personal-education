package agentstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/cloudwego/eino/schema"
	"github.com/musi-ai/education/internal/agent/runtime/state"
)

// PersistUser records the raw accepted input before model/tool execution. It is
// not positioned in model history until Finish atomically seals the whole turn.
func (s *Store) PersistUser(ctx context.Context, in state.Input, m *schema.Message) error {
	clean := state.CleanMessages([]*schema.Message{m})
	if len(clean) != 1 || clean[0].Role != schema.User || len(clean[0].ToolCalls) > 0 || clean[0].ToolCallID != "" {
		return state.ErrProtocol
	}
	return s.transaction(ctx, in.Identity.Scope, func(tx *sql.Tx, c *conversation) error {
		if e := validRun(c, in, true); e != nil {
			return e
		}
		var content []byte
		var status string
		e := tx.QueryRowContext(ctx, "SELECT content,status FROM agent_message WHERE scope_key=? AND turn_id=? AND role='user'", in.Identity.Scope.Key(), in.Identity.TurnID).Scan(&content, &status)
		payload := struct {
			Items []*schema.Message `json:"items"`
		}{clean}
		if e == nil {
			var old struct {
				Items []*schema.Message `json:"items"`
			}
			if e = json.Unmarshal(content, &old); e != nil {
				return e
			}
			if status != "RECEIVED" || state.Hash(old.Items) != state.Hash(clean) {
				return state.ErrConflict
			}
			return nil
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		id := state.MessageID(in.Identity.Scope, in.Identity.TurnID, "user")
		text := jsonText(payload)
		hash := state.Hash(struct{ Schema, Payload string }{"eino-text-v1", text})
		_, e = tx.ExecContext(ctx, `INSERT INTO agent_message(scope_key,message_id,branch_id,position,version,role,status,content,ext,turn_id,run_id,turn_cursor,content_hash,prefix_hash,created_at) VALUES(?,?,?,NULL,0,'user','RECEIVED',?,?,?,'',0,?,'',UTC_TIMESTAMP(6))`, in.Identity.Scope.Key(), id, c.Branch, text, jsonText(messageExt{Input: in, OriginalInput: clean[0]}), in.Identity.TurnID, hash)
		return e
	})
}
