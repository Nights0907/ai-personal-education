package bailian

import (
	"context"
	"errors"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/musi-ai/education/internal/agent/memory"
	"github.com/musi-ai/education/internal/agent/runtime/state"
)

type summaryModel struct{ output string }

func (s summaryModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return schema.AssistantMessage(s.output, nil), nil
}
func (summaryModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("not used")
}

func TestStructuredSummaryContract(t *testing.T) {
	source := memory.SummarySource{Turns: []state.Turn{{TurnID: "source-1"}}}
	valid := `{"schema_version":"math-summary-v1","overview":"学生仍在追问定义域","pending_questions":[{"text":"x=0 是否允许","source_turn_ids":["source-1"]}]}`
	result, err := (Summarizer{Model: summaryModel{valid}}).SummarizeStructured(context.Background(), source, 2048)
	if err != nil || result.PendingQuestions[0].SourceTurnIDs[0] != "source-1" {
		t.Fatal(result, err)
	}
	for _, bad := range []string{
		"plain summary", valid + ` {}`, `{"schema_version":"other","overview":"x"}`,
		`{"schema_version":"math-summary-v1","overview":"x","mastery":100}`,
		`{"schema_version":"math-summary-v1","overview":"x","student_statements":[{"text":"unrelated","source_turn_ids":["foreign"]}]}`,
	} {
		if _, err = (Summarizer{Model: summaryModel{bad}}).SummarizeStructured(context.Background(), source, 2048); err == nil {
			t.Fatal("invalid summary accepted", bad)
		}
	}
}
