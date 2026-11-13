package agentstore

import (
	"context"
	"errors"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	resources "github.com/musi-ai/education/agent-design"
	"github.com/musi-ai/education/internal/agent/adk/middleware"
	"github.com/musi-ai/education/internal/agent/business/demo"
	agentruntime "github.com/musi-ai/education/internal/agent/runtime"
	ctxbuild "github.com/musi-ai/education/internal/agent/runtime/context"
	"github.com/musi-ai/education/internal/agent/runtime/event"
	"github.com/musi-ai/education/internal/agent/runtime/state"
	"github.com/musi-ai/education/internal/agent/runtime/storage/contracttest"
	"github.com/musi-ai/education/internal/query"
	"strings"
	"testing"
	"time"
)

type failingProducer struct{ events []event.Event }

func (p *failingProducer) Emit(_ context.Context, e event.Event) error {
	p.events = append(p.events, e)
	return errors.New("simulated broker unavailable")
}
func TestRuntimeCanonicalHistoryRawQueryAndPostSealEvent(t *testing.T) {
	s := testStore(t)
	q := fixtureQueries(s)
	ctx := context.Background()
	raw := "  x < 2，如何求解？\n"
	record, _, e := q.Create(ctx, "alice", query.Input{Query: raw, ConversationID: "runtime", ClientRequestID: "canonical-test"})
	if e != nil {
		t.Fatal(e)
	}
	scope := state.Scope{Namespace: "teacher", SubjectID: "alice", ConversationID: "runtime"}
	in := contracttest.Input(scope, 1)
	in.InputRef = record.ID
	if e = s.DB.QueryRowContext(ctx, "SELECT turn_id FROM agent_message WHERE message_id=?", record.ID).Scan(&in.Identity.TurnID); e != nil {
		t.Fatal(e)
	}
	app := demo.New()
	app.Inputs[record.ID] = raw
	counter := ctxbuild.ConservativeCounter{}
	m := &demo.Model{UseSkill: true}
	skills, e := resources.Skills()
	if e != nil {
		t.Fatal(e)
	}
	f := &agentruntime.Factory{Models: func(context.Context, string) (model.BaseChatModel, error) { return m, nil }, Tools: map[string]tool.InvokableTool{"commit_demo": app.Tool()}, Skills: skills, Guard: app, Counter: counter}
	spec := state.Spec{Name: "test", Version: "v1", Instruction: "fixed", ModelRef: "fake", SkillRefs: []string{"step-tutor"}, ToolRefs: []string{"commit_demo"}, TerminalTools: []string{"commit_demo"}, MaxIterations: 4, MaxToolCalls: 4, Timeout: time.Minute, History: state.DefaultPolicy()}
	producer := &failingProducer{}
	rt, e := agentruntime.New(agentruntime.Config{Store: s, Provider: app, Guard: app, Completion: app, Engine: f, Specs: map[string]state.Spec{in.ConfigRef: spec}, Counter: counter}, agentruntime.WithEventProducer(producer), agentruntime.WithBusinessHooks(middleware.LifecycleHooks{AfterTool: func(_ context.Context, in *middleware.HookInput) error {
		if in.ToolName == "read_file" {
			in.Result = "reviewed skill body"
		}
		return nil
	}}))
	if e != nil {
		t.Fatal(e)
	}
	result, e := rt.RunTurn(ctx, in)
	if e != nil || result.Status != "succeeded" || result.HistoryStatus != "sealed" {
		t.Fatalf("run failed: %+v %v", result, e)
	}
	shown, e := q.Get(ctx, "alice", record.ID)
	if e != nil || shown.Query != raw || shown.Status != "ANSWERED" {
		t.Fatal("raw UI input lost", shown, e)
	}
	view, e := s.View(ctx, scope)
	if e != nil || len(view.Turns) != 1 {
		t.Fatal(e)
	}
	messages := view.Turns[0].Messages
	if !strings.Contains(messages[0].Content, "<current-problem>") || !strings.Contains(messages[0].Content, "<student-input>") || messages[2].Content != "reviewed skill body" {
		t.Fatalf("noncanonical history: %+v", messages)
	}
	if len(producer.events) != 1 {
		t.Fatal("event must follow successful seal")
	}
	payload := producer.events[0].Payload.(*event.TurnCompleted)
	if payload.ItemCount != len(messages) || payload.TailSeq != 2 || payload.Items[0].Seq != 1 || payload.Items[0].MessageID != record.ID || payload.Preamble == "" || payload.Memory == "" {
		t.Fatalf("incomplete event: %+v", payload)
	}
	if _, e = rt.RunTurn(ctx, in); e != nil || m.Calls.Load() != 2 || len(producer.events) != 1 {
		t.Fatal("committed retry reexecuted or reemitted", e)
	}
	var count int
	if e = s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA=DATABASE()").Scan(&count); e != nil || count != 4 {
		t.Fatal("must retain exactly three tables", count, e)
	}
}
