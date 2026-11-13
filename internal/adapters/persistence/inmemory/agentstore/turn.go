package agentstore

import (
	"context"
	"github.com/musi-ai/education/internal/agent/runtime/state"
)

func (s *Store) GetTurn(ctx context.Context, scope state.Scope, id string) (state.Turn, error) {
	if e := ctx.Err(); e != nil {
		return state.Turn{}, e
	}
	if e := scope.Validate(); e != nil {
		return state.Turn{}, e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.conversations[scope.Key()]
	if c == nil {
		return state.Turn{}, state.ErrNotFound
	}
	t, ok := c.Turns[id]
	if !ok {
		return t, state.ErrNotFound
	}
	return clone(t), nil
}
