package middleware

import (
	"context"
	"errors"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"testing"
)

type invalidAnswerModel struct {
	calls             int
	ignoreRequirement bool
}

func (m *invalidAnswerModel) Generate(_ context.Context, _ []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	m.calls++
	o := model.GetCommonOptions(nil, opts...)
	if !m.ignoreRequirement && o.ToolChoice != nil && *o.ToolChoice == schema.ToolChoiceForced {
		return schema.AssistantMessage("", []schema.ToolCall{{ID: "real-model-call", Function: schema.FunctionCall{Name: "reveal_solution", Arguments: `{"question_id":"q1"}`}}}), nil
	}
	return schema.AssistantMessage("未核对的答案", nil), nil
}
func (m *invalidAnswerModel) Stream(ctx context.Context, ms []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	r, e := m.Generate(ctx, ms, opts...)
	return schema.StreamReaderFromArray([]*schema.Message{r}), e
}
func TestInvalidAnswerRequestsOneRealToolCallBeforeDisplay(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, ignore := range []bool{false, true} {
			base := &invalidAnswerModel{ignoreRequirement: ignore}
			a := NewAdapter([]LifecycleHooks{{ValidateModelOutput: func(_ context.Context, h *HookInput) error {
				if len(h.Messages[len(h.Messages)-1].ToolCalls) == 0 {
					return ErrToolRequired
				}
				return nil
			}}})
			m := &constrainedModel{BaseChatModel: base, adapter: a}
			var out *schema.Message
			var e error
			if stream {
				var r *schema.StreamReader[*schema.Message]
				r, e = m.Stream(context.Background(), nil)
				if e == nil {
					out, e = r.Recv()
					r.Close()
				}
			} else {
				out, e = m.Generate(context.Background(), nil)
			}
			if base.calls != 2 {
				t.Fatalf("unbounded or missing correction: %d", base.calls)
			}
			if ignore {
				if !errors.Is(e, ErrToolRequired) {
					t.Fatal("invalid answer accepted", e)
				}
			} else if e != nil || len(out.ToolCalls) != 1 || out.Content != "" {
				t.Fatal("tool selection lost", out, e)
			}
		}
	}
}

func TestToolVisibilityRestoresAfterOpeningPhase(t *testing.T) {
	calls := 0
	a := NewAdapter([]LifecycleHooks{{BeforeModel: func(_ context.Context, h *HookInput) error {
		calls++
		if calls == 1 {
			h.ToolAllowlist = []string{"opening"}
		}
		return nil
	}}})
	s := &adk.ChatModelAgentState{ToolInfos: []*schema.ToolInfo{{Name: "opening"}, {Name: "save"}}}
	_, s, e := a.BeforeModelRewriteState(context.Background(), s, nil)
	if e != nil || len(s.ToolInfos) != 1 {
		t.Fatal(s, e)
	}
	_, s, e = a.BeforeModelRewriteState(context.Background(), s, nil)
	if e != nil || len(s.ToolInfos) != 2 {
		t.Fatal("tool visibility stuck in opening phase", s, e)
	}
}
