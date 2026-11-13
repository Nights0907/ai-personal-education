package agentstore

import (
	"context"
	"database/sql"
	"github.com/musi-ai/education/internal/agent/runtime/state"
	"github.com/musi-ai/education/internal/platform/idgen"
)

// Fork is an application-authorized infrastructure operation. No HTTP edit or
// regenerate endpoint is exposed. The application must reconcile business state.
// nil at starts an empty history; otherwise at must end a complete visible turn.
func (s *Store) Fork(ctx context.Context, scope state.Scope, expected state.ReadToken, at *state.MessageCursor, op string) (state.ReadToken, error) {
	var result state.ReadToken
	if op != "EDIT" && op != "REGENERATE" {
		return result, state.ErrConflict
	}
	err := s.transaction(ctx, scope, func(tx *sql.Tx, c *conversation) error {
		if token(scope, *c) != expected || c.Active != nil {
			return state.ErrConflict
		}
		parent, position, message, prefix := c.Branch, int64(0), "", ""
		if at == nil {
			segments, e := lineage(ctx, tx, scope, expected)
			if e != nil {
				return e
			}
			parent = segments[0].Branch
		}
		if at != nil {
			if at.Position < 2 || at.Position%2 != 0 {
				return state.ErrProtocol
			}
			segments, e := lineage(ctx, tx, scope, expected)
			if e != nil {
				return e
			}
			visible := false
			for _, seg := range segments {
				if seg.Branch == at.BranchID && at.Position <= seg.Max {
					visible = true
				}
			}
			if !visible {
				return state.ErrConflict
			}
			var found state.MessageCursor
			found.BranchID = at.BranchID
			e = tx.QueryRowContext(ctx, "SELECT message_id,position,prefix_hash FROM agent_message WHERE scope_key=? AND branch_id=? AND position=? AND role='assistant'", scope.Key(), at.BranchID, at.Position).Scan(&found.MessageID, &found.Position, &found.PrefixHash)
			if e != nil {
				return e
			}
			if found != *at {
				return state.ErrConflict
			}
			parent, position, message, prefix = at.BranchID, at.Position, at.MessageID, at.PrefixHash
		}
		branch := idgen.New("b")
		c.Version++
		c.Epoch++
		_, e := tx.ExecContext(ctx, `INSERT INTO agent_branch(scope_key,branch_id,parent_branch_id,fork_parent_message_id,fork_parent_position,fork_parent_version,created_version,op,created_at) VALUES(?,?,?,?,?,?,?,?,UTC_TIMESTAMP(6))`, scope.Key(), branch, parent, nullable(message), position, expected.WriteVersion, c.Version, op)
		if e != nil {
			return e
		}
		c.Branch = branch
		c.Tip = state.MessageCursor{BranchID: branch, PrefixHash: prefix}
		c.Usage = state.ContextUsage{}
		c.Summary = state.Summary{}
		c.SummaryToken = state.ReadToken{}
		c.SummaryThrough = state.MessageCursor{}
		result = token(scope, *c)
		return nil
	})
	return result, err
}
