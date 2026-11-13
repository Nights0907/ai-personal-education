package summary

import "encoding/json"

const DialogueSummaryVersion = "math-summary-v1"

// SummaryEvidence is a description of what was said/done, never a mastery score.
type SummaryEvidence struct {
	Text          string   `json:"text"`
	SourceTurnIDs []string `json:"source_turn_ids"`
}
type DialogueSummary struct {
	SchemaVersion     string            `json:"schema_version"`
	Overview          string            `json:"overview"`
	StudentStatements []SummaryEvidence `json:"student_statements"`
	TeachingActions   []SummaryEvidence `json:"teaching_actions"`
	PendingQuestions  []SummaryEvidence `json:"pending_questions"`
	Uncertainties     []SummaryEvidence `json:"uncertainties"`
}

func NormalizeSummary(s DialogueSummary) DialogueSummary {
	if s.StudentStatements == nil {
		s.StudentStatements = []SummaryEvidence{}
	}
	if s.TeachingActions == nil {
		s.TeachingActions = []SummaryEvidence{}
	}
	if s.PendingQuestions == nil {
		s.PendingQuestions = []SummaryEvidence{}
	}
	if s.Uncertainties == nil {
		s.Uncertainties = []SummaryEvidence{}
	}
	return s
}

func Encode(s DialogueSummary) json.RawMessage { b, _ := json.Marshal(s); return b }
