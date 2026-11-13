package agentstore

import (
	"context"
	"github.com/cloudwego/eino/schema"
	"github.com/musi-ai/education/internal/agent/runtime/state"
	"github.com/musi-ai/education/internal/agent/runtime/storage/contracttest"
	"strings"
	"testing"
)

func TestThreeMessageTablesPlusCompactAndNoFailedTrace(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var tables int
	if err := s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA=DATABASE()").Scan(&tables); err != nil || tables != 4 {
		t.Fatal(tables, err)
	}
	in := contracttest.Input(state.Scope{Namespace: "test", SubjectID: "alice", ConversationID: "atomic"}, 1)
	if err := s.Begin(ctx, in); err != nil {
		t.Fatal(err)
	}
	needle := "FAILED_PRIVATE_MATH_WORK_MUST_NOT_PERSIST"
	snap := state.Snapshot{Identity: in.Identity, Phase: "assembled", Policy: state.DefaultPolicy(), Messages: []*schema.Message{schema.UserMessage(needle)}}
	if err := s.SaveSnapshot(ctx, in, snap); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(ctx, in, 1, schema.AssistantMessage(needle, nil)); err != nil {
		t.Fatal(err)
	}
	if err := s.Finish(ctx, in, "failed", "TEST", nil, []*schema.Message{schema.UserMessage(needle), schema.AssistantMessage(needle, nil)}); err != nil {
		t.Fatal(err)
	}
	var messages int
	if err := s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM agent_message").Scan(&messages); err != nil || messages != 0 {
		t.Fatal("failed turn wrote messages", messages, err)
	}
	var ext string
	if err := s.DB.QueryRowContext(ctx, "SELECT ext FROM agent_conversation WHERE scope_key=?", in.Identity.Scope.Key()).Scan(&ext); err != nil || strings.Contains(ext, needle) {
		t.Fatal("failed buffer leaked into conversation ext", err)
	}
	if _, err := New(s.DB).LoadRecovery(ctx, in); err != state.ErrNotFound {
		t.Fatal("discarded trace survived restart", err)
	}
}

func TestRetireRefusesNonemptyOldTables(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.DB.ExecContext(ctx, "CREATE TABLE agent_store_runs(id INT PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.DB.ExecContext(ctx, "DROP TABLE IF EXISTS agent_store_runs") })
	if _, err := s.DB.ExecContext(ctx, "INSERT INTO agent_store_runs VALUES(1)"); err != nil {
		t.Fatal(err)
	}
	if err := s.RetireEmptyV2(ctx); err == nil {
		t.Fatal("nonempty old data dropped")
	}
	var count int
	if err := s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM agent_store_runs").Scan(&count); err != nil || count != 1 {
		t.Fatal("old data changed", count, err)
	}
	if _, err := s.DB.ExecContext(ctx, "DELETE FROM agent_store_runs"); err != nil {
		t.Fatal(err)
	}
	if err := s.RetireEmptyV2(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestOldAcknowledgementDoesNotEraseNewSeal(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	scope := state.Scope{Namespace: "test", SubjectID: "alice", ConversationID: "coalesced"}
	contracttest.Seal(t, s, contracttest.Input(scope, 1))
	old, err := s.NextSealed(ctx)
	if err != nil {
		t.Fatal(err)
	}
	contracttest.Seal(t, s, contracttest.Input(scope, 2))
	if err = s.AcknowledgeSealed(ctx, old); err != nil {
		t.Fatal(err)
	}
	latest, err := s.NextSealed(ctx)
	if err != nil || latest.TurnID != "t2" {
		t.Fatal("old ack erased newer wake-up", latest, err)
	}
}
