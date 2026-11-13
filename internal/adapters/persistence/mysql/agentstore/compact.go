package agentstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/musi-ai/education/internal/agent/runtime/state"
)

func persistCompact(ctx context.Context, tx *sql.Tx, c conversation, v state.Summary, p state.Policy) (state.Summary, error) {
	if v.CompactID == "" {
		v.CompactID = state.Hash([]any{c.Scope.Key(), c.Branch, c.Epoch, v.Revision, v.SourceHash, c.SummaryThrough.PrefixHash})
	}
	content := v.Structured
	if len(content) == 0 {
		content = []byte(jsonText(map[string]string{"overview": v.Content}))
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO agent_memory_compact(compact_id,scope_key,branch_id,history_epoch,revision,through_turn_cursor,through_message_id,through_position,through_prefix_hash,content,overview,summary_json,source_token,policy_json,model_ref,source_hash,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,UTC_TIMESTAMP(6)) ON DUPLICATE KEY UPDATE compact_id=compact_id`, v.CompactID, c.Scope.Key(), c.Branch, c.Epoch, v.Revision, v.ThroughCursor, c.SummaryThrough.MessageID, c.SummaryThrough.Position, c.SummaryThrough.PrefixHash, string(content), v.Content, jsonText(v), jsonText(c.SummaryToken), jsonText(p), v.ModelRef, v.SourceHash)
	if err != nil {
		return v, err
	}
	var stored state.Summary
	if err = decode(tx.QueryRowContext(ctx, "SELECT summary_json FROM agent_memory_compact WHERE scope_key=? AND branch_id=? AND history_epoch=? AND revision=?", c.Scope.Key(), c.Branch, c.Epoch, v.Revision), &stored); err != nil {
		return v, err
	}
	// MySQL JSON normalizes object key order, including RawMessage fields.
	var storedJSON, proposedJSON any
	if json.Unmarshal([]byte(jsonText(stored)), &storedJSON) != nil || json.Unmarshal([]byte(jsonText(v)), &proposedJSON) != nil || state.Hash(storedJSON) != state.Hash(proposedJSON) {
		return v, state.ErrConflict
	}
	return v, nil
}
func loadCompact(ctx context.Context, q queryer, scope state.Scope, c conversation) (state.Summary, error) {
	var v state.Summary
	err := decode(q.QueryRowContext(ctx, "SELECT summary_json FROM agent_memory_compact WHERE scope_key=? AND branch_id=? AND history_epoch=? AND revision=?", scope.Key(), c.Branch, c.Epoch, c.Summary.Revision), &v)
	if errors.Is(err, sql.ErrNoRows) {
		return v, state.ErrConflict
	}
	if err == nil && c.Summary.CompactID != "" && v.CompactID != c.Summary.CompactID {
		return v, state.ErrConflict
	}
	return v, err
}

// Migrate old inline summaries without touching any message or its hashes.
func (s *Store) migrateInlineCompacts(ctx context.Context) error {
	for {
		rows, err := s.DB.QueryContext(ctx, `SELECT ext FROM agent_conversation WHERE status='ACTIVE' AND JSON_UNQUOTE(JSON_EXTRACT(ext,'$.Summary.Content'))<>'' LIMIT 100`)
		if err != nil {
			return err
		}
		scopes := []state.Scope{}
		for rows.Next() {
			var c conversation
			if err = decode(rows, &c); err != nil {
				rows.Close()
				return err
			}
			scopes = append(scopes, c.Scope)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(scopes) == 0 {
			return nil
		}
		for _, scope := range scopes {
			if err = s.transaction(ctx, scope, func(tx *sql.Tx, c *conversation) error {
				if c.Summary.Content == "" {
					return nil
				}
				v, e := persistCompact(ctx, tx, *c, c.Summary, state.DefaultPolicy())
				if e != nil {
					return e
				}
				c.Summary = v
				return nil
			}); err != nil {
				return err
			}
		}
	}
}
