// Package storage defines persistence and the atomic boundaries for a turn.
package storage

import (
	"context"
	"github.com/cloudwego/eino/schema"
	"github.com/musi-ai/education/internal/agent/runtime/state"
	"time"
)

// Implementations isolate every lookup by the full scope, fence writes, and never
// make incomplete/failed runs available through View. Seal also enqueues turn_sealed.
type Storage interface {
	GetTurn(context.Context, state.Scope, string) (state.Turn, error)
	PersistUser(context.Context, state.Input, *schema.Message) error
	Begin(context.Context, state.Input) error
	SaveSnapshot(context.Context, state.Input, state.Snapshot) error
	Append(context.Context, state.Input, int, *schema.Message) error
	Finish(context.Context, state.Input, string, string, *state.Receipt, []*schema.Message) error
	View(context.Context, state.Scope) (state.View, error)
	Forget(context.Context, state.Scope) error
}

// SummaryQueue is owned by the asynchronous memory consumer.
type SummaryQueue interface {
	NextSealed(context.Context) (state.SealedEvent, error)
	AcknowledgeSealed(context.Context, state.SealedEvent) error
	DeferSealed(context.Context, state.SealedEvent, string) error
	EnqueueSummary(context.Context, state.SummaryJob) error
	ClaimSummary(context.Context, string, time.Duration) (state.SummaryJob, error)
	PublishSummary(context.Context, state.SummaryJob, state.Summary) error
	RetrySummary(context.Context, state.SummaryJob, string, time.Time) error
}

// Store is the compatibility composition for existing adapters.
type Store interface {
	Storage
	SummaryQueue
}

type Message = schema.Message
type Compact = state.Summary
type ConversationState = state.View

// HistoryReader exposes bounded, complete-turn pages pinned to a frozen view.
// A nil after starts at the beginning. Next is the last returned assistant block.
type HistoryReader interface {
	ReadHead(context.Context, state.Scope) (state.ReadToken, error)
	LoadPage(context.Context, state.Scope, state.ReadToken, *state.MessageCursor, int) (state.HistoryPage, error)
	ContextView(context.Context, state.Scope) (state.View, error)
}

// RecoveryReader returns the original user input and ordered successful audit
// prefix. CompletionResolver supplies the atomically frozen terminal tail.
type RecoveryReader interface {
	LoadRecovery(context.Context, state.Input) ([]*schema.Message, error)
}
