package agentstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/cloudwego/eino/schema"
	"github.com/musi-ai/education/internal/agent/runtime/state"
)

func (s *Store) GetTurn(ctx context.Context, scope state.Scope, id string) (state.Turn, error) {
	if e := scope.Validate(); e != nil {
		return state.Turn{}, e
	}
	var t state.Turn
	var user, assistant []byte
	e := s.DB.QueryRowContext(ctx, `SELECT a.turn_id,a.run_id,a.turn_cursor,a.branch_id,a.message_id,a.position,a.prefix_hash,u.content,a.content FROM agent_message a JOIN agent_message u ON u.scope_key=a.scope_key AND u.turn_id=a.turn_id AND u.role='user' JOIN agent_conversation c ON c.scope_key=a.scope_key WHERE a.scope_key=? AND a.turn_id=? AND a.role='assistant' AND a.status='SEALED' AND u.status='SEALED' AND c.status='ACTIVE'`, scope.Key(), id).Scan(&t.TurnID, &t.RunID, &t.Cursor, &t.End.BranchID, &t.End.MessageID, &t.End.Position, &t.End.PrefixHash, &user, &assistant)
	if errors.Is(e, sql.ErrNoRows) {
		return t, state.ErrNotFound
	}
	if e != nil {
		return t, e
	}
	for _, b := range [][]byte{user, assistant} {
		var v struct {
			Items []*schema.Message `json:"items"`
		}
		if e = json.Unmarshal(b, &v); e != nil {
			return t, e
		}
		t.Messages = append(t.Messages, v.Items...)
	}
	return t, state.ValidateProtocol(t.Messages)
}
