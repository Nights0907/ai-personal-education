package agentstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/musi-ai/education/internal/agent/runtime/state"
	"sort"
	"time"
)

// Memory work is bounded conversation metadata, not an unbounded audit/queue
// history hidden in ext. At most eight pending/running descriptors are retained.
const maxMemoryJobs = 8

func terminalJob(j state.SummaryJob) bool {
	return j.State == "succeeded" || j.State == "obsolete" || j.State == "failed"
}
func (s *Store) EnqueueSummary(ctx context.Context, j state.SummaryJob) error {
	return s.transaction(ctx, j.Scope, func(tx *sql.Tx, c *conversation) error {
		if c.Jobs == nil {
			c.Jobs = map[string]state.SummaryJob{}
		}
		if _, ok := c.Jobs[j.ID]; ok {
			return nil
		}
		if j.ReadToken.BranchID == "" {
			j.ReadToken = token(j.Scope, *c)
		}
		if j.ReadToken.BranchID != c.Branch || j.ReadToken.HistoryEpoch != c.Epoch {
			return state.ErrConflict
		}
		if err := checkToken(ctx, tx, j.Scope, j.ReadToken); err != nil {
			return err
		}
		through, err := cursorForTurn(ctx, tx, j.Scope, j.ReadToken, j.ThroughCursor)
		if err != nil {
			return err
		}
		j.Through = through
		for id, old := range c.Jobs {
			if terminalJob(old) {
				delete(c.Jobs, id)
			}
		}
		if len(c.Jobs) >= maxMemoryJobs {
			return fmt.Errorf("agent store: pending memory work limit reached")
		}
		j.State = "pending"
		j.NextAttempt = time.Now().UTC()
		c.Jobs[j.ID] = j
		return nil
	})
}
func (s *Store) ClaimSummary(ctx context.Context, worker string, ttl time.Duration) (state.SummaryJob, error) {
	var result state.SummaryJob
	if worker == "" || ttl <= 0 {
		return result, state.ErrConflict
	}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	var c conversation
	err = decode(tx.QueryRowContext(ctx, "SELECT ext FROM agent_conversation WHERE memory_due_at<=UTC_TIMESTAMP(6) AND status='ACTIVE' ORDER BY memory_due_at,scope_key LIMIT 1 FOR UPDATE"), &c)
	if errors.Is(err, sql.ErrNoRows) {
		return result, state.ErrNotFound
	}
	if err != nil {
		return result, err
	}
	ids := make([]string, 0, len(c.Jobs))
	for id := range c.Jobs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		j := c.Jobs[id]
		due := (j.State == "pending" || j.State == "retry") && !j.NextAttempt.After(time.Now())
		due = due || (j.State == "running" && !j.LeaseUntil.After(time.Now()))
		if !due {
			continue
		}
		j.State = "running"
		j.Attempts++
		j.ClaimToken = fmt.Sprintf("%s:%s:%d", worker, j.ID, j.Attempts)
		j.LeaseUntil = time.Now().UTC().Add(ttl)
		c.Jobs[id] = j
		result = j
		break
	}
	if result.ID == "" {
		return result, state.ErrNotFound
	}
	if err = writeConversation(ctx, tx, c); err != nil {
		return result, err
	}
	return result, tx.Commit()
}
func checkClaim(c *conversation, j state.SummaryJob) error {
	old, ok := c.Jobs[j.ID]
	if !ok || old.State != "running" || state.Hash(old) != state.Hash(j) || !old.LeaseUntil.After(time.Now()) {
		return state.ErrConflict
	}
	return nil
}
func (s *Store) PublishSummary(ctx context.Context, j state.SummaryJob, v state.Summary) error {
	return s.transaction(ctx, j.Scope, func(tx *sql.Tx, c *conversation) error {
		if err := checkClaim(c, j); err != nil {
			return err
		}
		if c.Summary.Revision != j.BaseRevision || c.Branch != j.ReadToken.BranchID || c.Epoch != j.ReadToken.HistoryEpoch {
			j.State = "obsolete"
			c.Jobs[j.ID] = j
			return nil
		}
		if v.Revision != j.BaseRevision+1 || v.ThroughCursor != j.ThroughCursor || v.ThroughCursor <= c.Summary.ThroughCursor || v.Content == "" || v.PolicyVersion != j.Policy.Version || v.ModelRef != j.ModelRef {
			return state.ErrConflict
		}
		cur, err := cursorForTurn(ctx, tx, j.Scope, token(j.Scope, *c), j.ThroughCursor)
		if err != nil {
			return err
		}
		if cur != j.Through {
			return state.ErrConflict
		}
		c.SummaryToken = j.ReadToken
		c.SummaryThrough = cur
		stored, e := persistCompact(ctx, tx, *c, v, j.Policy)
		if e != nil {
			return e
		}
		c.Summary = stored
		j.State = "succeeded"
		c.Jobs[j.ID] = j
		return nil
	})
}
func (s *Store) RetrySummary(ctx context.Context, j state.SummaryJob, reason string, next time.Time) error {
	return s.transaction(ctx, j.Scope, func(_ *sql.Tx, c *conversation) error {
		if err := checkClaim(c, j); err != nil {
			return err
		}
		j.State = "retry"
		if j.Attempts >= 5 {
			j.State = "failed"
		}
		j.LastError = reason
		j.NextAttempt = next
		c.Jobs[j.ID] = j
		return nil
	})
}

// Consecutive seals coalesce into one durable wake-up for this conversation.
// Scheduling always reads the latest complete prefix. Ack/defer use the event ID
// so an older dispatcher cannot erase a newer seal notification.
func (s *Store) NextSealed(ctx context.Context) (state.SealedEvent, error) {
	var c conversation
	err := decode(s.DB.QueryRowContext(ctx, "SELECT ext FROM agent_conversation WHERE event_due_at<=UTC_TIMESTAMP(6) AND status='ACTIVE' ORDER BY event_due_at,scope_key LIMIT 1"), &c)
	if errors.Is(err, sql.ErrNoRows) {
		return state.SealedEvent{}, state.ErrNotFound
	}
	if err != nil {
		return state.SealedEvent{}, err
	}
	if c.Event == nil {
		return state.SealedEvent{}, state.ErrNotFound
	}
	return *c.Event, nil
}
func (s *Store) AcknowledgeSealed(ctx context.Context, e state.SealedEvent) error {
	return s.transaction(ctx, e.Scope, func(_ *sql.Tx, c *conversation) error {
		if c.Event != nil && c.Event.ID == e.ID {
			c.Event = nil
		}
		return nil
	})
}
func (s *Store) DeferSealed(ctx context.Context, e state.SealedEvent, reason string) error {
	return s.transaction(ctx, e.Scope, func(_ *sql.Tx, c *conversation) error {
		if c.Event == nil || c.Event.ID != e.ID || c.Event.Attempts != e.Attempts {
			return nil
		}
		e.Attempts++
		e.NextAttempt = time.Now().UTC().Add(30 * time.Second)
		e.LastError = reason
		e.Failed = e.Attempts >= 5
		c.Event = &e
		return nil
	})
}
