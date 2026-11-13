package memory_test

import (
	"context"
	"errors"
	"github.com/cloudwego/eino/schema"
	memstore "github.com/musi-ai/education/internal/adapters/persistence/inmemory/agentstore"
	"github.com/musi-ai/education/internal/agent/memory"
	ctxbuild "github.com/musi-ai/education/internal/agent/runtime/context"
	"github.com/musi-ai/education/internal/agent/runtime/state"
	"github.com/musi-ai/education/internal/agent/runtime/storage/contracttest"
	"strings"
	"testing"
)

type summarizer struct {
	calls  int
	source memory.SummarySource
	err    error
	text   string
}

func TestEightyPercentUsesProviderUsage(t *testing.T) {
	for _, tc := range []struct {
		name   string
		tokens int
		want   bool
	}{{"below", 9830, false}, {"at", 9831, true}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			s := memstore.New()
			scope := state.Scope{Namespace: "test", SubjectID: "student", ConversationID: tc.name}
			p := state.DefaultPolicy()
			for n := 1; n <= 6; n++ {
				in := contracttest.Input(scope, n)
				if e := s.Begin(ctx, in); e != nil {
					t.Fatal(e)
				}
				if e := s.SaveSnapshot(ctx, in, state.Snapshot{Identity: in.Identity, Phase: "assembled", Policy: p, Usage: state.ContextUsage{InputTokens: tc.tokens, Source: "provider"}}); e != nil {
					t.Fatal(e)
				}
				r := &state.Receipt{ResultID: in.Identity.TurnID, TurnID: in.Identity.TurnID, RunID: in.Identity.RunID}
				if e := s.Finish(ctx, in, "succeeded", "", r, []*schema.Message{schema.UserMessage("问题"), schema.AssistantMessage("答案", nil)}); e != nil {
					t.Fatal(e)
				}
			}
			m := memory.Manager{Store: s, Counter: ctxbuild.ConservativeCounter{}, Summarizer: &summarizer{}}
			if ok, e := m.Schedule(ctx, scope, p, false); e != nil || ok != tc.want {
				t.Fatal(ok, e)
			}
		})
	}
}

func TestDurableSealedEventsRecoverSchedulingAfterCrash(t *testing.T) {
	s := memstore.New()
	scope := state.Scope{Namespace: "test", SubjectID: "student", ConversationID: "dispatch"}
	p := state.DefaultPolicy()
	p.KeepRecentTurns = 1
	p.TriggerPercent = 1
	ctx := context.Background()
	for n := 1; n <= 4; n++ {
		in := contracttest.Input(scope, n)
		if e := s.Begin(ctx, in); e != nil {
			t.Fatal(e)
		}
		if e := s.SaveSnapshot(ctx, in, state.Snapshot{Identity: in.Identity, Phase: "assembled", Policy: p}); e != nil {
			t.Fatal(e)
		}
		r := &state.Receipt{ResultID: in.Identity.TurnID, TurnID: in.Identity.TurnID, RunID: in.Identity.RunID}
		if e := s.Finish(ctx, in, "succeeded", "", r, []*schema.Message{schema.UserMessage("math"), schema.AssistantMessage("feedback", nil)}); e != nil {
			t.Fatal(e)
		}
	}
	sm := &summarizer{}
	m := memory.Manager{Store: s, Counter: ctxbuild.ConservativeCounter{}, Summarizer: sm}
	for n := 0; n < 4; n++ {
		if ok, e := m.DispatchOne(ctx); !ok || e != nil {
			t.Fatal("lost sealed event", e)
		}
	}
	if ok, e := m.DispatchOne(ctx); ok || e != nil {
		t.Fatal("event not acknowledged", e)
	}
	if _, e := m.ProcessOne(ctx, "restarted-worker"); e != nil {
		t.Fatal(e)
	}
	v, e := s.View(ctx, scope)
	if e != nil || v.Summary.ThroughCursor != 3 || sm.calls != 1 {
		t.Fatal("recovery duplicated or missed summary", e)
	}
}

func (s *summarizer) Summarize(_ context.Context, src memory.SummarySource, _ int) (string, error) {
	s.calls++
	s.source = src
	if s.text == "" {
		return "保留原始卡点和待继续问题，不作为学习证据", s.err
	}
	return s.text, s.err
}
func TestAsyncCompressionKeepsRawHistoryAndRecentTurns(t *testing.T) {
	s := memstore.New()
	scope := state.Scope{Namespace: "test", SubjectID: "student", ConversationID: "memory"}
	for n := 1; n <= 6; n++ {
		contracttest.Seal(t, s, contracttest.Input(scope, n))
	}
	p := state.DefaultPolicy()
	p.KeepRecentTurns = 2
	summary := &summarizer{}
	m := memory.Manager{Store: s, Counter: ctxbuild.ConservativeCounter{}, Summarizer: summary}
	ctx := context.Background()
	scheduled, e := m.Schedule(ctx, scope, p, true)
	if e != nil || !scheduled || summary.calls != 0 {
		t.Fatal("synchronous summary on request", e)
	}
	if _, e = m.ProcessOne(ctx, "worker"); e != nil {
		t.Fatal(e)
	}
	v, e := s.View(ctx, scope)
	if e != nil || len(v.Turns) != 6 || v.Summary.ThroughCursor != 4 || len(summary.source.Turns) != 4 {
		t.Fatal("history deleted or recent turns compressed", e)
	}
	for _, turn := range summary.source.Turns {
		if e = state.ValidateProtocol(turn.Messages); e != nil {
			t.Fatal(e)
		}
	}
	snap, e := (ctxbuild.Builder{Counter: ctxbuild.ConservativeCounter{}}).Build(ctx, state.Input{}, state.Spec{Instruction: "fixed math", History: p}, state.Binding{SourceVersion: "v", Input: schema.UserMessage("continue")}, v)
	if e != nil || len(snap.HistoryCursors) != 2 || snap.HistoryCursors[0] != 5 {
		t.Fatal("summary coverage loaded twice", e)
	}
}
func TestSummaryFailurePreservesUsableOriginals(t *testing.T) {
	for _, bad := range []string{"provider failure", "oversized output"} {
		t.Run(bad, func(t *testing.T) {
			s := memstore.New()
			scope := state.Scope{Namespace: "test", SubjectID: "student", ConversationID: bad}
			for n := 1; n <= 3; n++ {
				contracttest.Seal(t, s, contracttest.Input(scope, n))
			}
			p := state.DefaultPolicy()
			p.KeepRecentTurns = 1
			sm := &summarizer{}
			if bad == "provider failure" {
				sm.err = errors.New("network down")
			} else {
				sm.text = strings.Repeat("x", 10000)
			}
			m := memory.Manager{Store: s, Counter: ctxbuild.ConservativeCounter{}, Summarizer: sm}
			ctx := context.Background()
			if _, e := m.Schedule(ctx, scope, p, true); e != nil {
				t.Fatal(e)
			}
			if _, e := m.ProcessOne(ctx, "w"); e == nil {
				t.Fatal("invalid summary published")
			}
			v, e := s.View(ctx, scope)
			if e != nil || v.Summary.Revision != 0 || len(v.Turns) != 3 {
				t.Fatal("failed summary destroyed memory")
			}
		})
	}
}
