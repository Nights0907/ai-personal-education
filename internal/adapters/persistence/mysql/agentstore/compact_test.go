package agentstore

import (
	"context"
	"errors"
	"github.com/musi-ai/education/internal/agent/memory"
	ctxbuild "github.com/musi-ai/education/internal/agent/runtime/context"
	"github.com/musi-ai/education/internal/agent/runtime/state"
	"github.com/musi-ai/education/internal/agent/runtime/storage/contracttest"
	"github.com/musi-ai/education/internal/query"
	"testing"
)

type compactTestModel struct{}

func (compactTestModel) Summarize(context.Context, memory.SummarySource, int) (string, error) {
	return "保留学生问题和教师解答，待继续验证。", nil
}

func TestInspectPendingInputAndChangedPagination(t *testing.T) {
	s := testStore(t)
	q := fixtureQueries(s)
	ctx := context.Background()
	for _, id := range []string{"pending-one", "pending-two"} {
		if _, _, e := q.Create(ctx, "alice", query.Input{Query: "尚未回答的学生原文", ConversationID: "pending-history", ClientRequestID: id}); e != nil {
			t.Fatal(e)
		}
	}
	page, e := q.Inspect(ctx, "alice", "pending-history", "", 1)
	if e != nil || page.Total != 2 || page.Messages[0].Role != "user" || page.Messages[0].InActiveChain || page.Messages[0].CoveredByCompact {
		t.Fatal(page, e)
	}
	if page.ActiveChainMessages != 0 || page.CompactedMessages != 0 || page.ContextMessages != 0 {
		t.Fatal("pending inputs counted as model history", page)
	}
	if _, _, e = q.Create(ctx, "alice", query.Input{Query: "新增输入", ConversationID: "pending-history", ClientRequestID: "pending-three"}); e != nil {
		t.Fatal(e)
	}
	if _, e = q.Inspect(ctx, "alice", "pending-history", page.NextCursor, 1); !errors.Is(e, query.ErrConflict) {
		t.Fatal("pagination silently mixed snapshots", e)
	}
}

func TestInspectorForkDoesNotApplyPriorBranchSummary(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	scope := state.Scope{Namespace: "teacher", SubjectID: "alice", ConversationID: "summary-preview-fork"}
	for n := 1; n <= 6; n++ {
		contracttest.Seal(t, s, contracttest.Input(scope, n))
	}
	m := memory.Manager{Store: s, Counter: ctxbuild.ConservativeCounter{}, ModelRef: "same-model", Summarizer: compactTestModel{}}
	if _, e := m.Schedule(ctx, scope, state.DefaultPolicy(), true); e != nil {
		t.Fatal(e)
	}
	if _, e := m.ProcessOne(ctx, "test"); e != nil {
		t.Fatal(e)
	}
	head, e := s.ReadHead(ctx, scope)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Fork(ctx, scope, head, nil, "REGENERATE"); e != nil {
		t.Fatal(e)
	}
	page, e := NewQueries(s).Inspect(ctx, "alice", scope.ConversationID, "", 100)
	if e != nil || page.Total != 12 || page.CurrentSummary.Revision != 0 || page.CompactedMessages != 0 || page.ContextMessages != 0 || page.CompactVersions != 1 {
		t.Fatal("prior branch summary treated as current", page, e)
	}
}

func TestMigrateInlineSummaryToCompactTable(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	scope := state.Scope{Namespace: "teacher", SubjectID: "alice", ConversationID: "inline-migration"}
	for n := 1; n <= 6; n++ {
		contracttest.Seal(t, s, contracttest.Input(scope, n))
	}
	m := memory.Manager{Store: s, Counter: ctxbuild.ConservativeCounter{}, ModelRef: "same-model", Summarizer: compactTestModel{}}
	if _, e := m.Schedule(ctx, scope, state.DefaultPolicy(), true); e != nil {
		t.Fatal(e)
	}
	if _, e := m.ProcessOne(ctx, "test"); e != nil {
		t.Fatal(e)
	}
	c, e := readConversation(ctx, s.DB, scope)
	if e != nil {
		t.Fatal(e)
	}
	summary, e := readSummary(ctx, s.DB, scope, c)
	if e != nil {
		t.Fatal(e)
	}
	summary.CompactID = ""
	c.Summary = summary
	// Restore the legacy inline shape in an isolated fixture database.
	if _, e = s.DB.ExecContext(ctx, "UPDATE agent_conversation SET ext=? WHERE scope_key=?", jsonText(c), scope.Key()); e != nil {
		t.Fatal(e)
	}
	if _, e = s.DB.ExecContext(ctx, "DELETE FROM agent_memory_compact WHERE scope_key=?", scope.Key()); e != nil {
		t.Fatal(e)
	}
	if e = s.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	v, e := s.ContextView(ctx, scope)
	if e != nil || v.Summary.CompactID == "" || v.Summary.Content != summary.Content || len(v.Turns) != 4 {
		t.Fatal(v, e)
	}
	page, e := fixtureQueries(s).Inspect(ctx, "alice", scope.ConversationID, "", 100)
	if e != nil || page.Total != 12 || page.CompactVersions != 1 {
		t.Fatal(page, e)
	}
}

