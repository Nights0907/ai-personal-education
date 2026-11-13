package memory_test

import (
	"context"
	"encoding/json"
	"errors"
	teachingsummary "github.com/musi-ai/education/internal/modules/teaching/summary"
	"testing"

	memstore "github.com/musi-ai/education/internal/adapters/persistence/inmemory/agentstore"
	"github.com/musi-ai/education/internal/agent/memory"
	ctxbuild "github.com/musi-ai/education/internal/agent/runtime/context"
	"github.com/musi-ai/education/internal/agent/runtime/state"
	"github.com/musi-ai/education/internal/agent/runtime/storage/contracttest"
)

type structuredStub struct {
	invalid bool
	calls   int
}

func (s *structuredStub) Summarize(context.Context, memory.SummarySource, int) (string, error) {
	return "", errors.New("legacy path must not be used")
}
func (s *structuredStub) SummarizeStructured(_ context.Context, source memory.SummarySource, _ int) (teachingsummary.DialogueSummary, error) {
	s.calls++
	ref := source.Turns[0].TurnID
	if s.invalid {
		ref = "another-student-turn"
	}
	return teachingsummary.DialogueSummary{SchemaVersion: teachingsummary.DialogueSummaryVersion, Overview: "学生正在讨论定义域", PendingQuestions: []teachingsummary.SummaryEvidence{{Text: "分母为零时如何处理", SourceTurnIDs: []string{ref}}}}, nil
}
func TestStructuredWorkerPersistsOnlyGroundedSummary(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		t.Run(map[bool]string{false: "valid", true: "foreign evidence"}[invalid], func(t *testing.T) {
			s := memstore.New()
			ctx := context.Background()
			scope := state.Scope{Namespace: "test", SubjectID: "alice", ConversationID: "structured"}
			for n := 1; n <= 3; n++ {
				contracttest.Seal(t, s, contracttest.Input(scope, n))
			}
			stub := &structuredStub{invalid: invalid}
			m := memory.Manager{Store: s, Counter: ctxbuild.ConservativeCounter{}, Summarizer: stub}
			p := state.DefaultPolicy()
			p.KeepRecentTurns = 1
			if _, err := m.Schedule(ctx, scope, p, true); err != nil {
				t.Fatal(err)
			}
			_, err := m.ProcessOne(ctx, "worker")
			v, e := s.View(ctx, scope)
			if e != nil {
				t.Fatal(e)
			}
			if invalid {
				if err == nil || v.Summary.Revision != 0 {
					t.Fatal("unrelated evidence published", err)
				}
				return
			}
			var saved teachingsummary.DialogueSummary
			_ = json.Unmarshal(v.Summary.Structured, &saved)
			if err != nil || stub.calls != 1 || len(v.Summary.Structured) == 0 || len(saved.PendingQuestions) == 0 || saved.PendingQuestions[0].SourceTurnIDs[0] != "t1" || saved.StudentStatements == nil {
				t.Fatal(v.Summary, err)
			}
		})
	}
}
