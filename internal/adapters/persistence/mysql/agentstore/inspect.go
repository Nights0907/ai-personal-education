package agentstore

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/musi-ai/education/internal/agent/runtime/state"
	"github.com/musi-ai/education/internal/query"
	"strings"
)

type inspectCursor struct {
	Scope, Snapshot string
	Offset          int
}

// Inspect includes pending messages and inactive branches. A changing snapshot
// rejects pagination rather than silently skipping or duplicating messages.
func (q *Queries) Inspect(ctx context.Context, student, conversationID, cursor string, limit int) (out query.ConversationHistory, err error) {
	if !query.ValidID(student) || !query.ValidID(conversationID) || limit < 1 || limit > 100 {
		return out, query.ErrInvalid
	}
	scope := state.Scope{Namespace: q.Namespace, SubjectID: student, ConversationID: conversationID}
	tx, err := q.Store.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	c, err := readConversation(ctx, tx, scope)
	if errors.Is(err, state.ErrNotFound) || c.Deleted {
		return out, query.ErrNotFound
	}
	if err != nil {
		return out, err
	}
	out.ConversationID = conversationID
	out.BranchID = c.Branch
	out.Messages = []query.StoredMessage{}
	out.RecentCompacts = []state.Summary{}
	if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM agent_message WHERE scope_key=?", scope.Key()).Scan(&out.Total); err != nil {
		return out, err
	}
	snapshot := state.Hash([]any{c.Version, c.Epoch, c.Branch, c.Summary.Revision, out.Total})
	page := inspectCursor{Scope: scope.Key(), Snapshot: snapshot}
	if cursor != "" {
		b, e := base64.RawURLEncoding.DecodeString(cursor)
		if e != nil || len(b) > 2048 || json.Unmarshal(b, &page) != nil || page.Offset < 0 || page.Scope != scope.Key() {
			return out, query.ErrInvalid
		}
		if page.Snapshot != snapshot {
			return out, query.ErrConflict
		}
		if page.Offset > out.Total {
			return out, query.ErrInvalid
		}
	}
	segments, err := lineage(ctx, tx, scope, token(scope, c))
	if err != nil {
		return out, err
	}
	out.CurrentSummary, err = readSummary(ctx, tx, scope, c)
	if err != nil {
		return out, err
	}
	var chainPredicates []string
	chainArgs := []any{out.CurrentSummary.ThroughCursor, scope.Key()}
	for _, seg := range segments {
		chainPredicates = append(chainPredicates, "(branch_id=? AND position<=?)")
		chainArgs = append(chainArgs, seg.Branch, seg.Max)
	}
	if len(chainPredicates) > 0 {
		if err = tx.QueryRowContext(ctx, "SELECT COUNT(*),COALESCE(SUM(turn_cursor<=?),0) FROM agent_message WHERE scope_key=? AND status='SEALED' AND ("+strings.Join(chainPredicates, " OR ")+")", chainArgs...).Scan(&out.ActiveChainMessages, &out.CompactedMessages); err != nil {
			return out, err
		}
	}
	out.ContextMessages = out.ActiveChainMessages - out.CompactedMessages
	out.Policy = state.DefaultPolicy()
	out.PolicySource = "default"
	if c.HistoryPolicy.Version != "" {
		out.Policy = c.HistoryPolicy
		out.PolicySource = "last_successful_turn"
	}
	out.TriggerTokens = out.Policy.TriggerTokens()
	out.Usage = c.Usage
	rows, err := tx.QueryContext(ctx, `SELECT message_id,branch_id,turn_id,role,status,position,turn_cursor,content,created_at,ext,content_hash,prefix_hash FROM agent_message WHERE scope_key=? ORDER BY created_at,message_id LIMIT ? OFFSET ?`, scope.Key(), limit, page.Offset)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var m query.StoredMessage
		var meta messageExt
		if err = rows.Scan(&m.ID, &m.BranchID, &m.TurnID, &m.Role, &m.Status, &m.Position, &m.TurnCursor, &m.Content, &m.CreatedAt, newJSON(&meta), &m.ContentHash, &m.PrefixHash); err != nil {
			rows.Close()
			return out, err
		}
		m.SystemPrompts = meta.SystemPrompts
		m.DynamicContext = meta.DynamicContext
		if meta.OriginalInput != nil {
			m.OriginalInput, _ = json.Marshal(meta.OriginalInput)
		} else if m.Role == "user" {
			var block struct {
				Items []json.RawMessage `json:"items"`
			}
			if err = json.Unmarshal(m.Content, &block); err != nil {
				rows.Close()
				return out, err
			}
			if len(block.Items) > 0 {
				m.OriginalInput = block.Items[0]
			}
		}
		for _, seg := range segments {
			if m.Status == "SEALED" && m.Position != nil && m.BranchID == seg.Branch && *m.Position <= seg.Max {
				m.InActiveChain = true
			}
		}
		m.CoveredByCompact = m.InActiveChain && m.TurnCursor <= out.CurrentSummary.ThroughCursor
		out.Messages = append(out.Messages, m)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	page.Offset += len(out.Messages)
	if page.Offset < out.Total {
		b, _ := json.Marshal(page)
		out.NextCursor = base64.RawURLEncoding.EncodeToString(b)
	}
	if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM agent_memory_compact WHERE scope_key=?", scope.Key()).Scan(&out.CompactVersions); err != nil {
		return out, err
	}
	rows, err = tx.QueryContext(ctx, "SELECT summary_json FROM agent_memory_compact WHERE scope_key=? ORDER BY created_at DESC,compact_id DESC LIMIT 20", scope.Key())
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var s state.Summary
		if err = decode(rows, &s); err != nil {
			rows.Close()
			return out, err
		}
		out.RecentCompacts = append(out.RecentCompacts, s)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	return out, tx.Commit()
}
