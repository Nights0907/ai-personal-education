package agentstore

import (
	"context"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/musi-ai/education/internal/agent/runtime/state"
	"github.com/musi-ai/education/internal/agent/runtime/storage/contracttest"
	"github.com/musi-ai/education/internal/query"
)

func TestPendingQuerySealReusesUserMessage(t *testing.T) {
	s := testStore(t)
	q := fixtureQueries(s)
	ctx := context.Background()
	first, _, err := q.Create(ctx, "alice", query.Input{Query: "  第一题 x+1=2\n  ", ConversationID: "pending", ClientRequestID: "pending-first", Mode: query.GuideMode, GuideQuestionID: "q-guided"})
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := q.Create(ctx, "alice", query.Input{Query: "第二题 x²=4", ConversationID: "pending", ClientRequestID: "pending-second"})
	if err != nil {
		t.Fatal(err)
	}
	scope := state.Scope{Namespace: "teacher", SubjectID: "alice", ConversationID: "pending"}
	// Answer order need not equal input arrival order. Allocate model-history
	// turn cursors when processing begins, not when a raw question is saved.
	for n, record := range []query.Record{second, first} {
		in := contracttest.Input(scope, 10+n)
		in.InputRef = record.ID
		if err = s.DB.QueryRowContext(ctx, "SELECT turn_id FROM agent_message WHERE message_id=?", record.ID).Scan(&in.Identity.TurnID); err != nil {
			t.Fatal(err)
		}
		if err = s.Begin(ctx, in); err != nil {
			t.Fatal(err)
		}
		if err = s.Finish(ctx, in, "failed", "TEST", nil, nil); err != nil {
			t.Fatal(err)
		}
		raw, e := q.Get(ctx, "alice", record.ID)
		if e != nil || raw.Query != record.Query || raw.Status != "RECEIVED" {
			t.Fatal("failure lost original user message", raw, e)
		}
		in.Identity.RunID += "-retry"
		in.Lease.Token++
		if err = s.Begin(ctx, in); err != nil {
			t.Fatal(err)
		}
		bad := []*schema.Message{schema.UserMessage("tampered question"), schema.AssistantMessage("answer", nil)}
		receipt := &state.Receipt{ResultID: "result-" + record.ID, TurnID: in.Identity.TurnID, RunID: in.Identity.RunID}
		if err = s.Finish(ctx, in, "succeeded", "", receipt, bad); err != state.ErrProtocol {
			t.Fatal("changed pending question accepted", err)
		}
		messages := []*schema.Message{schema.UserMessage(record.Query), schema.AssistantMessage("answer", nil)}
		if err = s.Finish(ctx, in, "succeeded", "", receipt, messages); err != nil {
			t.Fatal(err)
		}
		if err = s.Finish(ctx, in, "succeeded", "", receipt, messages); err != nil {
			t.Fatal("repair not idempotent", err)
		}
		raw, e = q.Get(ctx, "alice", record.ID)
		if e != nil || raw.ID != record.ID || raw.Status != "ANSWERED" || raw.Query != record.Query || raw.Mode != record.Mode || raw.GuideQuestionID != record.GuideQuestionID {
			t.Fatal(raw, e)
		}
	}
	v, err := s.View(ctx, scope)
	if err != nil || len(v.Turns) != 2 || v.Turns[0].Cursor >= v.Turns[1].Cursor {
		t.Fatal(v, err)
	}
	var count int
	if err = s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM agent_message").Scan(&count); err != nil || count != 4 {
		t.Fatal("duplicated the pending user record", count, err)
	}
}

func TestPendingQueryUpgradePreservesSealedHistory(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	scope := state.Scope{Namespace: "test", SubjectID: "alice", ConversationID: "old-schema"}
	contracttest.Seal(t, s, contracttest.Input(scope, 1))
	before, err := s.View(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.DB.ExecContext(ctx, `ALTER TABLE agent_message DROP INDEX uk_query_request,DROP INDEX idx_query_recent,DROP COLUMN status,DROP COLUMN subject_scope,DROP COLUMN client_request_id,MODIFY COLUMN position BIGINT UNSIGNED NOT NULL`)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	after, err := s.View(ctx, scope)
	if err != nil || state.Hash(before) != state.Hash(after) {
		t.Fatal("schema upgrade changed existing history", err)
	}
	old := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	r, _, err := fixtureQueries(s).Import(ctx, "alice", query.Input{Query: "历史原文", ConversationID: "imported", ClientRequestID: "old-request"}, old)
	if err != nil || !r.CreatedAt.Equal(old) {
		t.Fatal("legacy input time not preserved", r, err)
	}
	var tables int
	if err = s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA=DATABASE()").Scan(&tables); err != nil || tables != 4 {
		t.Fatal("query persistence added extra tables", tables, err)
	}
}
