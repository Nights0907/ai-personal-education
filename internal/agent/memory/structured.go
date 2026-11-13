package memory

import (
	"encoding/json"
	"errors"
	teachingsummary "github.com/musi-ai/education/internal/modules/teaching/summary"
	"strings"
)

func evidenceGroups(s teachingsummary.DialogueSummary) [][]teachingsummary.SummaryEvidence {
	return [][]teachingsummary.SummaryEvidence{s.StudentStatements, s.TeachingActions, s.PendingQuestions, s.Uncertainties}
}

// Source validation prevents invented/cross-conversation evidence IDs. Semantic
// faithfulness still requires evaluation; valid references alone do not prove it.
func ValidateSummary(s teachingsummary.DialogueSummary, source SummarySource) error {
	if s.SchemaVersion != teachingsummary.DialogueSummaryVersion || strings.TrimSpace(s.Overview) == "" {
		return errors.New("runtime: invalid structured summary schema or empty overview")
	}
	allowed := map[string]bool{}
	for _, turn := range source.Turns {
		allowed[turn.TurnID] = true
	}
	if len(source.Previous.Structured) > 0 {
		var previous teachingsummary.DialogueSummary
		if e := json.Unmarshal(source.Previous.Structured, &previous); e != nil {
			return e
		}
		old := &previous
		for _, group := range evidenceGroups(*old) {
			for _, e := range group {
				for _, id := range e.SourceTurnIDs {
					allowed[id] = true
				}
			}
		}
	}
	for _, group := range evidenceGroups(s) {
		if len(group) > 20 {
			return errors.New("runtime: too many structured summary entries")
		}
		for _, e := range group {
			if strings.TrimSpace(e.Text) == "" || len(e.SourceTurnIDs) == 0 || len(e.SourceTurnIDs) > 20 {
				return errors.New("runtime: summary evidence requires text and bounded source references")
			}
			for _, id := range e.SourceTurnIDs {
				if id == "" || !allowed[id] {
					return errors.New("runtime: summary evidence source outside covered history")
				}
			}
		}
	}
	return nil
}
