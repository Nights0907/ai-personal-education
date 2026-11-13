package query

import (
	"context"
	"encoding/json"
	"github.com/musi-ai/education/internal/agent/runtime/state"
	"time"
)

// HistoryReader is a diagnostic read of durable messages, independent of the
// compressed model context. The student identity is supplied by the server.
type HistoryReader interface {
	Inspect(context.Context, string, string, string, int) (ConversationHistory, error)
}
type StoredMessage struct {
	DynamicContext   string          `json:"dynamic_context,omitempty"`
	SystemPrompts    []string        `json:"system_prompts,omitempty"`
	ID               string          `json:"message_id"`
	BranchID         string          `json:"branch_id"`
	TurnID           string          `json:"turn_id"`
	Role             string          `json:"role"`
	Status           string          `json:"status"`
	Position         *int64          `json:"position"`
	TurnCursor       int64           `json:"turn_cursor"`
	Content          json.RawMessage `json:"content"`
	OriginalInput    json.RawMessage `json:"original_input,omitempty"`
	ContentHash      string          `json:"content_hash"`
	PrefixHash       string          `json:"prefix_hash"`
	CreatedAt        time.Time       `json:"created_at"`
	InActiveChain    bool            `json:"in_active_chain"`
	CoveredByCompact bool            `json:"covered_by_current_compact"`
}
type ConversationHistory struct {
	PolicySource        string             `json:"policy_source"`
	ConversationID      string             `json:"conversation_id"`
	BranchID            string             `json:"current_branch_id"`
	Messages            []StoredMessage    `json:"messages"`
	Total               int                `json:"total_messages"`
	ActiveChainMessages int                `json:"active_chain_messages"`
	CompactedMessages   int                `json:"compacted_messages"`
	ContextMessages     int                `json:"context_messages"`
	NextCursor          string             `json:"next_cursor,omitempty"`
	CurrentSummary      state.Summary      `json:"current_summary"`
	RecentCompacts      []state.Summary    `json:"recent_compacts"`
	CompactVersions     int                `json:"compact_versions"`
	Usage               state.ContextUsage `json:"last_model_usage"`
	Policy              state.Policy       `json:"policy"`
	TriggerTokens       int                `json:"trigger_tokens"`
}
