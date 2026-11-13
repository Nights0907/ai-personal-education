package agentstore

import (
	"context"
	"encoding/json"
	"github.com/musi-ai/education/internal/adapters/httpapi"
	"github.com/musi-ai/education/internal/agent/runtime/state"
	"github.com/musi-ai/education/internal/platform/idgen"
	"github.com/musi-ai/education/internal/query"
	"github.com/musi-ai/education/web"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestHTTPConversationIssuanceBeforeMessage(t *testing.T) {
	s := testStore(t)
	server := httptest.NewServer((httpapi.Server{Repo: NewQueries(s), StudentID: "alice", Assets: web.Files}).Handler())
	defer server.Close()
	create := func() query.Conversation {
		t.Helper()
		r, e := http.Post(server.URL+"/api/v1/conversations", "application/json", strings.NewReader(`{"client_request_id":"http-create"}`))
		if e != nil {
			t.Fatal(e)
		}
		defer r.Body.Close()
		var c query.Conversation
		if r.StatusCode != 200 || json.NewDecoder(r.Body).Decode(&c) != nil || !idgen.Is("c", c.ConversationID) {
			t.Fatal("invalid creation response", r.StatusCode, c)
		}
		return c
	}
	c := create()
	if retry := create(); retry != c {
		t.Fatal("creation retry changed identity", c, retry)
	}
	for _, tc := range []struct {
		conversation string
		want         int
	}{{"client-invented-conversation", 404}, {c.ConversationID, 201}} {
		body := `{"query":"x+1=2","conversation_id":"` + tc.conversation + `","client_request_id":"http-message"}`
		r, e := http.Post(server.URL+"/api/v1/messages", "application/json", strings.NewReader(body))
		if e != nil {
			t.Fatal(e)
		}
		if r.StatusCode != tc.want {
			t.Fatal("unexpected message status", r.StatusCode, tc.want)
		}
		r.Body.Close()
	}
}

func TestConcurrentConversationCreationAndMessageReplay(t *testing.T) {
	s := testStore(t)
	q := NewQueries(s)
	ctx := context.Background()
	if _, _, err := q.Create(ctx, "alice", query.Input{Query: "禁止由客户端自建会话", ConversationID: "client-chosen", ClientRequestID: "invalid-create"}); err != query.ErrNotFound {
		t.Fatal("client-created conversation accepted", err)
	}
	var wg sync.WaitGroup
	results := make(chan query.Conversation, 8)
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, e := q.CreateConversation(ctx, "alice", "create-once")
			if e != nil {
				t.Error(e)
				return
			}
			results <- c
		}()
	}
	wg.Wait()
	close(results)
	var first query.Conversation
	for c := range results {
		if first.ConversationID == "" {
			first = c
		}
		if c != first {
			t.Fatal("retry created another conversation", first, c)
		}
	}
	if !idgen.Is("c", first.ConversationID) || !idgen.Is("b", first.BranchID) {
		t.Fatal(first)
	}
	in := query.Input{Query: "x+1=2", ConversationID: first.ConversationID, ClientRequestID: "message-once"}
	record, created, e := q.Create(ctx, "alice", in)
	if e != nil || !created || !idgen.Is("mu", record.ID) || !idgen.Is("t", record.TurnID) {
		t.Fatal(record, e)
	}
	again, created, e := q.Create(ctx, "alice", in)
	if e != nil || created || again.ID != record.ID || again.TurnID != record.TurnID {
		t.Fatal("message retry reissued identity", again, e)
	}
	other, e := q.CreateConversation(ctx, "bob", "create-once")
	if e != nil || other.ConversationID == first.ConversationID {
		t.Fatal("creation key escaped student scope", other, e)
	}
	scope := state.Scope{Namespace: "teacher", SubjectID: "alice", ConversationID: first.ConversationID}
	var id uint64
	if e = s.DB.QueryRowContext(ctx, "SELECT id FROM agent_message WHERE scope_key=? AND message_id=?", scope.Key(), record.ID).Scan(&id); e != nil || id == 0 {
		t.Fatal(id, e)
	}
	if e = s.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	var stableID uint64
	if e = s.DB.QueryRowContext(ctx, "SELECT id FROM agent_message WHERE scope_key=? AND message_id=?", scope.Key(), record.ID).Scan(&stableID); e != nil || stableID != id {
		t.Fatal("migration renumbered row", stableID, e)
	}
	for _, table := range []string{"agent_conversation", "agent_message", "agent_branch", "agent_memory_compact"} {
		var extra, key string
		if e = s.DB.QueryRowContext(ctx, "SELECT EXTRA,COLUMN_KEY FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=? AND COLUMN_NAME='id'", table).Scan(&extra, &key); e != nil || extra != "auto_increment" || key != "PRI" {
			t.Fatal(table, extra, key, e)
		}
	}
}
func TestRegenerationSubmissionCreatesDurableBranch(t *testing.T) {
	s := testStore(t)
	q := NewQueries(s)
	ctx := context.Background()
	conversation, err := q.CreateConversation(ctx, "alice", "regen-demo")
	if err != nil {
		t.Fatal(err)
	}
	original, _, err := q.Create(ctx, "alice", query.Input{ConversationID: conversation.ConversationID, ClientRequestID: "regen-original", Query: "数列题", Mode: "guide"})
	if err != nil {
		t.Fatal(err)
	}
	input := query.Input{ConversationID: conversation.ConversationID, ClientRequestID: "regen-new", Query: original.Query, Mode: original.Mode, RegenerateOf: original.ID}
	regenerated, created, err := q.Create(ctx, "alice", input)
	if err != nil || !created {
		t.Fatal(err)
	}
	var branches int
	if err = s.DB.QueryRow("SELECT COUNT(*) FROM agent_branch WHERE op='REGENERATE'").Scan(&branches); err != nil || branches != 1 {
		t.Fatal("missing branch", branches, err)
	}
	repeated, created, err := q.Create(ctx, "alice", input)
	if err != nil || created || repeated.ID != regenerated.ID {
		t.Fatal("retry forked again", err)
	}
	if _, err = q.Get(ctx, "alice", original.ID); err != nil {
		t.Fatal("old reply input lost", err)
	}
}
