package runtime_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	resources "github.com/musi-ai/education/agent-design"
	memstore "github.com/musi-ai/education/internal/adapters/persistence/inmemory/agentstore"
	"github.com/musi-ai/education/internal/agent/business/demo"
	runtime "github.com/musi-ai/education/internal/agent/runtime"
	ctxbuild "github.com/musi-ai/education/internal/agent/runtime/context"
	"github.com/musi-ai/education/internal/agent/runtime/state"
	"github.com/musi-ai/education/internal/agent/runtime/storage"
	"github.com/musi-ai/education/internal/agent/runtime/storage/contracttest"
)

type captureStore struct {
	storage.Store
	snaps      []state.Snapshot
	failFinish bool
	items      []*schema.Message
}

func (s *captureStore) Append(c context.Context, in state.Input, n int, m *schema.Message) error {
	s.items = append(s.items, state.CleanMessages([]*schema.Message{m})...)
	return s.Store.Append(c, in, n, m)
}

func (s *captureStore) SaveSnapshot(c context.Context, in state.Input, snap state.Snapshot) error {
	s.snaps = append(s.snaps, snap)
	return s.Store.SaveSnapshot(c, in, snap)
}
func (s *captureStore) Finish(c context.Context, in state.Input, status, code string, r *state.Receipt, ms []*schema.Message) error {
	if s.failFinish && r != nil {
		return errors.New("injected persistence failure")
	}
	return s.Store.Finish(c, in, status, code, r, ms)
}
func setup(t *testing.T, behavior string, options ...runtime.Option) (*runtime.Runtime, *demo.App, *demo.Model, *captureStore, state.Input) {
	t.Helper()
	app := demo.New()
	in := contracttest.Input(state.Scope{Namespace: "test", SubjectID: "alice", ConversationID: "eino"}, 1)
	app.Inputs[in.InputRef] = "原始问题含 {not_a_template}，请解释关键步骤。"
	counter := ctxbuild.ConservativeCounter{}
	m := &demo.Model{UseSkill: true, Behavior: behavior}
	skills, e := resources.Skills()
	if e != nil {
		t.Fatal(e)
	}
	s := &captureStore{Store: memstore.New()}
	factory := &runtime.Factory{Models: func(context.Context, string) (model.BaseChatModel, error) { return m, nil }, Tools: map[string]tool.InvokableTool{"commit_demo": app.Tool()}, Skills: skills, Guard: app, Counter: counter}
	spec := state.Spec{Name: "test", Version: "v1", Instruction: "Fixed SP with {literal braces}", ModelRef: "fake", ToolRefs: []string{"commit_demo"}, SkillRefs: []string{"step-tutor"}, TerminalTools: []string{"commit_demo"}, MaxIterations: 4, MaxToolCalls: 4, Timeout: time.Minute, History: state.DefaultPolicy()}
	rt, e := runtime.New(runtime.Config{Store: s, Provider: app, Guard: app, Completion: app, Engine: factory, Specs: map[string]state.Spec{in.ConfigRef: spec}, Counter: counter}, options...)
	if e != nil {
		t.Fatal(e)
	}
	return rt, app, m, s, in
}
func TestEinoTurnAndIdempotentRetry(t *testing.T) {
	rt, app, m, s, in := setup(t, "")
	ctx := context.Background()
	r, e := rt.RunTurn(ctx, in)
	if e != nil || r.Status != "succeeded" || r.HistoryStatus != "sealed" {
		t.Fatalf("%+v %v", r, e)
	}
	if m.Calls.Load() != 2 || app.CommitCalls != 1 {
		t.Fatal("real Eino skill/tool loop not executed")
	}
	v, e := s.View(ctx, in.Identity.Scope)
	if e != nil || len(v.Turns) != 1 {
		t.Fatal(e)
	}
	for _, msg := range v.Turns[0].Messages {
		if msg.ReasoningContent != "" || msg.Role == schema.System || strings.Contains(msg.Content, "Fixed SP") {
			t.Fatal("control/hidden reasoning leaked into memory")
		}
	}
	if v.Turns[0].Messages[0].Content != s.snaps[0].CanonicalInput.Content || s.snaps[0].OriginalInput.Content != app.Inputs[in.InputRef] {
		t.Fatal("canonical history or raw input snapshot changed")
	}
	if len(s.snaps) != 3 {
		t.Fatalf("expected assembly and 2 model inputs, got %d", len(s.snaps))
	}
	if len(s.snaps[1].ToolSchemas) != 3 {
		t.Fatal("actual model schemas missing")
	}
	if !strings.Contains(s.snaps[1].Messages[1].Content, "<available-skills>") || strings.Contains(s.snaps[1].Messages[0].Content, "## Skills") {
		t.Fatal("skill catalog was not separately orchestrated")
	}
	if !strings.Contains(s.snaps[1].Messages[len(s.snaps[1].Messages)-1].Content, "<student-input>") {
		t.Fatal("actual model input lost student block")
	}
	countSP := 0
	for _, msg := range s.snaps[1].Messages {
		countSP += strings.Count(msg.Content, "Fixed SP with {literal braces}")
	}
	if countSP != 1 {
		t.Fatal("duplicated or interpolated SP")
	}
	retry := in
	retry.Identity.RunID = "retry-after-commit"
	retry.Lease.Token++
	r, e = rt.RunTurn(ctx, retry)
	if e != nil || r.Status != "succeeded" || m.Calls.Load() != 2 || app.CommitCalls != 1 {
		t.Fatal("committed retry reran agent", r, e)
	}
}
func TestRejectParallelBatchBeforeSideEffect(t *testing.T) {
	rt, app, _, s, in := setup(t, "parallel")
	r, e := rt.RunTurn(context.Background(), in)
	if e == nil || r.Status == "succeeded" || app.CommitCalls != 0 {
		t.Fatal("parallel batch executed", r, e)
	}
	v, _ := s.View(context.Background(), in.Identity.Scope)
	if len(v.Turns) != 0 {
		t.Fatal("failed trace loaded as memory")
	}
}
func TestNaturalTextIsNotCommit(t *testing.T) {
	rt, app, _, _, in := setup(t, "text")
	r, e := rt.RunTurn(context.Background(), in)
	if !errors.Is(e, state.ErrIncomplete) || r.Status == "succeeded" || app.CommitCalls != 0 {
		t.Fatal(r, e)
	}
}
func TestOverBudgetStopsBeforeModel(t *testing.T) {
	rt, app, m, _, in := setup(t, "")
	app.Inputs[in.InputRef] = strings.Repeat("数学条件不可删除", 5000)
	r, e := rt.RunTurn(context.Background(), in)
	if !errors.Is(e, state.ErrBudget) || r.ErrorCode != "CONTEXT_BUDGET_EXCEEDED" || m.Calls.Load() != 0 {
		t.Fatal("overbudget input silently truncated or sent to provider", r, e)
	}
}
func TestCommittedBusinessSurvivesHistoryFailure(t *testing.T) {
	rt, app, m, s, in := setup(t, "")
	s.failFinish = true
	ctx := context.Background()
	r, e := rt.RunTurn(ctx, in)
	if e != nil || r.Status != "succeeded" || r.HistoryStatus != "pending_repair" || app.CommitCalls != 1 {
		t.Fatal(r, e)
	}
	v, _ := s.View(ctx, in.Identity.Scope)
	if len(v.Turns) != 0 {
		t.Fatal("unsealed history published")
	}
	before := m.Calls.Load()
	retry := in
	retry.Identity.RunID = "new-run"
	retry.Lease.Token++
	if _, e = rt.RunTurn(ctx, retry); e != nil {
		t.Fatal(e)
	}
	if m.Calls.Load() != before || app.CommitCalls != 1 {
		t.Fatal("duplicate side effect after store failure")
	}
	s.failFinish = false
	canonical := append([]*schema.Message{schema.UserMessage(app.Inputs[in.InputRef])}, s.items...)
	if e = rt.Repair(ctx, in, canonical); e != nil {
		t.Fatal(e)
	}
	if e = rt.Repair(ctx, in, canonical); e != nil {
		t.Fatal(e)
	}
	v, _ = s.View(ctx, in.Identity.Scope)
	if len(v.Turns) != 1 || app.CommitCalls != 1 {
		t.Fatal("repair mutated business")
	}
}

func TestContextRecoveryIsBoundedAndDoesNotTruncateOriginal(t *testing.T) {
	calls := 0
	rt, app, model, _, in := setup(t, "", runtime.WithContextBudgetHandler(func(context.Context, state.Scope, state.Policy) error { calls++; return nil }))
	app.Inputs[in.InputRef] = strings.Repeat("数学条件不可删除", 5000)
	_, err := rt.RunTurn(context.Background(), in)
	if !errors.Is(err, state.ErrBudget) || calls != 3 || model.Calls.Load() != 0 {
		t.Fatal("unbounded recovery or oversized input sent", calls, err)
	}
}
