package adk_test

import (
	"context"
	"errors"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	sdk "github.com/musi-ai/education/internal/agent/adk"
	"github.com/musi-ai/education/internal/agent/adk/middleware"
	"testing"
)

type testModel struct{ calls int }

func (m *testModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	m.calls++
	if m.calls == 1 {
		return schema.AssistantMessage("", []schema.ToolCall{{ID: "call-1", Type: "function", Function: schema.FunctionCall{Name: "read", Arguments: `{}`}}}), nil
	}
	return schema.AssistantMessage("draft", nil), nil
}
func (*testModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("unused")
}

type testTool struct{ calls int }

func (*testTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "read", Desc: "test read", ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{})}, nil
}
func (t *testTool) InvokableRun(context.Context, string, ...tool.Option) (string, error) {
	t.calls++
	return "raw", nil
}
func TestVetoPreventsToolSideEffect(t *testing.T) {
	ctx := context.Background()
	m := &testModel{}
	tool := &testTool{}
	after := false
	loop, e := sdk.CreateAgentLoop(sdk.Config{Name: "test", SystemPrompt: "fixed", Models: func(context.Context, string) (model.BaseChatModel, error) { return m, nil }, ModelRef: "fake", MaxIterations: 3}, sdk.WithTools(tool), sdk.WithHooks(middleware.LifecycleHooks{BeforeTool: func(context.Context, *middleware.HookInput) (middleware.ToolDecision, error) {
		return middleware.Deny, nil
	}, AfterTurn: func(_ context.Context, in *middleware.HookInput) error {
		after = true
		if in.Err == nil {
			t.Error("missing failure status")
		}
		return nil
	}}))
	if e != nil {
		t.Fatal(e)
	}
	failed := false
	for ev := range loop.Run(ctx, []*schema.Message{schema.UserMessage("hi")}) {
		failed = failed || ev.Err != nil
	}
	if !failed || tool.calls != 0 || !after {
		t.Fatalf("veto failed: failed=%v calls=%d after=%v", failed, tool.calls, after)
	}
}
func TestCanonicalIncludesModelAndToolRewrites(t *testing.T) {
	ctx := context.Background()
	m := &testModel{}
	tool := &testTool{}
	var canonical []*schema.Message
	loop, e := sdk.CreateAgentLoop(sdk.Config{Name: "test", SystemPrompt: "fixed", Models: func(context.Context, string) (model.BaseChatModel, error) { return m, nil }, ModelRef: "fake", MaxIterations: 3, OnCanonical: func(ms []*schema.Message) error { canonical = ms; return nil }}, sdk.WithTools(tool), sdk.WithHooks(middleware.LifecycleHooks{AfterTool: func(_ context.Context, in *middleware.HookInput) error { in.Result = "reviewed tool"; return nil }, AfterModelRewriteState: func(_ context.Context, in *middleware.HookInput) error {
		last := in.Messages[len(in.Messages)-1]
		if last.Content == "draft" {
			last.Content = "reviewed answer"
		}
		return nil
	}}))
	if e != nil {
		t.Fatal(e)
	}
	for ev := range loop.Run(ctx, []*schema.Message{schema.UserMessage("hi")}) {
		if ev.Err != nil {
			t.Fatal(ev.Err)
		}
	}
	if len(canonical) != 5 || canonical[3].Role != schema.Tool || canonical[3].Content != "reviewed tool" || canonical[4].Content != "reviewed answer" {
		t.Fatalf("canonical timeline not rewritten: %#v", canonical)
	}
}
func TestIteratorEarlyStopDoesNotYieldAgain(t *testing.T) {
	loop, e := sdk.CreateAgentLoop(sdk.Config{Name: "test", SystemPrompt: "fixed", Models: func(context.Context, string) (model.BaseChatModel, error) { return &testModel{}, nil }, ModelRef: "fake", MaxIterations: 3}, sdk.WithHooks(middleware.LifecycleHooks{AfterTurn: func(context.Context, *middleware.HookInput) error { return errors.New("cleanup failed") }}))
	if e != nil {
		t.Fatal(e)
	}
	for range loop.Run(context.Background(), []*schema.Message{schema.UserMessage("hi")}) {
		break
	}
}

