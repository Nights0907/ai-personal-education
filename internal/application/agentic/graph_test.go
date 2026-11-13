package agentic_test

import (
	"context"
	"errors"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	mem "github.com/musi-ai/education/internal/adapters/persistence/inmemory/agentstore"
	"github.com/musi-ai/education/internal/agent/adk/middleware"
	ctxbuild "github.com/musi-ai/education/internal/agent/runtime/context"
	"github.com/musi-ai/education/internal/agent/runtime/observability"
	"github.com/musi-ai/education/internal/agent/runtime/state"
	"github.com/musi-ai/education/internal/application/agentic"
	"strings"
	"testing"
	"time"
)

type textModel struct {
	calls int
	seen  string
	fail  bool
}

func (m *textModel) Generate(_ context.Context, ms []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	m.calls++
	m.seen = ms[len(ms)-1].Content
	if m.fail {
		return nil, errors.New("model failure")
	}
	return schema.AssistantMessage("answer draft", nil), nil
}
func (*textModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("not used")
}
func spec() state.Spec {
	return state.Spec{Name: "test", Version: "v1", Instruction: "fixed SP", ModelRef: "fake", MaxIterations: 3, MaxToolCalls: 3, Timeout: time.Minute, History: state.DefaultPolicy()}
}
func TestFourNodeGraphLifecycleAndProjection(t *testing.T) {
	ctx := context.Background()
	s := mem.New()
	m := &textModel{}
	recorder := &observability.Recorder{}
	events := []string{}
	post := false
	registration := agentic.RegistrationFunc(func(context.Context, *agentic.Turn) (agentic.Contribution, error) {
		return agentic.Contribution{
			PreloadHooks: []func(context.Context, *agentic.Turn) error{func(_ context.Context, turn *agentic.Turn) error {
				events = append(events, "preload")
				turn.Request.Query = "preloaded query"
				return nil
			}},
			LifecycleHooks: []middleware.LifecycleHooks{{BeforeTurn: func(_ context.Context, in *middleware.HookInput) error {
				events = append(events, "before_turn")
				in.Messages[0].Content += " + turn"
				return nil
			}, BeforeAssemble: func(_ context.Context, in *middleware.HookInput) error {
				events = append(events, "before_assemble")
				in.Messages[0].Content += " + assemble"
				return nil
			}, AfterModelRewriteState: func(_ context.Context, in *middleware.HookInput) error {
				in.Messages[len(in.Messages)-1].Content = "reviewed answer"
				return nil
			}, AfterTurn: func(ctx context.Context, _ *middleware.HookInput) error {
				events = append(events, "after_turn")
				_, e := s.GetTurn(ctx, state.Scope{Namespace: "test", SubjectID: "alice", ConversationID: "c1"}, "t1")
				return e
			}, Policy: middleware.HookPolicy{Mode: middleware.FailTurn}}},
			PacketHooks: []agentic.PacketHook{{BeforeSend: func(_ context.Context, p *agentic.Packet) (bool, error) {
				events = append(events, "before_send")
				return true, nil
			}}},
			PostRuntimeHooks: []func(context.Context, *agentic.Turn) error{func(ctx context.Context, turn *agentic.Turn) error {
				post = true
				events = append(events, "post")
				_, e := s.GetTurn(ctx, turn.Request.Input.Identity.Scope, "t1")
				return e
			}},
		}, nil
	})
	p, e := agentic.New(ctx, agentic.Dependencies{Store: s, Models: func(context.Context, string) (model.BaseChatModel, error) { return m, nil }, Spec: spec(), Tracer: observability.NewTracer(recorder)}, agentic.NewRegistry(registration))
	if e != nil {
		t.Fatal(e)
	}
	request := agentic.Request{Input: state.Input{Identity: state.Identity{Scope: state.Scope{Namespace: "test", SubjectID: "alice", ConversationID: "c1"}, TurnID: "t1"}}, Query: "original"}
	packets := []agentic.Packet{}
	turn, e := p.Run(ctx, request, agentic.SinkFunc(func(_ context.Context, v agentic.Packet) error { packets = append(packets, v); return nil }))
	if e != nil {
		t.Fatal(e)
	}
	expected := []string{agentic.BusinessPreload, agentic.RuntimeInit, agentic.RuntimeTurn, agentic.RuntimeMessageProjection}
	if state.Hash(turn.Stages) != state.Hash(expected) {
		t.Fatal(turn.Stages)
	}
	if turn.Result.Status != "succeeded" || turn.Result.CommittedResult != "" || !post || m.calls != 1 || !strings.Contains(m.seen, "preloaded query + turn + assemble") {
		t.Fatalf("bad run %+v %v %s", turn.Result, events, m.seen)
	}
	if len(packets) != 2 || packets[0].Text != "reviewed answer" {
		t.Fatal("projection did not use canonical output", packets)
	}
	spans := recorder.Records()
	if len(spans) != 2 || spans[0].Name != "runtime.model" || spans[0].ParentSpanID != spans[1].SpanID || spans[0].TraceID != spans[1].TraceID {
		t.Fatal("trace not linked", spans)
	}
	_, e = p.Run(ctx, request, agentic.SinkFunc(func(context.Context, agentic.Packet) error { return nil }))
	if e != nil || m.calls != 1 {
		t.Fatal("sealed retry called model", e, m.calls)
	}
}
func TestShortCircuitAndCancellation(t *testing.T) {
	m := &textModel{}
	ctx := context.Background()
	p, e := agentic.New(ctx, agentic.Dependencies{Store: mem.New(), Models: func(context.Context, string) (model.BaseChatModel, error) { return m, nil }, Spec: spec()}, agentic.NewRegistry(agentic.RegistrationFunc(func(context.Context, *agentic.Turn) (agentic.Contribution, error) {
		return agentic.Contribution{PreloadHooks: []func(context.Context, *agentic.Turn) error{func(_ context.Context, t *agentic.Turn) error {
			t.ShortCircuit = true
			t.Packets = []agentic.Packet{{Type: "text", Text: "business response"}}
			return nil
		}}}, nil
	})))
	if e != nil {
		t.Fatal(e)
	}
	count := 0
	turn, e := p.Run(ctx, agentic.Request{Input: state.Input{Identity: state.Identity{Scope: state.Scope{Namespace: "t", SubjectID: "a", ConversationID: "c"}}}, Query: "q"}, agentic.SinkFunc(func(context.Context, agentic.Packet) error { count++; return nil }))
	if e != nil || m.calls != 0 || count != 1 || len(turn.Stages) != 4 {
		t.Fatal("short circuit entered runtime", e)
	}
}
func TestAssembleFailureReachesOnError(t *testing.T) {
	ctx := context.Background()
	called := false
	m := &textModel{}
	p, e := agentic.New(ctx, agentic.Dependencies{Store: mem.New(), Models: func(context.Context, string) (model.BaseChatModel, error) { return m, nil }, Spec: spec()}, agentic.NewRegistry(agentic.RegistrationFunc(func(context.Context, *agentic.Turn) (agentic.Contribution, error) {
		return agentic.Contribution{TurnProviders: []ctxbuild.TurnProvider{{Name: "broken", Slot: ctxbuild.SlotPreamble, Provide: func(context.Context, ctxbuild.BuildInput) ([]ctxbuild.Block, error) {
			return nil, errors.New("provider failed")
		}}}, LifecycleHooks: []middleware.LifecycleHooks{{OnError: func(context.Context, *middleware.HookInput) error { called = true; return nil }}}}, nil
	})))
	if e != nil {
		t.Fatal(e)
	}
	_, e = p.Run(ctx, agentic.Request{Input: state.Input{Identity: state.Identity{Scope: state.Scope{Namespace: "t", SubjectID: "a", ConversationID: "c"}, TurnID: "bad"}}, Query: "q"}, agentic.SinkFunc(func(context.Context, agentic.Packet) error { return nil }))
	if e == nil || !called || m.calls != 0 {
		t.Fatal("assembly failure bypassed lifecycle", e, called, m.calls)
	}
}

func TestSingleAgentDialogueDoesNotRequireUnavailableTerminalTool(t *testing.T) {
	ctx := context.Background()
	store := mem.New()
	cfg := spec()
	cfg.TerminalTools = []string{"finalize_solution"}
	p, e := agentic.New(ctx, agentic.Dependencies{Store: store, Models: func(context.Context, string) (model.BaseChatModel, error) { return &textModel{}, nil }, Spec: cfg}, agentic.NewRegistry())
	if e != nil {
		t.Fatal(e)
	}
	_, e = p.Run(ctx, agentic.Request{Query: "下一步", Input: state.Input{Identity: state.Identity{Scope: state.Scope{Namespace: "teacher", SubjectID: "s", ConversationID: "c"}, TurnID: "t"}, InputRef: "m", BindingRef: "m"}}, agentic.SinkFunc(func(context.Context, agentic.Packet) error { return nil }))
	if e != nil {
		t.Fatal(e)
	}
}
