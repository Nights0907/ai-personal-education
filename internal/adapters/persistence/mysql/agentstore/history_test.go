package agentstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/musi-ai/education/internal/agent/runtime/state"
	"github.com/musi-ai/education/internal/agent/runtime/storage/contracttest"
)

func TestFrozenPagesForkAndCompaction(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	scope := state.Scope{Namespace: "v2", SubjectID: "alice", ConversationID: "fork"}
	for n := 1; n <= 6; n++ {
		contracttest.Seal(t, s, contracttest.Input(scope, n))
	}
	head, err := s.ReadHead(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	page, err := s.LoadPage(ctx, scope, head, nil, 2)
	if err != nil || len(page.Turns) != 2 || page.Next == nil {
		t.Fatal(page, err)
	}
	boundary := page.Turns[1].End
	contracttest.Seal(t, s, contracttest.Input(scope, 7))
	var ids []string
	for {
		for _, turn := range page.Turns {
			ids = append(ids, turn.TurnID)
		}
		if page.Next == nil {
			break
		}
		page, err = s.LoadPage(ctx, scope, head, page.Next, 2)
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(ids) != 6 {
		t.Fatalf("frozen page included later append: %v", ids)
	}
	head, err = s.ReadHead(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	j := state.SummaryJob{ID: state.Hash("before-fork"), Scope: scope, ThroughCursor: 1, Policy: state.DefaultPolicy(), State: "pending", ReadToken: head}
	if err = s.EnqueueSummary(ctx, j); err != nil {
		t.Fatal(err)
	}
	j, err = s.ClaimSummary(ctx, "test", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	child, err := s.Fork(ctx, scope, head, &boundary, "REGENERATE")
	if err != nil {
		t.Fatal(err)
	}
	if child.HistoryEpoch != head.HistoryEpoch+1 || child.Tip.Position != 0 {
		t.Fatal(child)
	}
	if _, err = s.LoadPage(ctx, scope, head, nil, 2); !errors.Is(err, state.ErrConflict) {
		t.Fatal("old epoch accepted", err)
	}
	if err = s.PublishSummary(ctx, j, state.Summary{Revision: 1, ThroughCursor: 1, Content: "obsolete", PolicyVersion: j.Policy.Version}); err != nil {
		t.Fatal(err)
	}
	for n := 8; n <= 9; n++ {
		contracttest.Seal(t, s, contracttest.Input(scope, n))
	}
	v, err := s.View(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Turns) != 4 || v.Turns[2].TurnID != "t8" || v.Turns[2].End.Position != 2 || v.Summary.Content != "" {
		t.Fatal("fork path mixed with abandoned suffix", v)
	}
	// An active child can own a summary whose covered cursor belongs to an ancestor.
	j = state.SummaryJob{ID: state.Hash("child-ancestor-prefix"), Scope: scope, ThroughCursor: 1, Policy: state.DefaultPolicy(), State: "pending", ReadToken: v.Token}
	if err = s.EnqueueSummary(ctx, j); err != nil {
		t.Fatal(err)
	}
	j, err = s.ClaimSummary(ctx, "test", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.PublishSummary(ctx, j, state.Summary{Revision: 1, ThroughCursor: 1, Content: "covered first root turn", PolicyVersion: j.Policy.Version}); err != nil {
		t.Fatal(err)
	}
	v, err = s.ContextView(ctx, scope)
	if err != nil || len(v.Turns) != 3 || v.Turns[0].TurnID != "t2" || v.Turns[2].TurnID != "t9" {
		t.Fatal("ancestor cursor lost child turns", v, err)
	}
	c, e := readConversation(ctx, s.DB, scope)
	if e != nil {
		t.Fatal(e)
	}
	if c.SummaryToken.BranchID == c.SummaryThrough.BranchID || c.SummaryToken.BranchID != child.BranchID {
		t.Fatal("summary owner/cursor lost", c)
	}
	var pairs int
	if err = s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM agent_message WHERE scope_key=?", scope.Key()).Scan(&pairs); err != nil || pairs != 18 {
		t.Fatal("source history was destroyed", pairs, err)
	}
	bad := v.Turns[0].End
	bad.PrefixHash = "forged"
	if _, err = s.LoadPage(ctx, scope, v.Token, &bad, 2); !errors.Is(err, state.ErrConflict) {
		t.Fatal("forged cursor accepted", err)
	}
}

func TestTombstoneAndPendingPrefix(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	scope := state.Scope{Namespace: "v2", SubjectID: "alice", ConversationID: "deleted"}
	in := contracttest.Input(scope, 1)
	in.Lease.Until = time.Now().Add(50 * time.Millisecond)
	if err := s.Begin(ctx, in); err != nil {
		t.Fatal(err)
	}
	time.Sleep(60 * time.Millisecond)
	if err := s.Begin(ctx, contracttest.Input(scope, 2)); !errors.Is(err, state.ErrConflict) {
		t.Fatal("unreconciled prefix bypassed", err)
	}
	if err := s.Forget(ctx, scope); err != nil {
		t.Fatal(err)
	}
	if err := s.Forget(ctx, scope); err != nil {
		t.Fatal(err)
	}
	if err := s.Begin(ctx, contracttest.Input(scope, 3)); !errors.Is(err, state.ErrConflict) {
		t.Fatal("deleted conversation resurrected", err)
	}
	var count int
	if err := s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM agent_message WHERE scope_key=?", scope.Key()).Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
	var status string
	if err := s.DB.QueryRowContext(ctx, "SELECT status FROM agent_conversation WHERE scope_key=?", scope.Key()).Scan(&status); err != nil || status != "DELETED" {
		t.Fatal(status, err)
	}
}
