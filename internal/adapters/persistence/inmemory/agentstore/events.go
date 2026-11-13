package agentstore

import (
	"context"
	"github.com/musi-ai/education/internal/agent/runtime/state"
	"time"
)

func (s *Store) Forget(ctx context.Context, scope state.Scope) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	if e := scope.Validate(); e != nil {
		return e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.conversations, scope.Key())
	for id, j := range s.jobs {
		if j.Scope == scope {
			delete(s.jobs, id)
		}
	}
	for id, e := range s.events {
		if e.Scope == scope {
			delete(s.events, id)
		}
	}
	return nil
}
func (s *Store) NextSealed(ctx context.Context) (state.SealedEvent, error) {
	if e := ctx.Err(); e != nil {
		return state.SealedEvent{}, e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.events {
		if !e.Failed && !e.NextAttempt.After(time.Now()) {
			return e, nil
		}
	}
	return state.SealedEvent{}, state.ErrNotFound
}
func (s *Store) DeferSealed(ctx context.Context, event state.SealedEvent, reason string) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.events[event.ID]
	if !ok {
		return nil
	}
	if old.Scope != event.Scope {
		return state.ErrConflict
	}
	if old.Attempts != event.Attempts {
		return nil
	}
	event.Attempts++
	event.LastError = reason
	event.NextAttempt = time.Now().UTC().Add(30 * time.Second)
	event.Failed = event.Attempts >= 5
	s.events[event.ID] = event
	return nil
}
func (s *Store) AcknowledgeSealed(ctx context.Context, event state.SealedEvent) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.events[event.ID]; ok && e.Scope != event.Scope {
		return state.ErrConflict
	}
	delete(s.events, event.ID)
	return nil
}
