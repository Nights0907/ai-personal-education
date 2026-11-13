package teachingtasks

import (
	"context"
	"database/sql"
	"errors"
	"github.com/go-sql-driver/mysql"
	store "github.com/musi-ai/education/internal/adapters/persistence/mysql/agentstore"
	"github.com/musi-ai/education/internal/modules/teaching/tasks"
	"github.com/musi-ai/education/internal/platform/idgen"
	"github.com/musi-ai/education/internal/query"
	"os"
	"testing"
	"time"
)

func taskStore(t *testing.T) (*Repository, *store.Queries) {
	t.Helper()
	dsn := os.Getenv("RUNTIME_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("isolated MySQL configuration required")
	}
	cfg, e := mysql.ParseDSN(dsn)
	if e != nil {
		t.Fatal(e)
	}
	cfg.DBName = ""
	admin, e := sql.Open("mysql", cfg.FormatDSN())
	if e != nil {
		t.Fatal(e)
	}
	name := "teaching_tasks_test_" + idgen.New("test")[5:25]
	if _, e = admin.Exec("CREATE DATABASE " + name); e != nil {
		t.Fatal(e)
	}
	cfg.DBName = name
	cfg.ParseTime = true
	cfg.Loc = time.UTC
	db, e := sql.Open("mysql", cfg.FormatDSN())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close(); admin.Exec("DROP DATABASE " + name); admin.Close() })
	if e = store.New(db).Migrate(context.Background()); e != nil {
		t.Fatal(e)
	}
	repo := &Repository{DB: db}
	if e = repo.Migrate(context.Background()); e != nil {
		t.Fatal(e)
	}
	return repo, store.NewQueries(store.New(db))
}
func TestDurableTaskQueueReplayRecoveryAndOwnership(t *testing.T) {
	r, q := taskStore(t)
	ctx := context.Background()
	c, e := q.CreateConversation(ctx, "alice", "task-conversation")
	if e != nil {
		t.Fatal(e)
	}
	in := query.Input{Query: "求a10", Mode: "guide", ConversationID: c.ConversationID, ClientRequestID: "original"}
	task, e := r.Create(ctx, "alice", in, "")
	if e != nil {
		t.Fatal(e)
	}
	duplicate, e := r.Create(ctx, "alice", in, "")
	if e != nil || duplicate.ID != task.ID {
		t.Fatal("create not idempotent", duplicate, e)
	}
	changed := in
	changed.Query = "changed"
	if _, e = r.Create(ctx, "alice", changed, ""); !errors.Is(e, tasks.ErrConflict) {
		t.Fatal("request key accepted different content", e)
	}
	if _, e = r.Get(ctx, "bob", task.ID); !errors.Is(e, tasks.ErrNotFound) {
		t.Fatal("ownership check", e)
	}
	childIn := query.Input{Query: "我的思路", Mode: "guide", ConversationID: c.ConversationID, ClientRequestID: "answer"}
	child, e := r.Create(ctx, "alice", childIn, task.ID)
	if e != nil {
		t.Fatal(e)
	}
	running, e := r.Claim(ctx, time.Now().UTC())
	if e != nil || running.ID != task.ID {
		t.Fatal(running, e)
	}
	text, e := r.Emit(ctx, running, "guide_question", map[string]string{"text": "你准备先求什么？"})
	if e != nil || text == "" {
		t.Fatal(text, e)
	}
	if _, e = r.Claim(ctx, time.Now().UTC()); !errors.Is(e, tasks.ErrNotFound) {
		t.Fatal("child ran before reference", e)
	}
	restarted := &Repository{DB: r.DB}
	recovered, e := restarted.Claim(ctx, time.Now().UTC().Add(5*time.Minute))
	if e != nil || recovered.ID != running.ID || recovered.Token == running.Token {
		t.Fatal("expired run not reclaimed", recovered, e)
	}
	if _, e = r.Emit(ctx, running, "guide_question", map[string]string{"text": "迟到结果"}); !errors.Is(e, tasks.ErrConflict) {
		t.Fatal("old worker wrote", e)
	}
	text, e = restarted.Emit(ctx, recovered, "guide_question", map[string]string{"text": "不同的重试问题"})
	if e != nil || text != "你准备先求什么？" {
		t.Fatal("opening changed on retry", text, e)
	}
	events, e := r.Events(ctx, "alice", task.ID, 0)
	if e != nil || len(events) != 1 {
		t.Fatal("opening duplicated", events, e)
	}
	if e = r.Finish(ctx, recovered, map[string]string{"question_id": "q1"}, nil); e != nil {
		t.Fatal(e)
	}
	next, e := r.Claim(ctx, time.Now().UTC())
	if e != nil || next.ID != child.ID {
		t.Fatal("queued answer did not resume", next, e)
	}
	if e = r.Finish(ctx, next, nil, errors.New("retryable problem")); e != nil {
		t.Fatal(e)
	}
	if e = r.Retry(ctx, "alice", child.ID); e != nil {
		t.Fatal(e)
	}
	if e = q.DeleteConversation(ctx, "alice", c.ConversationID); e != nil {
		t.Fatal(e)
	}
	if _, e = r.Claim(ctx, time.Now().UTC()); !errors.Is(e, tasks.ErrNotFound) {
		t.Fatal("deleted conversation task ran", e)
	}
	if _, e = r.Get(ctx, "alice", task.ID); !errors.Is(e, tasks.ErrNotFound) {
		t.Fatal("deleted task visible", e)
	}
}
