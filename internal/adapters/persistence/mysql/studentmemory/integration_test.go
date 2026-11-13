package studentmemory

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/go-sql-driver/mysql"
	store "github.com/musi-ai/education/internal/adapters/persistence/mysql/agentstore"
	"github.com/musi-ai/education/internal/adapters/persistence/mysql/teaching"
	redisdocs "github.com/musi-ai/education/internal/adapters/persistence/redis/studentmemory"
	"github.com/musi-ai/education/internal/agent/runtime/state"
	mem "github.com/musi-ai/education/internal/modules/student/memory"
	"github.com/musi-ai/education/internal/modules/teaching/solve"
	"github.com/musi-ai/education/internal/platform/idgen"
	"github.com/musi-ai/education/internal/query"
	"github.com/redis/go-redis/v9"
)

func integration(t *testing.T) (*Repository, *redisdocs.Documents) {
	t.Helper()
	dsn, url := os.Getenv("RUNTIME_TEST_MYSQL_DSN"), os.Getenv("MUSI_TEST_REDIS_URL")
	if dsn == "" || url == "" {
		t.Skip("isolated MySQL and Redis test configuration required")
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.DBName = ""
	admin, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	name := "student_memory_test_" + time.Now().Format("150405") + idgen.New("test")[8:18]
	if _, err = admin.Exec("CREATE DATABASE " + name); err != nil {
		t.Fatal(err)
	}
	cfg.DBName = name
	cfg.ParseTime = true
	cfg.Loc = time.UTC
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Close()
		if _, e := admin.Exec("DROP DATABASE " + name); e != nil {
			t.Error(e)
		}
		admin.Close()
	})
	ctx := context.Background()
	if err = store.New(db).Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = teaching.New(db).Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	repo := &Repository{DB: db}
	if _, err = db.Exec("CREATE TABLE student_profile (sid VARCHAR(255), details VARCHAR(255))"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO student_profile(sid,details) VALUES('legacy','keep')"); err != nil {
		t.Fatal(err)
	}
	if err = repo.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var legacy string
	if err = db.QueryRow("SELECT details FROM student_profile WHERE sid='legacy'").Scan(&legacy); err != nil || legacy != "keep" {
		t.Fatal("legacy profile changed")
	}
	opts, err := redis.ParseURL(url)
	if err != nil {
		t.Fatal(err)
	}
	opts.Protocol = 2
	opts.DisableIdentity = true
	client := redis.NewClient(opts)
	if err = client.Ping(ctx).Err(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	return repo, &redisdocs.Documents{Client: client}
}
func fixtureConversation(t *testing.T, repo *Repository, student string) string {
	t.Helper()
	c, err := store.NewQueries(store.New(repo.DB)).CreateConversation(context.Background(), student, idgen.New("req"))
	if err != nil {
		t.Fatal(err)
	}
	return c.ConversationID
}
func fixtureTurn(t *testing.T, repo *Repository, student, conversation string, created time.Time) string {
	t.Helper()
	ctx := context.Background()
	q := store.NewQueries(store.New(repo.DB))
	r, _, err := q.Create(ctx, student, query.Input{ClientRequestID: idgen.New("req"), ConversationID: conversation, Query: "我解方程时容易漏根，请帮我理解。"})
	if err != nil {
		t.Fatal(err)
	}
	scope := memScope(student, conversation)
	var branch string
	if err = repo.DB.QueryRow("SELECT current_branch_id FROM agent_conversation WHERE scope_key=?", scope).Scan(&branch); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.DB.Exec("UPDATE agent_message SET status='SEALED',created_at=? WHERE message_id=?", created, r.ID); err != nil {
		t.Fatal(err)
	}
	original, _ := json.Marshal(schema.UserMessage(r.Query))
	canonical, _ := json.Marshal(map[string]any{"items": []*schema.Message{schema.UserMessage("<student-input>\n" + r.Query + "\n</student-input>")}})
	if _, err = repo.DB.Exec("UPDATE agent_message SET content=?,ext=JSON_SET(ext,'$.OriginalInput',CAST(? AS JSON)) WHERE message_id=?", string(canonical), string(original), r.ID); err != nil {
		t.Fatal(err)
	}
	content, _ := json.Marshal(map[string]any{"items": []*schema.Message{schema.AssistantMessage("先检查每次变形是否等价。", nil)}})
	_, err = repo.DB.Exec(`INSERT INTO agent_message(scope_key,message_id,branch_id,position,version,role,status,content,ext,turn_id,run_id,turn_cursor,content_hash,prefix_hash,created_at) VALUES(?,?,?,NULL,1,'assistant','SEALED',?,'{}',?,'fixture',1,?,'',?)`, scope, idgen.New("ma"), branch, string(content), r.TurnID, strings64(), created)
	if err != nil {
		t.Fatal(err)
	}
	return r.TurnID
}
func strings64() string { return "0000000000000000000000000000000000000000000000000000000000000000" }
func memScope(student, conversation string) string {
	return (state.Scope{Namespace: "teacher", SubjectID: student, ConversationID: conversation}).Key()
}

