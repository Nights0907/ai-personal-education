package solve

import (
	"context"
	"github.com/cloudwego/eino/schema"
	"github.com/musi-ai/education/internal/agent/adk/middleware"
	"github.com/musi-ai/education/internal/agent/runtime/state"
	"github.com/musi-ai/education/internal/application/agentic"
	"github.com/musi-ai/education/internal/query"
	"testing"
)

type modeQueries struct {
	query.Repository
	rec query.Record
}

func (r modeQueries) Get(context.Context, string, string) (query.Record, error) { return r.rec, nil }
func TestReferencePreparationRequiresToolsOnlyBeforeQuestionBinding(t *testing.T) {
	for _, tc := range []struct {
		mode, qid string
		required  bool
	}{{query.GuideMode, "", true}, {query.SolveMode, "", true}, {query.GuideMode, "q1", false}, {query.ReviewMode, "q1", false}} {
		rec := query.Record{TurnID: "t1", Input: query.Input{Query: "求和", ConversationID: "c1", Mode: tc.mode, GuideQuestionID: tc.qid}}
		turn := &agentic.Turn{Request: agentic.Request{Input: state.Input{Identity: state.Identity{Scope: state.Scope{Namespace: "teacher", SubjectID: "s1", ConversationID: "c1"}, TurnID: "t1"}, InputRef: "m1"}}, Sink: agentic.SinkFunc(func(context.Context, agentic.Packet) error { return nil })}
		contribution, e := (Module{Service: &Service{Queries: modeQueries{rec: rec}}}).Contribute(context.Background(), turn)
		if e != nil {
			t.Fatal(e)
		}
		h := &middleware.HookInput{}
		for _, hook := range contribution.LifecycleHooks {
			if hook.BeforeModel != nil {
				if e = hook.BeforeModel(context.Background(), h); e != nil {
					t.Fatal(e)
				}
			}
		}
		if required := h.ToolChoice != nil && *h.ToolChoice == schema.ToolChoiceForced; required != tc.required {
			t.Fatalf("mode %q qid %q tool requirement=%v", tc.mode, tc.qid, required)
		}
	}
}
