// Package event contains out-bound contracts, independent of brokers or storage.
package event

import (
	"context"
	"github.com/cloudwego/eino/schema"
	"github.com/musi-ai/education/internal/agent/runtime/state"
	"time"
)

type Type string

const TypeTurnCompleted Type = "turn_completed"

type Identity struct {
	AgentName, ConversationID, TurnID, RunID, BranchID, Tenant, SubjectID string
	Labels                                                                map[string]string
}
type Item struct {
	Index               int
	MessageID           string
	Seq                 int64
	Type                string
	Role                schema.RoleType
	Content, ToolCallID string
	ToolCalls           []schema.ToolCall
}
type TurnCompleted struct {
	Policy state.Policy
	Identity
	Status                  string
	StartedAt, EndedAt      time.Time
	TailSeq                 int64
	ItemCount               int
	Items                   []Item
	Error, Preamble, Memory string
}
type Event struct {
	Type    Type
	Payload any
}
type Producer interface {
	Emit(context.Context, Event) error
}
