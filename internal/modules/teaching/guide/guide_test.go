package guide

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
	"github.com/musi-ai/education/internal/agent/adk/middleware"
	ctxbuild "github.com/musi-ai/education/internal/agent/runtime/context"
	"github.com/musi-ai/education/internal/agent/runtime/state"
	"github.com/musi-ai/education/internal/application/agentic"
	"github.com/musi-ai/education/internal/modules/teaching/solve"
	"github.com/musi-ai/education/internal/query"
)

type stubQueries struct{ rec query.Record }

func (s stubQueries) Create(context.Context, string, query.Input) (query.Record, bool, error) {
	return s.rec, false, nil
}
func (s stubQueries) Get(context.Context, string, string) (query.Record, error) { return s.rec, nil }
func (s stubQueries) List(context.Context, string, string, *query.Cursor, int) ([]query.Record, bool, error) {
	return nil, false, nil
}

type stubRepo struct{ view solve.View }

func (s stubRepo) Get(context.Context, string, string) (solve.View, error) { return s.view, nil }

func sampleView() solve.View {
	return solve.View{
		QuestionID: "q_1",
		Question:   solve.Question{Stem: "解方程 x^2-5x+6=0", Type: "constructed_response"},
		Solution: solve.Solution{
			Answer:   []solve.Answer{{Content: "x=2 或 x=3"}},
			Analysis: "利用因式分解与零乘积律。",
			Steps: []solve.Step{
				{Title: "找因数组合", Content: "找到 -2 和 -3"},
				{Title: "写出因式分解", Content: "(x-2)(x-3)"},
			},
		},
	}
}

func turnFor(mode, questionID string) *agentic.Turn {
	return &agentic.Turn{Request: agentic.Request{Input: state.Input{
		Identity: state.Identity{Scope: state.Scope{SubjectID: "s1"}},
		InputRef: "m1",
	}}}
}

func TestContributeActivatesOnlyForGuideTurns(t *testing.T) {
	view := sampleView()
	newModule := func(mode, qid string) Module {
		return Module{Service: &Service{
			Repo:    stubRepo{view: view},
			Queries: stubQueries{rec: query.Record{Input: query.Input{Mode: mode, GuideQuestionID: qid}}},
		}}
	}

	c, err := newModule("", "").Contribute(context.Background(), turnFor("", ""))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Tools) != 0 || len(c.TurnProviders) != 0 {
		t.Fatalf("non-guide turn must contribute nothing: %+v", c)
	}

	c, err = newModule(query.GuideMode, "q_1").Contribute(context.Background(), turnFor(query.GuideMode, "q_1"))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Tools) != 2 {
		t.Fatalf("guide turn should expose outline + reveal, got %v", len(c.Tools))
	}
	for _, name := range []string{"get_guide_outline", "reveal_solution"} {
		if _, ok := c.Tools[name]; !ok {
			t.Fatalf("missing tool %s", name)
		}
	}
	if len(c.TurnProviders) != 1 {
		t.Fatalf("expected one provider, got %d", len(c.TurnProviders))
	}
	// The mode block must stay out of the student's message, otherwise it is
	// persisted and replayed as history on every later turn.
	if c.TurnProviders[0].Slot != ctxbuild.SlotPreamble {
		t.Fatalf("guide block must use the preamble slot, got %v", c.TurnProviders[0].Slot)
	}
	if c.SkillPaths["step-tutor"] != "/skills/step-tutor/SKILL.md" {
		t.Fatalf("step-tutor skill not authorized: %+v", c.SkillPaths)
	}
}

func TestOutlineWithholdsTheAnswer(t *testing.T) {
	out := Outline(sampleView())
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	body := string(b)
	for _, leaked := range []string{"x=2", "x=3", "零乘积律", "-2 和 -3"} {
		if strings.Contains(body, leaked) {
			t.Fatalf("outline leaked %q: %s", leaked, body)
		}
	}
	if !strings.Contains(body, "找因数组合") {
		t.Fatalf("outline should keep step titles: %s", body)
	}
}

func TestRevealReturnsCommittedSolution(t *testing.T) {
	b, err := json.Marshal(Reveal(sampleView()))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "x=2 或 x=3") {
		t.Fatalf("reveal must carry the committed answer: %s", b)
	}
}

func TestVerifiedAnswerRequiresActualRevealTool(t *testing.T) {
	m := Module{Service: &Service{Repo: stubRepo{view: sampleView()}, Queries: stubQueries{rec: query.Record{Input: query.Input{Mode: query.GuideMode, GuideQuestionID: "q_1"}}}}}
	c, e := m.Contribute(context.Background(), turnFor(query.GuideMode, "q_1"))
	if e != nil {
		t.Fatal(e)
	}
	h := c.LifecycleHooks[0]
	output := &middleware.HookInput{Messages: []*schema.Message{schema.AssistantMessage("已核对你的推导，答案是……", nil)}}
	if e = h.ValidateModelOutput(context.Background(), output); e == nil {
		t.Fatal("unverified answer accepted")
	}
	if e = h.AfterTool(context.Background(), &middleware.HookInput{ToolName: "reveal_solution", Result: `{"verified":true}`}); e != nil {
		t.Fatal(e)
	}
	if e = h.ValidateModelOutput(context.Background(), output); e != nil {
		t.Fatal(e)
	}
}
