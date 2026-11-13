// Package agentstore provides a concurrency-safe development store. Data is volatile.
package agentstore

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/musi-ai/education/internal/platform/idgen"
	"sync"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/musi-ai/education/internal/agent/runtime/state"
	"github.com/musi-ai/education/internal/agent/runtime/storage"
)

type conversation struct {
	Branch   string
	Usage    state.ContextUsage
	Pending  map[string]*schema.Message
	Scope    state.Scope
	Next     int64
	Runs     map[string]state.Run
	Turns    map[string]state.Turn
	Active   string
	Snapshot map[string]state.Snapshot
	Items    map[string]map[int]*schema.Message
	Summary  state.Summary
}
type Store struct {
	mu            sync.Mutex
	conversations map[string]*conversation
	jobs          map[string]state.SummaryJob
	events        map[string]state.SealedEvent
}

var _ storage.Store = (*Store)(nil)

func New() *Store {
	return &Store{conversations: map[string]*conversation{}, jobs: map[string]state.SummaryJob{}, events: map[string]state.SealedEvent{}}
}
func clone[T any](v T) T {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	var c T
	if err = json.Unmarshal(b, &c); err != nil {
		panic(err)
	}
	return c
}
func (s *Store) Begin(ctx context.Context, in state.Input) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := in.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	k := in.Identity.Scope.Key()
	c := s.conversations[k]
	if c == nil {
		c = &conversation{Branch: idgen.New("b"), Scope: in.Identity.Scope, Runs: map[string]state.Run{}, Turns: map[string]state.Turn{}, Snapshot: map[string]state.Snapshot{}, Items: map[string]map[int]*schema.Message{}}
		s.conversations[k] = c
	}
	if _, ok := c.Runs[in.Identity.RunID]; ok {
		return state.ErrConflict
	}
	if _, ok := c.Turns[in.Identity.TurnID]; ok {
		return state.ErrConflict
	}
	if c.Active != "" {
		a := c.Runs[c.Active]
		if a.Status == "running" && (a.Input.Lease.Until.After(time.Now()) || in.Lease.Token <= a.Input.Lease.Token) {
			return state.ErrConflict
		}
	}
	cursor := int64(0)
	for _, r := range c.Runs {
		if r.Input.Identity.TurnID == in.Identity.TurnID {
			if r.Input.InputRef != in.InputRef || r.Input.BindingRef != in.BindingRef || r.Input.ConfigRef != in.ConfigRef || in.Lease.Token <= r.Input.Lease.Token {
				return state.ErrConflict
			}
			cursor = r.Cursor
		}
	}
	if cursor == 0 {
		c.Next++
		cursor = c.Next
	}
	c.Runs[in.Identity.RunID] = state.Run{Input: in, Cursor: cursor, Status: "running"}
	c.Active = in.Identity.RunID
	c.Items[in.Identity.RunID] = map[int]*schema.Message{}
	return nil
}
func (s *Store) active(in state.Input) (*conversation, state.Run, error) {
	c := s.conversations[in.Identity.Scope.Key()]
	if c == nil {
		return nil, state.Run{}, state.ErrNotFound
	}
	r, ok := c.Runs[in.Identity.RunID]
	if !ok || c.Active != in.Identity.RunID || r.Status != "running" || state.Hash(r.Input) != state.Hash(in) {
		return c, r, state.ErrConflict
	}
	return c, r, nil
}
func (s *Store) SaveSnapshot(ctx context.Context, in state.Input, v state.Snapshot) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c, r, err := s.active(in)
	if err != nil {
		return err
	}
	if !in.Lease.Until.After(time.Now()) {
		return state.ErrConflict
	}
	key := in.Identity.RunID + ":" + v.Key()
	if old, ok := c.Snapshot[key]; ok && state.Hash(old) != state.Hash(v) {
		return state.ErrConflict
	}
	c.Snapshot[key] = clone(v)
	if v.Usage.InputTokens > 0 {
		r.Usage = v.Usage
		c.Runs[in.Identity.RunID] = r
	}
	if v.Phase == "assembled" {
		r.Policy = v.Policy
		c.Runs[in.Identity.RunID] = r
	}
	return nil
}
func (s *Store) Append(ctx context.Context, in state.Input, n int, m *schema.Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c, _, err := s.active(in)
	if err != nil {
		return err
	}
	item := state.CleanMessages([]*schema.Message{m})
	if !in.Lease.Until.After(time.Now()) {
		return state.ErrConflict
	}
	if len(item) != 1 {
		return state.ErrProtocol
	}
	if old, ok := c.Items[in.Identity.RunID][n]; ok && state.Hash(old) != state.Hash(item[0]) {
		return state.ErrConflict
	}
	c.Items[in.Identity.RunID][n] = clone(item[0])
	return nil
}
func (s *Store) Finish(ctx context.Context, in state.Input, status, code string, receipt *state.Receipt, ms []*schema.Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c, r, err := s.active(in)
	if err != nil {
		if c != nil && r.Status == "succeeded" && receipt != nil && r.Receipt != nil && state.Hash(receipt) == state.Hash(r.Receipt) {
			return nil
		}
		return err
	}
	if status == "succeeded" {
		if receipt == nil || receipt.ResultID == "" || receipt.TurnID != in.Identity.TurnID || receipt.RunID != in.Identity.RunID {
			return state.ErrIncomplete
		}
		if err := state.ValidateProtocol(ms); err != nil {
			return err
		}
		branch := c.Branch
		end := state.MessageCursor{BranchID: branch, MessageID: state.MessageID(in.Identity.Scope, in.Identity.TurnID, "assistant"), Position: r.Cursor * 2}
		c.Usage = r.Usage
		c.Turns[in.Identity.TurnID] = state.Turn{End: end, Cursor: r.Cursor, TurnID: in.Identity.TurnID, RunID: in.Identity.RunID, Messages: clone(state.CleanMessages(ms))}
		id := state.Hash([]string{in.Identity.Scope.Key(), in.Identity.TurnID, "turn_sealed"})
		s.events[id] = state.SealedEvent{ID: id, Scope: in.Identity.Scope, Policy: r.Policy, TurnID: in.Identity.TurnID}
	}
	r.Status = status
	r.ErrorCode = code
	r.Receipt = clone(receipt)
	r.Messages = clone(state.CleanMessages(ms))
	c.Runs[in.Identity.RunID] = r
	c.Active = ""
	return nil
}
func (s *Store) View(ctx context.Context, scope state.Scope) (state.View, error) {
	if err := ctx.Err(); err != nil {
		return state.View{}, err
	}
	if err := scope.Validate(); err != nil {
		return state.View{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.conversations[scope.Key()]
	if c == nil {
		return state.View{}, nil
	}
	branch := c.Branch
	v := state.View{Summary: c.Summary, Usage: c.Usage, Token: state.ReadToken{ScopeKey: scope.Key(), BranchID: branch, HistoryEpoch: 1}}
	for n := int64(1); n <= c.Next; n++ {
		for _, t := range c.Turns {
			if t.Cursor == n {
				v.Turns = append(v.Turns, t)
				v.Token.Tip = t.End
				v.Token.WriteVersion++
			}
		}
	}
	return clone(v), nil
}
func (s *Store) EnqueueSummary(ctx context.Context, j state.SummaryJob) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.conversations[j.Scope.Key()]; !ok {
		return state.ErrNotFound
	}
	if _, ok := s.jobs[j.ID]; !ok {
		s.jobs[j.ID] = clone(j)
	}
	return nil
}
func (s *Store) ClaimSummary(ctx context.Context, worker string, ttl time.Duration) (state.SummaryJob, error) {
	if err := ctx.Err(); err != nil {
		return state.SummaryJob{}, err
	}
	if worker == "" || ttl <= 0 {
		return state.SummaryJob{}, state.ErrConflict
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for id, j := range s.jobs {
		if ((j.State == "pending" || j.State == "retry") && !j.NextAttempt.After(now)) || (j.State == "running" && !j.LeaseUntil.After(now)) {
			j.State = "running"
			j.Attempts++
			j.ClaimToken = fmt.Sprintf("%s:%s:%d", worker, id, j.Attempts)
			j.LeaseUntil = now.Add(ttl)
			s.jobs[id] = j
			return clone(j), nil
		}
	}
	return state.SummaryJob{}, state.ErrNotFound
}
func (s *Store) claimed(j state.SummaryJob) bool {
	v, ok := s.jobs[j.ID]
	return ok && v.State == "running" && v.ClaimToken == j.ClaimToken && v.LeaseUntil.After(time.Now())
}
func (s *Store) PublishSummary(ctx context.Context, j state.SummaryJob, v state.Summary) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.claimed(j) {
		return state.ErrConflict
	}
	c := s.conversations[j.Scope.Key()]
	if c == nil {
		return state.ErrNotFound
	}
	if c.Summary.Revision != j.BaseRevision {
		j.State = "obsolete"
		s.jobs[j.ID] = j
		return nil
	}
	if v.Revision != j.BaseRevision+1 || v.ThroughCursor != j.ThroughCursor || v.ThroughCursor <= c.Summary.ThroughCursor || v.Content == "" {
		return state.ErrConflict
	}
	c.Summary = clone(v)
	j.State = "succeeded"
	s.jobs[j.ID] = j
	return nil
}
func (s *Store) RetrySummary(ctx context.Context, j state.SummaryJob, reason string, next time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.claimed(j) {
		return state.ErrConflict
	}
	j.State = "retry"
	j.LastError = reason
	j.NextAttempt = next
	if j.Attempts >= 5 {
		j.State = "failed"
	}
	s.jobs[j.ID] = j
	return nil
}

func (s *Store) PersistUser(ctx context.Context, in state.Input, m *schema.Message) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c, _, e := s.active(in)
	if e != nil {
		return e
	}
	if m == nil || m.Role != schema.User {
		return state.ErrProtocol
	}
	if c.Pending == nil {
		c.Pending = map[string]*schema.Message{}
	}
	if old := c.Pending[in.Identity.TurnID]; old != nil && state.Hash(old) != state.Hash(m) {
		return state.ErrConflict
	}
	c.Pending[in.Identity.TurnID] = clone(m)
	return nil
}
