package agentic_test

import (
	"context"
	"errors"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	mem "github.com/musi-ai/education/internal/adapters/persistence/inmemory/agentstore"
	"github.com/musi-ai/education/internal/agent/adk/middleware"
	"github.com/musi-ai/education/internal/agent/runtime/state"
	"github.com/musi-ai/education/internal/application/agentic"
	"testing"
)

type lookupTool struct{}

func (lookupTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "lookup", Desc: "test", ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{})}, nil
}
func (lookupTool) InvokableRun(context.Context, string, ...tool.Option) (string, error) {
	return "unreviewed", nil
}

type toolModel struct{ calls int }

func (m *toolModel) Generate(_ context.Context, ms []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	m.calls++
	if m.calls == 1 {
		return schema.AssistantMessage("", []schema.ToolCall{{ID: "call", Type: "function", Function: schema.FunctionCall{Name: "lookup", Arguments: `{}`}}}), nil
	}
	if ms[len(ms)-1].Content != "verified" {
		return nil, errors.New("model saw unreviewed result")
	}
	return schema.AssistantMessage("final", nil), nil
}
func (*toolModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("unused")
}
func TestToolProjectionUsesReviewedCanonicalResult(t *testing.T) {
	ctx := context.Background()
	s := mem.New()
	m := &toolModel{}
	projected := ""
	p, e := agentic.New(ctx, agentic.Dependencies{Store: s, Models: func(context.Context, string) (model.BaseChatModel, error) { return m, nil }, Spec: spec()}, agentic.NewRegistry(agentic.RegistrationFunc(func(context.Context, *agentic.Turn) (agentic.Contribution, error) {
		return agentic.Contribution{Tools: map[string]tool.InvokableTool{"lookup": lookupTool{}}, LifecycleHooks: []middleware.LifecycleHooks{{AfterTool: func(_ context.Context, in *middleware.HookInput) error { in.Result = "verified"; return nil }}}, ToolHandlers: map[string]func(context.Context, string) ([]agentic.Packet, error){"lookup": func(_ context.Context, out string) ([]agentic.Packet, error) {
			projected = out
			return []agentic.Packet{{Type: "card", BizName: "lookup", Text: out, EndBusiness: true}}, nil
		}}}, nil
	})))
	if e != nil {
		t.Fatal(e)
	}
	scope := state.Scope{Namespace: "t", SubjectID: "a", ConversationID: "tools"}
	turn, e := p.Run(ctx, agentic.Request{Input: state.Input{Identity: state.Identity{Scope: scope, TurnID: "t"}}, Query: "q"}, agentic.SinkFunc(func(context.Context, agentic.Packet) error { return nil }))
	if e != nil || projected != "verified" || turn.Result.Status != "succeeded" {
		t.Fatal(projected, e)
	}
	saved, e := s.GetTurn(ctx, scope, "t")
	if e != nil || saved.Messages[2].Content != "verified" {
		t.Fatal("stored result diverged", e)
	}
}
