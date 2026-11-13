package agentstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/cloudwego/eino/schema"
	"github.com/musi-ai/education/internal/agent/runtime/state"
	"github.com/musi-ai/education/internal/agent/runtime/storage"
)

var _ storage.HistoryReader = (*Store)(nil)

func token(scope state.Scope, c conversation) state.ReadToken {
	return state.ReadToken{ScopeKey: scope.Key(), BranchID: c.Branch, Tip: c.Tip, WriteVersion: c.Version, HistoryEpoch: c.Epoch}
}

type segment struct {
	Branch string
	Max    int64
}
type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// lineage caps every ancestor at its fork point, independent of child positions.
func lineage(ctx context.Context, q queryer, scope state.Scope, t state.ReadToken) ([]segment, error) {
	var reversed []segment
	seen := map[string]bool{}
	branch, max := t.BranchID, t.Tip.Position
	for branch != "" {
		if seen[branch] || len(seen) >= 128 {
			return nil, state.ErrConflict
		}
		seen[branch] = true
		reversed = append(reversed, segment{branch, max})
		var parent sql.NullString
		var pos sql.NullInt64
		if err := q.QueryRowContext(ctx, "SELECT parent_branch_id,fork_parent_position FROM agent_branch WHERE scope_key=? AND branch_id=?", scope.Key(), branch).Scan(&parent, &pos); err != nil {
			return nil, err
		}
		branch = parent.String
		max = pos.Int64
	}
	for a, b := 0, len(reversed)-1; a < b; a, b = a+1, b-1 {
		reversed[a], reversed[b] = reversed[b], reversed[a]
	}
	return reversed, nil
}
func readConversation(ctx context.Context, q queryer, scope state.Scope) (conversation, error) {
	if err := scope.Validate(); err != nil {
		return conversation{}, err
	}
	var c conversation
	err := decode(q.QueryRowContext(ctx, "SELECT ext FROM agent_conversation WHERE scope_key=?", scope.Key()), &c)
	if errors.Is(err, sql.ErrNoRows) {
		err = state.ErrNotFound
	}
	return c, err
}
func checkToken(ctx context.Context, q queryer, scope state.Scope, t state.ReadToken) error {
	c, err := readConversation(ctx, q, scope)
	if err != nil {
		return err
	}
	if c.Deleted || t.ScopeKey != scope.Key() || c.Branch != t.BranchID || c.Epoch != t.HistoryEpoch || t.WriteVersion > c.Version {
		return state.ErrConflict
	}
	if t.Tip.Position == 0 {
		if t.Tip.MessageID != "" {
			return state.ErrConflict
		}
		return nil
	}
	var cursor state.MessageCursor
	var version int64
	err = q.QueryRowContext(ctx, "SELECT branch_id,message_id,position,prefix_hash,version FROM agent_message WHERE scope_key=? AND message_id=? AND role='assistant' AND status='SEALED'", scope.Key(), t.Tip.MessageID).Scan(&cursor.BranchID, &cursor.MessageID, &cursor.Position, &cursor.PrefixHash, &version)
	if err != nil {
		return err
	}
	if cursor != t.Tip || cursor.BranchID != t.BranchID || version > t.WriteVersion {
		return state.ErrConflict
	}
	return nil
}
func (s *Store) ReadHead(ctx context.Context, scope state.Scope) (state.ReadToken, error) {
	c, err := readConversation(ctx, s.DB, scope)
	if err != nil {
		return state.ReadToken{}, err
	}
	if c.Deleted {
		return state.ReadToken{}, state.ErrNotFound
	}
	return token(scope, c), nil
}

