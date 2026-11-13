package teaching

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/go-sql-driver/mysql"
	agentdesign "github.com/musi-ai/education/agent-design"
	"github.com/musi-ai/education/internal/adapters/httpapi"
	agentstore "github.com/musi-ai/education/internal/adapters/persistence/mysql/agentstore"
	"github.com/musi-ai/education/internal/agent/completion"
	"github.com/musi-ai/education/internal/agent/runtime/model/proxy"
	"github.com/musi-ai/education/internal/agent/runtime/state"
	"github.com/musi-ai/education/internal/application/agentic"
	"github.com/musi-ai/education/internal/modules/teaching/photo"
	"github.com/musi-ai/education/internal/modules/teaching/solve"
	"github.com/musi-ai/education/internal/query"
	"github.com/musi-ai/education/web"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func dbForTest(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("RUNTIME_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("isolated MySQL test DSN required")
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
	name := "teaching_test_" + state.Hash(time.Now().String())[:18]
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
	t.Cleanup(func() {
		db.Close()
		_, e := admin.Exec("DROP DATABASE " + name)
		if e != nil {
			t.Error(e)
		}
		admin.Close()
	})
	if e = agentstore.New(db).Migrate(context.Background()); e != nil {
		t.Fatal(e)
	}
	if e = New(db).Migrate(context.Background()); e != nil {
		t.Fatal(e)
	}
	return db
}

const candidate = `{"question":{"stem":"解方程 x+1=2。","type":"constructed_response","difficulty":"easy","options":[],"subquestions":[]},"solution":{"answer":[{"subquestion_id":null,"content":"x=1"}],"analysis":"等式两边同时减去1。","steps":[{"ordinal":1,"subquestion_id":null,"title":"移项求解","content":"x=2-1=1。"},{"ordinal":2,"subquestion_id":null,"title":"检验","content":"1+1=2，等式成立。"}],"knowledge_points":[{"code":"equations_inequalities","is_primary":true}]}}`

type testModel struct {
	wait   <-chan struct{}
	calls  atomic.Int32
	review bool
	reject bool
	bare   bool
}

func (m *testModel) Generate(ctx context.Context, ms []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	m.calls.Add(1)
	if m.wait != nil && m.calls.Load() == 1 {
		select {
		case <-m.wait:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if m.review {
		if m.reject {
			return schema.AssistantMessage(`{"passed":false,"issues":["拒绝错误候选"]}`, nil), nil
		}
		return schema.AssistantMessage(`{"passed":true,"issues":[]}`, nil), nil
	}
	if m.bare {
		return schema.AssistantMessage("我已保存答案", nil), nil
	}
	name, args := "read_file", `{"path":"/skills/math-solve/SKILL.md"}`
	last := ms[len(ms)-1]
	if last.Role == schema.Tool {
		switch last.ToolName {
		case "read_file":
			name, args = "get_task_input", "{}"
		case "get_task_input":
			var a struct {
				Assets []struct {
					AssetID string `json:"asset_id"`
				} `json:"assets"`
			}
			_ = json.Unmarshal([]byte(last.Content), &a)
			if len(a.Assets) > 0 {
				name, args = "recognize_input_image", `{"asset_id":"`+a.Assets[0].AssetID+`"}`
			} else {
				name, args = "get_knowledge_catalog", `{"topic_codes":[]}`
			}
		case "recognize_input_image":
			name, args = "get_knowledge_catalog", `{"topic_codes":[]}`
		case "get_knowledge_catalog":
			name, args = "validate_solution", `{"candidate":`+candidate+`}`
		case "validate_solution":
			var r struct {
				Passed  bool   `json:"passed"`
				DraftID string `json:"draft_id"`
			}
			_ = json.Unmarshal([]byte(last.Content), &r)
			if !r.Passed {
				return schema.AssistantMessage("未通过校验", nil), nil
			}
			name, args = "finalize_solution", `{"draft_id":"`+r.DraftID+`"}`
		}
	}
	return schema.AssistantMessage("", []schema.ToolCall{{ID: fmt.Sprintf("call-%d", m.calls.Load()), Type: "function", Function: schema.FunctionCall{Name: name, Arguments: args}}}), nil
}
func (*testModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, fmt.Errorf("not used")
}
func setup(t *testing.T, db *sql.DB, m, review *testModel, photoServices ...*photo.Service) (*solve.Service, *httptest.Server, *agentstore.Queries) {
	t.Helper()
	store := agentstore.New(db)
	queries := agentstore.NewQueries(store)
	service := &solve.Service{Repo: New(db), Queries: queries, Store: store, Reviewer: review, ModelRef: "test-model"}
	if len(photoServices) > 0 {
		service.Photos = photoServices[0]
		service.Repo.(*Repository).Photos = service.Photos
	}
	fs, _, e := agentdesign.SkillMount()
	if e != nil {
		t.Fatal(e)
	}
	proxyService, e := proxy.NewService(map[string]model.BaseChatModel{"solve": m})
	if e != nil {
		t.Fatal(e)
	}
	p, e := agentic.New(context.Background(), agentic.Dependencies{Store: store, Models: proxy.Resolver(proxyService), FileSystem: fs, Completion: completion.Receipts{Resolver: service}, Spec: state.Spec{Name: "testsolve", Version: "solve-test-v1", ModelRef: "solve", Instruction: "Solve via the registered skill and tools.", TerminalTools: []string{"finalize_solution"}, MaxIterations: 15, MaxToolCalls: 12, Timeout: time.Minute, History: state.Policy{MaxInputTokens: 65536, OutputReserve: 8192, TriggerPercent: 80, KeepRecentTurns: 4, MaxSummaryTokens: 2048, Version: "test"}}}, agentic.NewRegistry(solve.Module{Service: service}))
	if e != nil {
		t.Fatal(e)
	}
	server := httptest.NewServer((httpapi.Server{Photos: service.Photos, Solver: p, Teaching: service, Repo: queries, StudentID: "alice", Assets: web.Files}).Handler())
	t.Cleanup(server.Close)
	return service, server, queries
}
func TestSolveHTTPTransactionalPersistenceAndReplay(t *testing.T) {
	db := dbForTest(t)
	m, review := &testModel{}, &testModel{review: true}
	service, server, q := setup(t, db, m, review)
	conv, e := q.CreateConversation(context.Background(), "alice", "create-solve")
	if e != nil {
		t.Fatal(e)
	}
	original := "  解方程 x+1=2。\n"
	input := query.Input{Query: original, ConversationID: conv.ConversationID, ClientRequestID: "same-request"}
	body, _ := json.Marshal(input)
	var first solve.View
	for n := 0; n < 2; n++ {
		res, e := http.Post(server.URL+"/api/v1/solve", "application/json", strings.NewReader(string(body)))
		if e != nil {
			t.Fatal(e)
		}
		raw, _ := io.ReadAll(res.Body)
		res.Body.Close()
		var view solve.View
		if res.StatusCode != 200 || json.Unmarshal(raw, &view) != nil || !view.Committed {
			t.Fatalf("solve failed %d %s", res.StatusCode, raw)
		}
		if n == 0 {
			first = view
		} else if first.QuestionID != view.QuestionID {
			t.Fatal("retry created new question")
		}
	}
	if first.OriginalText != original || first.QuestionID == "" || len(first.Solution.Steps) != 2 || first.Solution.Steps[0].StepID == "" || review.calls.Load() != 1 || m.calls.Load() != 5 {
		t.Fatal(first, m.calls.Load(), review.calls.Load())
	}
	for table, want := range map[string]int{"teaching_question": 1, "teaching_solution": 1, "teaching_solution_step": 2, "teaching_solution_receipt": 1, "agent_message": 2} {
		var n int
		if e = db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); e != nil || n != want {
			t.Fatal(table, n, e)
		}
	}
	if _, e = service.Repo.Get(context.Background(), "bob", first.QuestionID); e != state.ErrNotFound {
		t.Fatal("cross-student result exposed", e)
	}
	saved, e := service.Repo.Find(context.Background(), state.Scope{Namespace: "teacher", SubjectID: "alice", ConversationID: conv.ConversationID}, first.TurnID)
	if e != nil {
		t.Fatal(e)
	}
	if saved.Receipt.Terminal.Validate() != nil || len(saved.Receipt.FrozenPrefix) == 0 {
		t.Fatal("receipt lacks durable recovery source")
	}
	if e = service.Repo.RepairPending(context.Background(), service.Repair); e != nil {
		t.Fatal(e)
	}
	turn, e := service.Store.GetTurn(context.Background(), saved.Input.Identity.Scope, first.TurnID)
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, message := range turn.Messages {
		for _, call := range message.ToolCalls {
			if call.Function.Name == "read_file" {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("skill was never read")
	}
	var answer, analysis string
	if e = db.QueryRow("SELECT CAST(answer AS CHAR),analysis FROM teaching_solution WHERE solution_id=?", first.SolutionID).Scan(&answer, &analysis); e != nil || !strings.Contains(answer, "x=1") || analysis != first.Solution.Analysis {
		t.Fatal("official columns do not match view", e)
	}
	var originalColumn string
	if e = db.QueryRow("SELECT original_text FROM teaching_question WHERE question_id=?", first.QuestionID).Scan(&originalColumn); e != nil || originalColumn != original {
		t.Fatal("original column changed", e)
	}
	rows, e := db.Query("SELECT step_id,ordinal,title,content FROM teaching_solution_step WHERE solution_id=? ORDER BY ordinal", first.SolutionID)
	if e != nil {
		t.Fatal(e)
	}
	i := 0
	for rows.Next() {
		var id, title, content string
		var ordinal int
		if e = rows.Scan(&id, &ordinal, &title, &content); e != nil {
			t.Fatal(e)
		}
		if id != first.Solution.Steps[i].StepID || ordinal != i+1 || title != first.Solution.Steps[i].Title || content != first.Solution.Steps[i].Content {
			t.Fatal("step columns differ from official view")
		}
		i++
	}
	if e = rows.Err(); e != nil {
		t.Fatal(e)
	}
	rows.Close()
	if i != len(first.Solution.Steps) {
		t.Fatal("missing steps")
	}
}
func TestUnreviewedAndTextOnlyCannotCommit(t *testing.T) {
	for _, bare := range []bool{false, true} {
		t.Run(fmt.Sprint(bare), func(t *testing.T) {
			db := dbForTest(t)
			m := &testModel{bare: bare}
			_, server, q := setup(t, db, m, &testModel{review: true, reject: true})
			c, e := q.CreateConversation(context.Background(), "alice", "fail")
			if e != nil {
				t.Fatal(e)
			}
			b, _ := json.Marshal(query.Input{Query: "解方程 x+1=2。", ConversationID: c.ConversationID, ClientRequestID: "fail"})
			r, e := http.Post(server.URL+"/api/v1/solve", "application/json", strings.NewReader(string(b)))
			if e != nil {
				t.Fatal(e)
			}
			r.Body.Close()
			if r.StatusCode != 503 {
				t.Fatal(r.StatusCode)
			}
			var n int
			_ = db.QueryRow("SELECT COUNT(*) FROM teaching_question").Scan(&n)
			if n != 0 {
				t.Fatal("unvalidated solution committed")
			}
			_ = db.QueryRow("SELECT COUNT(*) FROM agent_message WHERE role='user'").Scan(&n)
			if n != 1 {
				t.Fatal("original question lost")
			}
		})
	}
}

func TestCommitRollsBackOnStepFailure(t *testing.T) {
	db := dbForTest(t)
	if _, e := db.Exec("CREATE TRIGGER fail_step BEFORE INSERT ON teaching_solution_step FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='injected step write failure'"); e != nil {
		t.Fatal(e)
	}
	_, server, q := setup(t, db, &testModel{}, &testModel{review: true})
	c, e := q.CreateConversation(context.Background(), "alice", "rollback")
	if e != nil {
		t.Fatal(e)
	}
	b, _ := json.Marshal(query.Input{Query: "解方程 x+1=2。", ConversationID: c.ConversationID, ClientRequestID: "rollback"})
	response, e := http.Post(server.URL+"/api/v1/solve", "application/json", strings.NewReader(string(b)))
	if e != nil {
		t.Fatal(e)
	}
	response.Body.Close()
	if response.StatusCode != 503 {
		t.Fatal(response.StatusCode)
	}
	for _, table := range []string{"teaching_question", "teaching_solution", "teaching_solution_step", "teaching_solution_receipt"} {
		var n int
		if e = db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); e != nil || n != 0 {
			t.Fatal("partial business commit survived", table, n, e)
		}
	}
}

func TestCommittedReceiptRepairsCanonicalHistoryAfterRestart(t *testing.T) {
	db := dbForTest(t)
	ctx := context.Background()
	store := agentstore.New(db)
	q := agentstore.NewQueries(store)
	repo := New(db)
	c, e := q.CreateConversation(ctx, "alice", "recovery")
	if e != nil {
		t.Fatal(e)
	}
	raw := "  解方程 x+1=2。\n"
	record, _, e := q.Create(ctx, "alice", query.Input{Query: raw, ConversationID: c.ConversationID, ClientRequestID: "recovery"})
	if e != nil {
		t.Fatal(e)
	}
	in := state.Input{Identity: state.Identity{Scope: state.Scope{Namespace: "teacher", SubjectID: "alice", ConversationID: c.ConversationID}, TurnID: record.TurnID, RunID: "recovery-run"}, InputRef: record.ID, BindingRef: record.ID, ConfigRef: "recovery", Lease: state.Lease{Token: 1, Until: time.Now().Add(time.Minute)}}
	if e = store.Begin(ctx, in); e != nil {
		t.Fatal(e)
	}
	canonical := schema.UserMessage("<current-problem>trusted source</current-problem>\n<student-input>" + raw + "</student-input>")
	if e = store.SaveSnapshot(ctx, in, state.Snapshot{Identity: in.Identity, Phase: "assembled", OriginalInput: schema.UserMessage(raw), CanonicalInput: canonical, Policy: state.DefaultPolicy()}); e != nil {
		t.Fatal(e)
	}
	var payload solve.Candidate
	if e = json.Unmarshal([]byte(candidate), &payload); e != nil {
		t.Fatal(e)
	}
	draft, e := repo.SaveDraft(ctx, in, solve.SourceHash(record), payload, solve.Review{Passed: true})
	if e != nil {
		t.Fatal(e)
	}
	args := `{"draft_id":"` + draft + `"}`
	call := state.TerminalObservation{CallID: "commit-before-crash", ToolName: "finalize_solution", Arguments: args, ArgumentsHash: state.Hash(args)}
	prefix := []*schema.Message{canonical, schema.AssistantMessage("", []schema.ToolCall{{ID: call.CallID, Type: "function", Function: schema.FunctionCall{Name: call.ToolName, Arguments: args}}})}
	saved, e := repo.Commit(ctx, in, draft, "test-model", call, prefix)
	if e != nil {
		t.Fatal(e)
	}
	// No Finish call: discard the entire process-local snapshot buffer.
	restarted := &solve.Service{Repo: New(db), Store: agentstore.New(db)}
	if e = restarted.Repo.RepairPending(ctx, restarted.Repair); e != nil {
		t.Fatal(e)
	}
	turn, e := restarted.Store.GetTurn(ctx, in.Identity.Scope, in.Identity.TurnID)
	if e != nil || state.ValidateProtocol(turn.Messages) != nil {
		t.Fatal(turn, e)
	}
	stored, e := q.Get(ctx, "alice", record.ID)
	if e != nil || stored.Query != raw || stored.Status != "ANSWERED" {
		t.Fatal("raw input was changed during recovery", stored, e)
	}
	duplicate, e := repo.Commit(ctx, in, draft, "test-model", call, prefix)
	if e != nil || duplicate.View.QuestionID != saved.View.QuestionID {
		t.Fatal("committed retry reissued result", duplicate, e)
	}
	view, e := repo.Get(ctx, "alice", saved.View.QuestionID)
	if e != nil || view.HistoryStatus != "SEALED" {
		t.Fatal(view, e)
	}
}

func TestSavedQuestionSurvivesConversationDeletion(t *testing.T) {
	db := dbForTest(t)
	service, server, q := setup(t, db, &testModel{}, &testModel{review: true})
	ctx := context.Background()
	c, err := q.CreateConversation(ctx, "alice", "delete-question-test")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(query.Input{Query: "解方程 x+1=2。", ConversationID: c.ConversationID, ClientRequestID: "saved-before-delete"})
	response, err := http.Post(server.URL+"/api/v1/solve", "application/json", strings.NewReader(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var v solve.View
	if err = json.NewDecoder(response.Body).Decode(&v); err != nil || !v.Committed {
		t.Fatal("solve", err, response.StatusCode)
	}
	if err = q.DeleteConversation(ctx, "alice", c.ConversationID); err != nil {
		t.Fatal(err)
	}
	saved, err := service.Repo.Get(ctx, "alice", v.QuestionID)
	if err != nil || saved.SolutionID != v.SolutionID {
		t.Fatal("saved question hidden after deletion", saved, err)
	}
	if _, err = service.Repo.Get(ctx, "bob", v.QuestionID); !errors.Is(err, state.ErrNotFound) {
		t.Fatal("cross-student access", err)
	}
	list, err := service.Repo.List(ctx, "alice", 100)
	if err != nil || len(list) != 0 {
		t.Fatal("deleted chat must not reappear via history projection", err)
	}
}
