// Package contracttest exercises semantics shared by all runtime stores.
package contracttest

import (
	"context"
	"errors"
	"fmt"
	"github.com/cloudwego/eino/schema"
	"github.com/musi-ai/education/internal/agent/runtime/state"
	"github.com/musi-ai/education/internal/agent/runtime/storage"
	"sync"
	"testing"
	"time"
)

func Input(scope state.Scope, n int) state.Input {
	return state.Input{Identity: state.Identity{Scope: scope, TurnID: fmt.Sprintf("t%d", n), RunID: fmt.Sprintf("r%d", n)}, InputRef: fmt.Sprintf("i%d", n), BindingRef: fmt.Sprintf("b%d", n), ConfigRef: "test-v1", Lease: state.Lease{Token: int64(n), Until: time.Now().Add(time.Minute)}}
}
func Seal(t *testing.T, s storage.Store, in state.Input) {
	t.Helper()
	ctx := context.Background()
	if err := s.Begin(ctx, in); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveSnapshot(ctx, in, state.Snapshot{Identity: in.Identity, Phase: "assembled", Policy: state.DefaultPolicy()}); err != nil {
		t.Fatal(err)
	}
	receipt := &state.Receipt{ResultID: "result-" + in.Identity.TurnID, TurnID: in.Identity.TurnID, RunID: in.Identity.RunID}
	ms := []*schema.Message{schema.UserMessage("problem " + in.Identity.TurnID), schema.AssistantMessage("explanation", nil)}
	if err := s.Finish(ctx, in, "succeeded", "", receipt, ms); err != nil {
		t.Fatal(err)
	}
}
func Run(t *testing.T, newStore func(*testing.T) storage.Store) {
	t.Run("failed_event_does_not_block_other_students", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		Seal(t, s, Input(state.Scope{Namespace: "test", SubjectID: "alice", ConversationID: "events"}, 1))
		Seal(t, s, Input(state.Scope{Namespace: "test", SubjectID: "bob", ConversationID: "events"}, 1))
		first, e := s.NextSealed(ctx)
		if e != nil {
			t.Fatal(e)
		}
		if e = s.DeferSealed(ctx, first, "injected scheduling failure"); e != nil {
			t.Fatal(e)
		}
		second, e := s.NextSealed(ctx)
		if e != nil || second.ID == first.ID {
			t.Fatal("poison event blocked independent event", e)
		}
	})
	t.Run("forget_scoped_memory_and_events", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		a := state.Scope{Namespace: "test", SubjectID: "alice", ConversationID: "forget"}
		b := state.Scope{Namespace: "test", SubjectID: "bob", ConversationID: "forget"}
		Seal(t, s, Input(a, 1))
		Seal(t, s, Input(b, 1))
		if e := s.Forget(ctx, a); e != nil {
			t.Fatal(e)
		}
		v, e := s.View(ctx, a)
		if e != nil || len(v.Turns) != 0 {
			t.Fatal("forgotten memory remains", e)
		}
		v, e = s.View(ctx, b)
		if e != nil || len(v.Turns) != 1 {
			t.Fatal("forgot another student", e)
		}
		event, e := s.NextSealed(ctx)
		if e != nil || event.Scope != b {
			t.Fatal("deleted event remained", e)
		}
		if e = s.AcknowledgeSealed(ctx, event); e != nil {
			t.Fatal(e)
		}
		if _, e = s.NextSealed(ctx); !errors.Is(e, state.ErrNotFound) {
			t.Fatal("acknowledged event redelivered", e)
		}
	})
	t.Run("isolation_and_immutability", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		a := state.Scope{Namespace: "test", SubjectID: "alice", ConversationID: "conversation"}
		b := state.Scope{Namespace: "test", SubjectID: "bob", ConversationID: "conversation"}
		Seal(t, s, Input(a, 1))
		v, e := s.View(ctx, b)
		if e != nil || len(v.Turns) != 0 {
			t.Fatalf("cross-user leak: %+v %v", v, e)
		}
		v, e = s.View(ctx, a)
		if e != nil {
			t.Fatal(e)
		}
		v.Turns[0].Messages[0].Content = "modified"
		v, e = s.View(ctx, a)
		if e != nil || v.Turns[0].Messages[0].Content == "modified" {
			t.Fatal("store alias")
		}
	})
	t.Run("failed_history_excluded_and_retry_fenced", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		in := Input(state.Scope{Namespace: "test", SubjectID: "student", ConversationID: "retry"}, 1)
		if e := s.Begin(ctx, in); e != nil {
			t.Fatal(e)
		}
		if e := s.Begin(ctx, Input(in.Identity.Scope, 2)); !errors.Is(e, state.ErrConflict) {
			t.Fatal("overlapping conversation accepted", e)
		}
		if e := s.Finish(ctx, in, "failed", "test", nil, nil); e != nil {
			t.Fatal(e)
		}
		v, e := s.View(ctx, in.Identity.Scope)
		if e != nil || len(v.Turns) != 0 {
			t.Fatal("failed run used as memory")
		}
		retry := in
		retry.Identity.RunID = "r-retry"
		retry.Lease.Token++
		if e = s.Begin(ctx, retry); e != nil {
			t.Fatal(e)
		}
		if e = s.Append(ctx, in, 1, schema.AssistantMessage("old", nil)); !errors.Is(e, state.ErrConflict) {
			t.Fatal("old run not fenced", e)
		}
	})
	t.Run("protocol_and_idempotent_repair", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		in := Input(state.Scope{Namespace: "test", SubjectID: "student", ConversationID: "protocol"}, 1)
		if e := s.Begin(ctx, in); e != nil {
			t.Fatal(e)
		}
		r := &state.Receipt{ResultID: "result", TurnID: in.Identity.TurnID, RunID: in.Identity.RunID}
		bad := []*schema.Message{schema.UserMessage("x"), {Role: schema.Tool, ToolCallID: "orphan"}}
		if e := s.Finish(ctx, in, "succeeded", "", r, bad); !errors.Is(e, state.ErrProtocol) {
			t.Fatal("orphan result accepted", e)
		}
		good := []*schema.Message{schema.UserMessage("x"), schema.AssistantMessage("y", nil)}
		for n := 0; n < 2; n++ {
			if e := s.Finish(ctx, in, "succeeded", "", r, good); e != nil {
				t.Fatal(e)
			}
		}
		v, e := s.View(ctx, in.Identity.Scope)
		if e != nil || len(v.Turns) != 1 {
			t.Fatal("repair duplicated turn")
		}
	})
	t.Run("snapshot_and_audit_conflict", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		in := Input(state.Scope{Namespace: "test", SubjectID: "student", ConversationID: "snapshot"}, 1)
		if e := s.Begin(ctx, in); e != nil {
			t.Fatal(e)
		}
		v := state.Snapshot{Identity: in.Identity, Phase: "assembled", Hash: "a"}
		if e := s.SaveSnapshot(ctx, in, v); e != nil {
			t.Fatal(e)
		}
		v.Hash = "b"
		if e := s.SaveSnapshot(ctx, in, v); !errors.Is(e, state.ErrConflict) {
			t.Fatal(e)
		}
		v.Phase = "model_input"
		if e := s.SaveSnapshot(ctx, in, v); e != nil {
			t.Fatal(e)
		}
		if e := s.Append(ctx, in, 1, schema.AssistantMessage("a", nil)); e != nil {
			t.Fatal(e)
		}
		if e := s.Append(ctx, in, 1, schema.AssistantMessage("b", nil)); !errors.Is(e, state.ErrConflict) {
			t.Fatal(e)
		}
	})
	t.Run("summary_claim_exclusive_and_monotonic", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		scope := state.Scope{Namespace: "test", SubjectID: "student", ConversationID: "summary"}
		for n := 1; n <= 4; n++ {
			Seal(t, s, Input(scope, n))
		}
		for n := 1; n <= 2; n++ {
			j := state.SummaryJob{ID: state.Hash(fmt.Sprint(n)), Scope: scope, ThroughCursor: int64(n), State: "pending", Policy: state.DefaultPolicy()}
			if e := s.EnqueueSummary(ctx, j); e != nil {
				t.Fatal(e)
			}
		}
		var wg sync.WaitGroup
		claimed := make(chan state.SummaryJob, 2)
		for n := 0; n < 2; n++ {
			wg.Add(1)
			go func(n int) {
				defer wg.Done()
				j, e := s.ClaimSummary(ctx, fmt.Sprint(n), time.Minute)
				if e != nil {
					t.Error(e)
					return
				}
				claimed <- j
			}(n)
		}
		wg.Wait()
		close(claimed)
		var jobs []state.SummaryJob
		for j := range claimed {
			jobs = append(jobs, j)
		}
		if len(jobs) != 2 || jobs[0].ID == jobs[1].ID {
			t.Fatal("double claim")
		}
		if jobs[0].ThroughCursor < jobs[1].ThroughCursor {
			jobs[0], jobs[1] = jobs[1], jobs[0]
		}
		for _, j := range jobs {
			v := state.Summary{Revision: 1, ThroughCursor: j.ThroughCursor, Content: "summary", PolicyVersion: j.Policy.Version}
			if e := s.PublishSummary(ctx, j, v); e != nil {
				t.Fatal(e)
			}
		}
		v, e := s.View(ctx, scope)
		if e != nil || v.Summary.ThroughCursor != 2 || v.Summary.Revision != 1 {
			t.Fatal("summary moved backward", v, e)
		}
	})
}