type countingExtractor struct{ calls int }

func (e *countingExtractor) Extract(_ context.Context, _ mem.SummaryContent, _ mem.QuestionContext, turns []mem.OriginalTurn) (mem.Extracted, error) {
	e.calls++
	return mem.Extracted{Summary: "学生自述求根时会漏根，需验证。", Topics: []mem.ExtractedTopic{{Area: "ALG", Topic: "一元二次方程求根", Ability: "AB-COMPUTE", Suggested: mem.MasteryPartial, Authored: true, QuestionID: turns[0].Source.QuestionID, SourceIDs: []string{mem.SourceID(turns[0].Source)}}}}, nil
}

// capturingExtractor records the QuestionContext it receives so a test can
// assert the full "solve metadata -> mapping -> extractor input" chain.
type capturingExtractor struct {
	last mem.QuestionContext
}

func (e *capturingExtractor) Extract(_ context.Context, _ mem.SummaryContent, qc mem.QuestionContext, turns []mem.OriginalTurn) (mem.Extracted, error) {
	e.last = qc
	return mem.Extracted{Summary: "学生解一元二次方程漏根。", Topics: []mem.ExtractedTopic{{Area: "ALG", Topic: "一元二次方程求根", Ability: "AB-COMPUTE", Suggested: mem.MasteryPartial, Authored: true, Incorrect: true, QuestionID: turns[0].Source.QuestionID, SourceIDs: []string{mem.SourceID(turns[0].Source)}}}}, nil
}

// attachSolveView inserts a teaching_question + teaching_solution_receipt whose
// view_json carries the given knowledge points, so the memory repository's
// pending() JOIN can project them onto OriginalTurn.KnowledgeTags.
func attachSolveView(t *testing.T, repo *Repository, student, conversation, turnID string, kps []solve.Knowledge) {
	t.Helper()
	ctx := context.Background()
	scope := memScope(student, conversation)
	qid, sid, rid := idgen.New("q"), idgen.New("s"), idgen.New("r")
	question := solve.Question{Stem: "解方程 x^2-5x+6=0", Type: "constructed_response", Difficulty: solve.DifficultyMedium}
	qjson, _ := json.Marshal(question)
	if _, err := repo.DB.ExecContext(ctx, `INSERT INTO teaching_question(question_id,student_id,scope_key,turn_id,source_message_id,original_text,source_hash,input_type,question_json,created_at) VALUES(?,?,?,?,?,?,?,?,?,UTC_TIMESTAMP(6))`, qid, student, scope, turnID, idgen.New("m"), "解方程 x^2-5x+6=0", strings64(), "text", string(qjson)); err != nil {
		t.Fatal(err)
	}
	// This fixture represents a student attempt bound to the stored question.
	if _, err := repo.DB.ExecContext(ctx, `UPDATE agent_message SET ext=JSON_SET(ext,'$.GuideQuestionID',?) WHERE scope_key=? AND turn_id=? AND role='user'`, qid, scope, turnID); err != nil {
		t.Fatal(err)
	}
	view := solve.View{
		Committed:      true,
		ResultID:       rid,
		QuestionID:     qid,
		SolutionID:     sid,
		ConversationID: conversation,
		TurnID:         turnID,
		OriginalText:   "解方程 x^2-5x+6=0",
		InputType:      "text",
		Question:       question,
		Solution:       solve.Solution{Answer: []solve.Answer{{Content: "x=2,3"}}, Analysis: "因式分解", Steps: []solve.Step{{Ordinal: 1, Title: "因式分解", Content: "(x-2)(x-3)=0"}}, KnowledgePoints: kps},
		ReviewStatus:   "model_review_passed",
	}
	vjson, _ := json.Marshal(view)
	if _, err := repo.DB.ExecContext(ctx, `INSERT INTO teaching_solution_receipt(result_id,scope_key,turn_id,student_id,question_id,solution_id,input_json,receipt_json,view_json,history_status,repair_due_at,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,UTC_TIMESTAMP(6),UTC_TIMESTAMP(6))`, rid, scope, turnID, student, qid, sid, "{}", "{}", string(vjson), "PENDING"); err != nil {
		t.Fatal(err)
	}
}