type choiceModel struct{ choices []*schema.ToolChoice }

func (m *choiceModel) Generate(_ context.Context, _ []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	choice := model.GetCommonOptions(nil, opts...).ToolChoice
	m.choices = append(m.choices, choice)
	if choice != nil && *choice == schema.ToolChoiceForced {
		return schema.AssistantMessage("", []schema.ToolCall{{ID: "required-call", Type: "function", Function: schema.FunctionCall{Name: "read", Arguments: `{}`}}}), nil
	}
	return schema.AssistantMessage("普通回复", nil), nil
}
func (*choiceModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("unused")
}
func TestPerCallToolRequirementReachesModelAndResetsForDialogue(t *testing.T) {
	m := &choiceModel{}
	calls := 0
	tool := &testTool{}
	loop, e := sdk.CreateAgentLoop(sdk.Config{Name: "test", SystemPrompt: "fixed", ModelRef: "fake", Models: func(context.Context, string) (model.BaseChatModel, error) { return m, nil }, MaxIterations: 3}, sdk.WithTools(tool), sdk.WithHooks(middleware.LifecycleHooks{BeforeModel: func(_ context.Context, h *middleware.HookInput) error {
		calls++
		if calls == 1 {
			c := schema.ToolChoiceForced
			h.ToolChoice = &c
		}
		return nil
	}}))
	if e != nil {
		t.Fatal(e)
	}
	for ev := range loop.Run(context.Background(), []*schema.Message{schema.UserMessage("引导学习")}) {
		if ev.Err != nil {
			t.Fatal(ev.Err)
		}
	}
	if tool.calls != 1 || len(m.choices) != 2 || m.choices[0] == nil || *m.choices[0] != schema.ToolChoiceForced || m.choices[1] != nil {
		t.Fatalf("requirement missing or leaked: tools=%d choices=%v", tool.calls, m.choices)
	}
}

type submissionRetryModel struct{ testModel }

func (m *submissionRetryModel) Generate(_ context.Context, messages []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	m.calls++
	if m.calls > 2 {
		return nil, errors.New("model invoked after committed submission")
	}
	if m.calls == 2 && messages[len(messages)-1].Content != `{"passed":false,"issues":["fix code"]}` {
		return nil, errors.New("missing corrective result")
	}
	return schema.AssistantMessage("", []schema.ToolCall{{ID: []string{"retry-1", "retry-2"}[m.calls-1], Type: "function", Function: schema.FunctionCall{Name: "submit_solution", Arguments: `{}`}}}), nil
}

type submissionRetryTool struct{ calls int }

func (*submissionRetryTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "submit_solution", Desc: "submit", ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{})}, nil
}
func (t *submissionRetryTool) InvokableRun(context.Context, string, ...tool.Option) (string, error) {
	t.calls++
	if t.calls == 1 {
		return `{"passed":false,"issues":["fix code"]}`, nil
	}
	return `{"committed":true}`, nil
}
func TestSubmissionCorrectionContinuesUntilCommitted(t *testing.T) {
	m := &submissionRetryModel{}
	submission := &submissionRetryTool{}
	loop, err := sdk.CreateAgentLoop(sdk.Config{Name: "test", SystemPrompt: "fixed", ModelRef: "fake", Models: func(context.Context, string) (model.BaseChatModel, error) { return m, nil }, MaxIterations: 5, ReturnDirectly: map[string]bool{"submit_solution": true}}, sdk.WithTools(submission))
	if err != nil {
		t.Fatal(err)
	}
	for event := range loop.Run(context.Background(), []*schema.Message{schema.UserMessage("question")}) {
		if event.Err != nil {
			t.Fatal(event.Err)
		}
	}
	if m.calls != 2 || submission.calls != 2 {
		t.Fatalf("model=%d submissions=%d", m.calls, submission.calls)
	}
}