func TestCompactVersionsPreserveRawMessagesAndInspection(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	scope := state.Scope{Namespace: "teacher", SubjectID: "alice", ConversationID: "compact-versions"}
	for n := 1; n <= 7; n++ {
		contracttest.Seal(t, s, contracttest.Input(scope, n))
	}
	rawHash := func() string {
		rows, e := s.DB.QueryContext(ctx, "SELECT message_id,content,content_hash,prefix_hash,created_at FROM agent_message WHERE scope_key=? ORDER BY message_id", scope.Key())
		if e != nil {
			t.Fatal(e)
		}
		defer rows.Close()
		var all []any
		for rows.Next() {
			var id, content, ch, ph, at string
			if e = rows.Scan(&id, &content, &ch, &ph, &at); e != nil {
				t.Fatal(e)
			}
			all = append(all, []string{id, content, ch, ph, at})
		}
		if e = rows.Err(); e != nil {
			t.Fatal(e)
		}
		return state.Hash(all)
	}
	before := rawHash()
	manager := memory.Manager{Store: s, Counter: ctxbuild.ConservativeCounter{}, ModelRef: "same-model", Summarizer: compactTestModel{}}
	p := state.DefaultPolicy()
	if ok, e := manager.Schedule(ctx, scope, p, true); !ok || e != nil {
		t.Fatal(ok, e)
	}
	if ok, e := manager.ProcessOne(ctx, "test"); !ok || e != nil {
		t.Fatal(ok, e)
	}
	if rawHash() != before {
		t.Fatal("compression changed original messages")
	}
	v, e := s.ContextView(ctx, scope)
	if e != nil || len(v.Turns) != 4 || v.Summary.CompactID == "" || v.Summary.ThroughCursor != 3 {
		t.Fatal(v, e)
	}
	var inline string
	if e = s.DB.QueryRowContext(ctx, "SELECT JSON_UNQUOTE(JSON_EXTRACT(ext,'$.Summary.Content')) FROM agent_conversation WHERE scope_key=?", scope.Key()).Scan(&inline); e != nil || inline != "" {
		t.Fatal("summary body duplicated in conversation", inline, e)
	}
	q := fixtureQueries(s)
	page, e := q.Inspect(ctx, "alice", scope.ConversationID, "", 3)
	if e != nil || page.Total != 14 || page.CompactVersions != 1 || page.NextCursor == "" {
		t.Fatal(page, e)
	}
	if page.ActiveChainMessages != 14 || page.CompactedMessages != 6 || page.ContextMessages != 8 {
		t.Fatal("coverage counts incorrectly limited to page", page)
	}
	var messages []query.StoredMessage
	messages = append(messages, page.Messages...)
	cursor := page.NextCursor
	for cursor != "" {
		page, e = q.Inspect(ctx, "alice", scope.ConversationID, cursor, 3)
		if e != nil {
			t.Fatal(e)
		}
		messages = append(messages, page.Messages...)
		cursor = page.NextCursor
	}
	covered := 0
	for _, m := range messages {
		if m.CoveredByCompact {
			covered++
		}
	}
	if len(messages) != 14 || covered != 6 {
		t.Fatal("inspection hid compacted messages", len(messages), covered)
	}
	if _, e = q.Inspect(ctx, "bob", scope.ConversationID, "", 10); !errors.Is(e, query.ErrNotFound) {
		t.Fatal("cross-student history exposed", e)
	}
	contracttest.Seal(t, s, contracttest.Input(scope, 8))
	if ok, e := manager.Schedule(ctx, scope, p, true); !ok || e != nil {
		t.Fatal(ok, e)
	}
	if ok, e := manager.ProcessOne(ctx, "test"); !ok || e != nil {
		t.Fatal(ok, e)
	}
	page, e = q.Inspect(ctx, "alice", scope.ConversationID, "", 100)
	if e != nil || page.Total != 16 || page.CompactVersions != 2 || page.CurrentSummary.Revision != 2 {
		t.Fatal(page, e)
	}
	if e = s.Migrate(ctx); e != nil {
		t.Fatal("migration is not idempotent", e)
	}
}
