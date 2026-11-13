package agentstore

import (
	"context"
	"errors"
	"github.com/musi-ai/education/internal/query"
	"testing"
)

func TestDeleteConversationIsScopedAndDurable(t *testing.T) {
	q := NewQueries(testStore(t))
	ctx := context.Background()
	c, e := q.CreateConversation(ctx, "alice", "delete-test")
	if e != nil {
		t.Fatal(e)
	}
	_, _, e = q.Create(ctx, "alice", query.Input{Query: "测试原题", ConversationID: c.ConversationID, ClientRequestID: "delete-message"})
	if e != nil {
		t.Fatal(e)
	}
	if e = q.DeleteConversation(ctx, "bob", c.ConversationID); !errors.Is(e, query.ErrNotFound) {
		t.Fatal(e)
	}
	if e = q.DeleteConversation(ctx, "alice", c.ConversationID); e != nil {
		t.Fatal(e)
	}
	if e = q.DeleteConversation(ctx, "alice", c.ConversationID); e != nil {
		t.Fatal(e)
	}
	if _, e = q.Inspect(ctx, "alice", c.ConversationID, "", 100); !errors.Is(e, query.ErrNotFound) {
		t.Fatal(e)
	}
	_, _, e = q.Create(ctx, "alice", query.Input{Query: "不能复活", ConversationID: c.ConversationID, ClientRequestID: "after-delete"})
	if e == nil {
		t.Fatal("deleted conversation resurrected")
	}
}
