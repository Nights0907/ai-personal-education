package state

import "time"

// InputTokens covers the full actual model request, including system and tools.
// Source explicitly distinguishes provider-reported usage from local estimation.
type ContextUsage struct {
	InputTokens          int       `json:"input_tokens"`
	EstimatedInputTokens int       `json:"estimated_input_tokens"`
	CompletionTokens     int       `json:"completion_tokens"`
	Source               string    `json:"source"`
	ModelRef             string    `json:"model_ref"`
	TurnID               string    `json:"turn_id"`
	SummaryRevision      int64     `json:"summary_revision"`
	RecordedAt           time.Time `json:"recorded_at"`
}

func (p Policy) TriggerTokens() int { return (p.InputLimit()*p.TriggerPercent + 99) / 100 }