func TestRealStorageThresholdSessionIdleAndCAS(t *testing.T) {
	repo, docs := integration(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	student := idgen.New("student")
	month := now.Add(8 * time.Hour).Format("2006-01")
	paths, _ := mem.MonthlyPaths(student, month)
	t.Cleanup(func() { _ = docs.Client.Del(ctx, paths.Index, paths.Summary, paths.Content).Err() })
	conversation := fixtureConversation(t, repo, student)
	model := &countingExtractor{}
	service := mem.Service{Repo: repo, Documents: docs, Extractor: model}
	for i := 0; i < 9; i++ {
		fixtureTurn(t, repo, student, conversation, now)
	}
	if worked, err := service.Step(ctx, now); err != nil || worked {
		t.Fatalf("nine turns unexpectedly triggered: %v %v", worked, err)
	}
	fixtureTurn(t, repo, student, conversation, now)
	if worked, err := service.Step(ctx, now); err != nil || !worked {
		t.Fatalf("ten turn extraction: %v %v", worked, err)
	}
	m, err := docs.Load(ctx, student, month)
	if err != nil || len(m.Raw.Content.Turns) != 10 {
		t.Fatalf("Redis raw: %d %v", len(m.Raw.Content.Turns), err)
	}
	if m.Raw.Content.Turns[0].User != "我解方程时容易漏根，请帮我理解。" || m.Raw.Content.Turns[0].QuestionText != "" {
		t.Fatal("runtime prompt wrapper leaked into raw student dialogue")
	}
	if ttl := docs.Client.TTL(ctx, paths.Content).Val(); ttl != -1 {
		t.Fatalf("memory unexpectedly expires: %v", ttl)
	}
	profile, err := repo.Profile(ctx, student)
	if err != nil || profile.Revision != 0 {
		t.Fatalf("extraction must not create L0 profile row (L0 is student-authored only): %+v %v", profile, err)
	}
	if _, err = repo.SaveBasic(ctx, student, mem.BasicProfile{Name: "测试学生", Grade: "高一"}, profile.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.SaveBasic(ctx, student, mem.BasicProfile{Name: "stale"}, profile.Revision); !errors.Is(err, mem.ErrConflict) {
		t.Fatal("stale basic profile overwrite accepted")
	}
	fixtureTurn(t, repo, student, conversation, now)
	_ = fixtureConversation(t, repo, student)
	if worked, err := service.Step(ctx, now); err != nil || !worked {
		t.Fatalf("new conversation trigger: %v %v", worked, err)
	}
	latest := fixtureConversation(t, repo, student)
	fixtureTurn(t, repo, student, latest, now.Add(-2*time.Hour))
	// Previous student inputs are also older than the idle boundary.
	if _, err = repo.DB.Exec("UPDATE agent_message SET created_at=? WHERE role='user'", now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if worked, err := service.Step(ctx, now); err != nil || !worked {
		t.Fatalf("two hour trigger: %v %v", worked, err)
	}
	m, err = docs.Load(ctx, student, month)
	if err != nil || len(m.Raw.Content.Turns) != 12 || model.calls != 3 {
		t.Fatalf("unexpected accumulated history: %d %d %v", len(m.Raw.Content.Turns), model.calls, err)
	}
	next := m
	next.Index.Revision++
	next.Summary.Revision++
	next.Raw.Revision++
	tampered := next
	tampered.Index.Content.Keywords = []string{"not-in-L2"}
	if err = docs.Publish(ctx, student, month, m, tampered); err == nil {
		t.Fatal("independent L1 content accepted")
	}
	if err = docs.Publish(ctx, student, month, m, next); err != nil {
		t.Fatal(err)
	}
	if err = docs.Publish(ctx, student, month, m, next); !errors.Is(err, mem.ErrConflict) {
		t.Fatalf("stale Redis write accepted: %v", err)
	}
	profile, _ = repo.Profile(ctx, student)
	if profile.Basic.Name != "测试学生" {
		t.Fatal("extraction overwrote student-entered identity")
	}
}

// TestQuestionMetadataAndHistoryReachExtractor verifies the end-to-end chain:
// a committed solve.View's knowledge points are projected onto the memory
// taxonomy, carried into OriginalTurn.KnowledgeTags, folded into QuestionContext
// (QuestionMeta + RecentTopics), and handed to the extractor as background.
func TestQuestionMetadataAndHistoryReachExtractor(t *testing.T) {
	repo, docs := integration(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	student := idgen.New("student")
	month := now.Add(8 * time.Hour).Format("2006-01")
	paths, _ := mem.MonthlyPaths(student, month)
	t.Cleanup(func() { _ = docs.Client.Del(ctx, paths.Index, paths.Summary, paths.Content).Err() })

	conversation := fixtureConversation(t, repo, student)
	// First turn: commit a solve view tagged with two knowledge codes, one of
	// which maps to ALG and one that is unknown (must be dropped, never guessed).
	turnID := fixtureTurn(t, repo, student, conversation, now)
	attachSolveView(t, repo, student, conversation, turnID, []solve.Knowledge{
		{Code: "equations_inequalities", IsPrimary: true},
		{Code: "not_a_real_code", IsPrimary: false},
	})

	captured := &capturingExtractor{}
	service := mem.Service{Repo: repo, Documents: docs, Extractor: captured}
	// Fire the extraction immediately via a new conversation trigger.
	fixtureTurn(t, repo, student, conversation, now)
	_ = fixtureConversation(t, repo, student)
	if worked, err := service.Step(ctx, now); err != nil || !worked {
		t.Fatalf("extraction trigger: %v %v", worked, err)
	}

	// The extractor must have received the mapped knowledge tag, the difficulty
	// tier, and the unknown code must be absent.
	var gotTag string
	var gotSourceID string
	var gotDifficulty string
	for sid, meta := range captured.last.QuestionMeta {
		if meta.QuestionID == "" {
			continue
		}
		gotSourceID = sid
		if meta.Difficulty != "" {
			gotDifficulty = meta.Difficulty
		}
		for _, tag := range meta.KnowledgeTags {
			if tag == "ALG" {
				gotTag = tag
			}
			if tag == "not_a_real_code" {
				t.Fatal("unknown solve code leaked into knowledge tags")
			}
		}
	}
	if gotTag != "ALG" {
		t.Fatalf("mapped knowledge tag not delivered to extractor: %+v", captured.last.QuestionMeta)
	}
	if gotDifficulty != solve.DifficultyMedium {
		t.Fatalf("difficulty not delivered to extractor: got %q, meta=%+v", gotDifficulty, captured.last.QuestionMeta)
	}
	if gotSourceID == "" {
		t.Fatal("no question metadata attached to any source")
	}

	// After the first commit, L2 holds one adjudicated topic; a second extraction
	// must expose it via RecentTopics (history aggregation reuses L2 anchors).
	second := &capturingExtractor{}
	service.Extractor = second
	fixtureTurn(t, repo, student, conversation, now)
	_ = fixtureConversation(t, repo, student)
	if worked, err := service.Step(ctx, now); err != nil || !worked {
		t.Fatalf("second extraction trigger: %v %v", worked, err)
	}
	found := false
	for _, rt := range second.last.RecentTopics {
		if rt.Topic == "一元二次方程求根" && rt.Area == "ALG" {
			found = true
			if rt.ErrorCount != 1 {
				t.Fatalf("history error count not carried: %+v", rt)
			}
		}
	}
	if !found {
		t.Fatalf("L2 topic not exposed as recent history: %+v", second.last.RecentTopics)
	}
}

func TestDurableEvidenceOutboxSurvivesWorkerRestart(t *testing.T) {
	repo, docs := integration(t)
	ctx := context.Background()
	student := idgen.New("student")
	c := fixtureConversation(t, repo, student)
	now := time.Now().UTC()
	turn := fixtureTurn(t, repo, student, c, now.Add(-3*time.Hour))
	attachSolveView(t, repo, student, c, turn, []solve.Knowledge{{Code: "equations_inequalities", IsPrimary: true}})
	s := &mem.Service{Repo: repo, Documents: docs, Extractor: &countingExtractor{}}
	if worked, e := s.Step(ctx, now); e != nil || !worked {
		t.Fatal(worked, e)
	}
	event, e := repo.PendingEvidence(ctx, now.Add(time.Second))
	if e != nil || event.StudentID != student || event.ConversationID != c || event.QuestionID == "" {
		t.Fatal("missing durable event", event, e)
	}
	calls := 0
	restarted := &mem.Service{Repo: repo, Documents: docs, AfterEvidence: func(context.Context, mem.EvidenceEvent) error {
		calls++
		if calls == 1 {
			return errors.New("injected delivery outage")
		}
		return nil
	}}
	if _, e = restarted.Step(ctx, now.Add(time.Second)); e == nil {
		t.Fatal("delivery outage not surfaced")
	}
	if _, e = restarted.Step(ctx, now.Add(2*time.Minute)); e != nil {
		t.Fatal(e)
	}
	if _, e = repo.PendingEvidence(ctx, now.Add(3*time.Minute)); !errors.Is(e, mem.ErrNotFound) {
		t.Fatal("event not acknowledged", e)
	}
}

func TestPhotographedAttemptProjectsBoundOCRAndActualFeedback(t *testing.T) {
	repo, _ := integration(t)
	ctx := context.Background()
	student := idgen.New("student")
	c := fixtureConversation(t, repo, student)
	now := time.Now().UTC()
	original := fixtureTurn(t, repo, student, c, now.Add(-time.Hour))
	attachSolveView(t, repo, student, c, original, []solve.Knowledge{{Code: "equations_inequalities", IsPrimary: true}})
	var qid string
	if e := repo.DB.QueryRow(`SELECT question_id FROM teaching_question WHERE student_id=?`, student).Scan(&qid); e != nil {
		t.Fatal(e)
	}
	reply := fixtureTurn(t, repo, student, c, now)
	asset := idgen.New("im")
	if _, e := repo.DB.Exec(`UPDATE agent_message SET ext=JSON_SET(ext,'$.Mode','review','$.GuideQuestionID',?,'$.ImageAssetID',?,'$.OriginalInput',CAST(? AS JSON)) WHERE turn_id=? AND role='user'`, qid, asset, `{"role":"user","content":"[图片题目]"}`, reply); e != nil {
		t.Fatal(e)
	}
	payload, _ := json.Marshal(map[string]any{"items": []*schema.Message{{Role: schema.Tool, ToolName: "recognize_review_image", Content: `{"asset_id":"` + asset + `","ocr_result_id":"ocr-bound","text":"我先移项，再因式分解，x=2或3"}`}, schema.AssistantMessage("请补充等价变形依据。", nil)}})
	if _, e := repo.DB.Exec(`UPDATE agent_message SET content=? WHERE turn_id=? AND role='assistant'`, string(payload), reply); e != nil {
		t.Fatal(e)
	}
	if e := repo.Discover(ctx); e != nil {
		t.Fatal(e)
	}
	rows, e := repo.pending(ctx, student)
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, row := range rows {
		if row.Raw.Source.TurnID == reply {
			found = true
			if row.Raw.AttemptUnavailable || row.Raw.AttemptOCRID != "ocr-bound" || row.Raw.AttemptOCR == "" || row.Raw.Assistant != "请补充等价变形依据。\n" || row.Raw.QuestionText == "" || row.Raw.InputType != "image" {
				t.Fatal("lost photographed evidence", row.Raw)
			}
		}
	}
	if !found {
		t.Fatal("missing photographed turn")
	}
}