func loadPage(ctx context.Context, q queryer, scope state.Scope, t state.ReadToken, after *state.MessageCursor, limit int) (state.HistoryPage, error) {
	page := state.HistoryPage{Token: t}
	if limit < 1 || limit > 100 {
		return page, fmt.Errorf("agent store: page size must be 1..100")
	}
	if err := checkToken(ctx, q, scope, t); err != nil {
		return page, err
	}
	segments, err := lineage(ctx, q, scope, t)
	if err != nil {
		return page, err
	}
	start := after == nil
	for _, seg := range segments {
		lower := int64(0)
		if !start {
			if seg.Branch != after.BranchID {
				continue
			}
			if after.Position > seg.Max || after.Position%2 != 0 {
				return page, state.ErrConflict
			}
			var id, hash string
			if err = q.QueryRowContext(ctx, "SELECT message_id,prefix_hash FROM agent_message WHERE scope_key=? AND branch_id=? AND position=? AND role='assistant'", scope.Key(), seg.Branch, after.Position).Scan(&id, &hash); err != nil {
				return page, err
			}
			if id != after.MessageID || hash != after.PrefixHash {
				return page, state.ErrConflict
			}
			lower = after.Position
			start = true
		}
		// Fetch whole pairs via the indexed assistant position; never OFFSET or half turns.
		rows, e := q.QueryContext(ctx, `SELECT a.turn_id,a.run_id,a.turn_cursor,a.message_id,a.position,a.prefix_hash,u.content,a.content FROM agent_message a JOIN agent_message u ON u.scope_key=a.scope_key AND u.turn_id=a.turn_id AND u.role='user' WHERE a.scope_key=? AND a.branch_id=? AND a.role='assistant' AND a.status='SEALED' AND u.status='SEALED' AND a.position>? AND a.position<=? AND a.version<=? ORDER BY a.position LIMIT ?`, scope.Key(), seg.Branch, lower, seg.Max, t.WriteVersion, limit+1-len(page.Turns))
		if e != nil {
			return page, e
		}
		for rows.Next() {
			var turn state.Turn
			var user, assistant []byte
			turn.End.BranchID = seg.Branch
			if e = rows.Scan(&turn.TurnID, &turn.RunID, &turn.Cursor, &turn.End.MessageID, &turn.End.Position, &turn.End.PrefixHash, &user, &assistant); e != nil {
				break
			}
			for _, b := range [][]byte{user, assistant} {
				var block struct {
					Items []*schema.Message `json:"items"`
				}
				if e = json.Unmarshal(b, &block); e != nil {
					break
				}
				turn.Messages = append(turn.Messages, block.Items...)
			}
			if e != nil {
				break
			}
			if e = state.ValidateProtocol(turn.Messages); e != nil {
				break
			}
			page.Turns = append(page.Turns, turn)
		}
		if e == nil {
			e = rows.Err()
		}
		rows.Close()
		if e != nil {
			return page, e
		}
		if len(page.Turns) > limit {
			page.Turns = page.Turns[:limit]
			last := page.Turns[limit-1].End
			page.Next = &last
			return page, nil
		}
	}
	if !start {
		return page, state.ErrConflict
	}
	return page, nil
}
func (s *Store) LoadPage(ctx context.Context, scope state.Scope, t state.ReadToken, after *state.MessageCursor, limit int) (state.HistoryPage, error) {
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return state.HistoryPage{}, err
	}
	defer tx.Rollback()
	p, err := loadPage(ctx, tx, scope, t, after, limit)
	if err != nil {
		return p, err
	}
	return p, tx.Commit()
}

func (s *Store) view(ctx context.Context, scope state.Scope, uncovered bool) (state.View, error) {
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return state.View{}, err
	}
	defer tx.Rollback()
	c, err := readConversation(ctx, tx, scope)
	if errors.Is(err, state.ErrNotFound) {
		return state.View{}, nil
	}
	if err != nil {
		return state.View{}, err
	}
	if c.Deleted {
		return state.View{}, nil
	}
	v := state.View{Token: token(scope, c), Summary: c.Summary, Usage: c.Usage}
	v.Summary, err = readSummary(ctx, tx, scope, c)
	if err != nil {
		return v, err
	}
	var after *state.MessageCursor
	if uncovered && v.Summary.Content != "" {
		cur, e := cursorForTurn(ctx, tx, scope, v.Token, v.Summary.ThroughCursor)
		if e != nil {
			return v, e
		}
		after = &cur
	}
	for {
		p, e := loadPage(ctx, tx, scope, v.Token, after, 100)
		if e != nil {
			return v, e
		}
		v.Turns = append(v.Turns, p.Turns...)
		if p.Next == nil {
			break
		}
		after = p.Next
	}
	return v, tx.Commit()
}
func (s *Store) View(ctx context.Context, scope state.Scope) (state.View, error) {
	return s.view(ctx, scope, false)
}
func (s *Store) ContextView(ctx context.Context, scope state.Scope) (state.View, error) {
	return s.view(ctx, scope, true)
}

func cursorForTurn(ctx context.Context, q queryer, scope state.Scope, t state.ReadToken, turnCursor int64) (state.MessageCursor, error) {
	var c state.MessageCursor
	err := q.QueryRowContext(ctx, `SELECT m.branch_id,m.message_id,m.position,m.prefix_hash FROM agent_message m WHERE m.scope_key=? AND m.turn_cursor=? AND m.role='assistant'`, scope.Key(), turnCursor).Scan(&c.BranchID, &c.MessageID, &c.Position, &c.PrefixHash)
	if err != nil {
		return c, err
	}
	segments, err := lineage(ctx, q, scope, t)
	if err != nil {
		return c, err
	}
	for _, seg := range segments {
		if c.BranchID == seg.Branch && c.Position <= seg.Max {
			return c, nil
		}
	}
	return c, state.ErrConflict
}

func readSummary(ctx context.Context, q queryer, scope state.Scope, c conversation) (state.Summary, error) {
	if c.Summary.Content == "" && c.Summary.Revision == 0 {
		return state.Summary{}, nil
	}
	if c.Summary.CompactID != "" || c.Summary.Content == "" {
		v, e := loadCompact(ctx, q, scope, c)
		if e != nil {
			return state.Summary{}, e
		}
		c.Summary = v
	}
	if c.SummaryToken.BranchID != c.Branch || c.SummaryToken.HistoryEpoch != c.Epoch {
		return state.Summary{}, state.ErrConflict
	}
	actual, err := cursorForTurn(ctx, q, scope, token(scope, c), c.Summary.ThroughCursor)
	if err != nil {
		return state.Summary{}, err
	}
	if actual != c.SummaryThrough {
		return state.Summary{}, state.ErrConflict
	}
	return c.Summary, nil
}
