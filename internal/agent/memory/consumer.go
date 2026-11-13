package memory

import (
	"context"
	"errors"
	"github.com/musi-ai/education/internal/agent/runtime/event"
	"github.com/musi-ai/education/internal/agent/runtime/state"
)

// Consume is the single subscriber contract for broker delivery and the local
// durable coalescing queue. It enqueues work; it never calls a summary model.
func (m Manager) Consume(ctx context.Context, e event.Event) error {
	if e.Type != event.TypeTurnCompleted {
		return nil
	}
	p, ok := e.Payload.(*event.TurnCompleted)
	if !ok || p == nil {
		return errors.New("memory: invalid turn event")
	}
	if p.Status != "completed" {
		return nil
	}
	scope := state.Scope{Namespace: p.Tenant, SubjectID: p.SubjectID, ConversationID: p.ConversationID}
	if err := scope.Validate(); err != nil {
		return err
	}
	_, err := m.Schedule(ctx, scope, p.Policy, false)
	return err
}
