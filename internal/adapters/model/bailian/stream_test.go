package bailian

import (
	"context"
	"errors"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/musi-ai/education/internal/agent/modelstream"
	"testing"
)

type chunkModel struct {
	generate, stream int
	fail             bool
}

func (m *chunkModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	m.generate++
	return schema.AssistantMessage("ordinary", nil), nil
}
func (m *chunkModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.stream++
	if m.fail {
		return nil, errors.New("stream interrupted")
	}
	index := 0
	return schema.StreamReaderFromArray([]*schema.Message{{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{Index: &index, ID: "call-1", Type: "function", Function: schema.FunctionCall{Name: "validate_solution", Arguments: `{"candidate":`}}}}, {Role: schema.Assistant, ToolCalls: []schema.ToolCall{{Index: &index, Function: schema.FunctionCall{Arguments: `{}}`}}}}}), nil
}
func TestGenerateObservesRealChunksAndReturnsCanonicalMessage(t *testing.T) {
	inner := &chunkModel{}
	slots, _ := NewLimiter(1)
	m := &limited{inner: inner, slots: slots}
	chunks := 0
	ctx := modelstream.WithObserver(context.Background(), func() func(*schema.Message) error { return func(*schema.Message) error { chunks++; return nil } })
	v, e := m.Generate(ctx, nil)
	if e != nil || chunks != 2 || inner.generate != 0 || inner.stream != 1 || len(v.ToolCalls) != 1 || v.ToolCalls[0].Function.Arguments != `{"candidate":{}}` {
		t.Fatal(v, e, chunks)
	}
	if _, e = m.Generate(modelstream.Without(ctx), nil); e != nil || inner.generate != 1 {
		t.Fatal("reviewer not isolated", e)
	}
	inner.fail = true
	if _, e = m.Generate(ctx, nil); e == nil {
		t.Fatal("stream failure hidden")
	}
	inner.fail = false
	if _, e = m.Generate(context.Background(), nil); e != nil {
		t.Fatal("limiter not released", e)
	}
}
func TestProviderHistoryOmitsEmptyTerminalWithoutBreakingToolPairs(t *testing.T) {
	call := schema.AssistantMessage("", []schema.ToolCall{{ID: "call", Type: "function", Function: schema.FunctionCall{Name: "submit_solution", Arguments: `{}`}}})
	result := &schema.Message{Role: schema.Tool, ToolCallID: "call", Content: `{"committed":true}`}
	input := []*schema.Message{schema.UserMessage("上轮题目"), call, result, schema.AssistantMessage("", nil), schema.AssistantMessage("  ", nil), schema.UserMessage("解方程4x-7=13")}
	got := providerMessages(input)
	if len(got) != 4 || got[1] != call || got[2] != result || got[3] != input[5] {
		t.Fatal("history protocol changed", got)
	}
	if len(input) != 6 || input[3].Content != "" {
		t.Fatal("stored history mutated")
	}
}
