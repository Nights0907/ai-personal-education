package recallplan

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/go-sql-driver/mysql"
	store "github.com/musi-ai/education/internal/adapters/persistence/mysql/agentstore"
	sqlmemory "github.com/musi-ai/education/internal/adapters/persistence/mysql/studentmemory"
	"github.com/musi-ai/education/internal/adapters/persistence/mysql/teaching"
	redisdocs "github.com/musi-ai/education/internal/adapters/persistence/redis/studentmemory"
	"github.com/musi-ai/education/internal/agent/runtime/state"
	mem "github.com/musi-ai/education/internal/modules/student/memory"
	rp "github.com/musi-ai/education/internal/modules/teaching/recallplan"
	"github.com/musi-ai/education/internal/modules/teaching/solve"
	"github.com/musi-ai/education/internal/platform/idgen"
	"github.com/musi-ai/education/internal/query"
	"github.com/redis/go-redis/v9"
)

// harness wires real MySQL + Redis for the full memory -> recall-plan chain.
type harness struct {
	db          *sql.DB
	memRepo     *sqlmemory.Repository
	memDocs     *redisdocs.Documents
	rpRepo      *Repository
	queries     *store.Queries
	redisClient *redis.Client
}

func setup(t *testing.T) *harness {
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
	name := "recall_plan_test_" + time.Now().Format("150405") + idgen.New("test")[8:18]
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
		_, _ = admin.Exec("DROP DATABASE " + name)
		admin.Close()
	})
	ctx := context.Background()
	if err = store.New(db).Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = teaching.New(db).Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	memRepo := &sqlmemory.Repository{DB: db}
	if err = memRepo.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	rpRepo := &Repository{DB: db}
	if err = rpRepo.Migrate(ctx); err != nil {
		t.Fatal(err)
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
	return &harness{
		db:          db,
		memRepo:     memRepo,
		memDocs:     &redisdocs.Documents{Client: client},
		rpRepo:      rpRepo,
		queries:     store.NewQueries(store.New(db)),
		redisClient: client,
	}
}

func (h *harness) scope(student, conversation string) string {
	return (state.Scope{Namespace: "teacher", SubjectID: student, ConversationID: conversation}).Key()
}

// fixtureTurn writes a SEALED user+assistant pair into agent_message so the
// memory Discover() picks it up, and attaches a teaching_question so the turn
// carries a question_id (as solve commits would). Returns the turn ID.
func (h *harness) fixtureTurn(t *testing.T, student, conversation string, created time.Time) string {
	t.Helper()
	ctx := context.Background()
	r, _, err := h.queries.Create(ctx, student, query.Input{ClientRequestID: idgen.New("req"), ConversationID: conversation, Query: "我解一元二次方程时经常漏根，请帮我看看。"})
	if err != nil {
		t.Fatal(err)
	}
	scope := h.scope(student, conversation)
	var branch string
	if err = h.db.QueryRow("SELECT current_branch_id FROM agent_conversation WHERE scope_key=?", scope).Scan(&branch); err != nil {
		t.Fatal(err)
	}
	if _, err = h.db.Exec("UPDATE agent_message SET status='SEALED',created_at=? WHERE message_id=?", created, r.ID); err != nil {
		t.Fatal(err)
	}
	original, _ := json.Marshal(schema.UserMessage(r.Query))
	canonical, _ := json.Marshal(map[string]any{"items": []*schema.Message{schema.UserMessage("<student-input>\n" + r.Query + "\n</student-input>")}})
	if _, err = h.db.Exec("UPDATE agent_message SET content=?,ext=JSON_SET(ext,'$.OriginalInput',CAST(? AS JSON)) WHERE message_id=?", string(canonical), string(original), r.ID); err != nil {
		t.Fatal(err)
	}
	content, _ := json.Marshal(map[string]any{"items": []*schema.Message{schema.AssistantMessage("先检查因式分解是否完整。", nil)}})
	if _, err = h.db.Exec(`INSERT INTO agent_message(scope_key,message_id,branch_id,position,version,role,status,content,ext,turn_id,run_id,turn_cursor,content_hash,prefix_hash,created_at) VALUES(?,?,?,NULL,1,'assistant','SEALED',?,'{}',?,'fixture',1,?,'',?)`, scope, idgen.New("ma"), branch, string(content), r.TurnID, strings64(), created); err != nil {
		t.Fatal(err)
	}
	// Attach a teaching_question so the turn carries a question_id (the JOIN in
	// memory's pending() relies on scope_key + turn_id).
	qid := idgen.New("q")
	q := solve.Question{Stem: "解方程 x^2-5x+6=0", Type: "constructed_response", Difficulty: solve.DifficultyMedium}
	qjson, _ := json.Marshal(q)
	if _, err := h.db.Exec(`INSERT INTO teaching_question(question_id,student_id,scope_key,turn_id,source_message_id,original_text,source_hash,input_type,question_json,created_at) VALUES(?,?,?,?,?,?,?,?,?,UTC_TIMESTAMP(6))`, qid, student, scope, r.TurnID, r.ID, "解方程 x^2-5x+6=0", strings64(), "text", string(qjson)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(`UPDATE agent_message SET ext=JSON_SET(ext,'$.GuideQuestionID',?) WHERE message_id=?`, qid, r.ID); err != nil {
		t.Fatal(err)
	}
	return r.TurnID
}

func strings64() string { return "0000000000000000000000000000000000000000000000000000000000000000" }

// deterministicExtractor produces a fixed authored topic so the test never
// depends on a live LLM.
type deterministicExtractor struct{}

func (deterministicExtractor) Extract(_ context.Context, _ mem.SummaryContent, _ mem.QuestionContext, turns []mem.OriginalTurn) (mem.Extracted, error) {
	qid := turns[0].Source.QuestionID
	return mem.Extracted{
		Summary: "学生自述解一元二次方程时会漏根，待巩固。",
		Topics: []mem.ExtractedTopic{{
			Area: "ALG", Topic: "一元二次方程求根", Ability: "AB-COMPUTE",
			Suggested: mem.MasteryPartial, Authored: true, Incorrect: true,
			QuestionID: qid, SourceIDs: []string{mem.SourceID(turns[0].Source)},
		}},
	}, nil
}

// TestFullMemoryToRecallChain is the end-to-end proof that long-term memory and
// the forgetting-prevention reminder genuinely work together on real MySQL+Redis:
//  1. A student turn is extracted into L2 with a forgetting anchor.
//  2. Time passes so the topic decays past the recall threshold.
//  3. RecallPlan flags it due; EnsurePlan auto-enrolls the student.
//  4. The daily scheduler delivers the question (new conversation, SENT).
//  5. The student answers; MarkReviewed removes it from the queue.
func TestFullMemoryToRecallChain(t *testing.T) {
	h := setup(t)
	ctx := context.Background()
	student := idgen.New("student")

	// --- Phase 1: extract a turn into L2 ---
	memSvc := &mem.Service{Repo: h.memRepo, Documents: h.memDocs, Extractor: deterministicExtractor{}}
	conv := mustConversation(t, h, student)
	now := time.Now().UTC().Truncate(time.Second)
	h.fixtureTurn(t, student, conv, now)

	// Trigger extraction via a new conversation (one turn + new conv => due).
	_ = mustConversation(t, h, student)
	if worked, err := memSvc.Step(ctx, now); err != nil || !worked {
		t.Fatalf("memory extraction: worked=%v err=%v", worked, err)
	}

	// L2 must now hold the topic with a forgetting anchor.
	month := now.Add(8 * time.Hour).Format("2006-01")
	m, err := h.memDocs.Load(ctx, student, month)
	if err != nil {
		t.Fatalf("load L2: %v", err)
	}
	if len(m.Summary.Content.KnowledgeAreas) != 1 || m.Summary.Content.KnowledgeAreas[0].Topics[0].Topic != "一元二次方程求根" {
		t.Fatalf("L2 missing topic: %+v", m.Summary.Content.KnowledgeAreas)
	}

	// --- Phase 2: time passes past the recall threshold ---
	// stability=0.5 (partial), difficulty=0.28 (error_count=1):
	// half_life = 1 + 14*0.5*(1-0.28) ≈ 6.04 days. After 20 days R << 0.80.
	later := now.Add(20 * 24 * time.Hour)

	// --- Phase 3: RecallPlan flags due, EnsurePlan auto-enrolls ---
	recall := rp.MemoryRecall{Service: memSvc}
	due, err := recall.DueItems(ctx, student, later)
	if err != nil {
		t.Fatalf("DueItems: %v", err)
	}
	if len(due) != 1 || !due[0].Due {
		t.Fatalf("expected 1 due topic, got %+v", due)
	}

	rpSvc := &rp.Service{Repo: h.rpRepo, Recall: recall}
	if err := rpSvc.EnsurePlan(ctx, student, later); err != nil {
		t.Fatalf("EnsurePlan: %v", err)
	}
	plan, err := h.rpRepo.Plan(ctx, student)
	if err != nil {
		t.Fatalf("plan not created: %v", err)
	}
	if plan.DailyQuota != 1 {
		t.Fatalf("expected quota 1, got %d", plan.DailyQuota)
	}

	// --- Phase 4: daily delivery ---
	// The due topic carries the turn's question_id as evidence; feed a fake
	// question source + a real conversation sink so delivery is deterministic.
	delivery := &rp.Service{
		Repo:          h.rpRepo,
		Recall:        recall,
		Questions:     fakeQuestionSource{stem: "解方程 x^2-5x+6=0"},
		Conversations: realSink{queries: h.queries},
		Model:         nil, // fallback opening, no LLM
	}
	// Set the clock to a time whose hour is >= the plan's send_hour so DuePlans
	// returns this plan (send_hour <= now.Hour()).
	deliveryAt := time.Date(later.Year(), later.Month(), later.Day(), plan.SendHour, 0, 0, 0, time.UTC)
	worked, err := delivery.Step(ctx, deliveryAt)
	if err != nil || !worked {
		t.Fatalf("delivery step: worked=%v err=%v", worked, err)
	}
	notifications := delivery.Status(ctx, student).Notifications
	if len(notifications) != 1 {
		t.Fatalf("expected one notification: %#v", notifications)
	}
	var beforeClaim int
	if err = h.db.QueryRow("SELECT COUNT(*) FROM recall_plan_item WHERE plan_id=? AND status='SENT'", plan.ID).Scan(&beforeClaim); err != nil || beforeClaim != 0 {
		t.Fatal("scheduler delivered before click", err)
	}
	claimed, err := delivery.ClaimNotification(ctx, student, notifications[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	var automaticReplies int
	if err = h.db.QueryRow("SELECT COUNT(*) FROM agent_message WHERE role='assistant' AND scope_key IN (SELECT scope_key FROM agent_conversation WHERE conversation_id=?)", claimed).Scan(&automaticReplies); err != nil {
		t.Fatal(err)
	}
	if automaticReplies != 0 {
		t.Fatal("claim triggered a teacher reply")
	}
	repeated, err := delivery.ClaimNotification(ctx, student, notifications[0].ID)
	if err != nil || claimed != repeated {
		t.Fatal("non-idempotent claim", err)
	}
	if _, err = delivery.ClaimNotification(ctx, "another-student", notifications[0].ID); err == nil {
		t.Fatal("cross-student claim")
	}
	// quota=1: the single due question was delivered and is no longer PENDING.
	pending, err := h.rpRepo.PendingItems(ctx, plan.ID, 10)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("expected 0 pending after delivery, got %d", len(pending))
	}
	// The delivered item is SENT and bound to a fresh conversation.
	var sentCount int
	if err = h.db.QueryRow("SELECT COUNT(*) FROM recall_plan_item WHERE plan_id=? AND status='SENT'", plan.ID).Scan(&sentCount); err != nil {
		t.Fatal(err)
	}
	if sentCount != 1 {
		t.Fatalf("expected 1 SENT item, got %d", sentCount)
	}

	// --- Phase 5: student answers -> MarkReviewed removes it from the queue ---
	// The delivered item's conversation is the fresh one we just opened; simulate
	// the student answering there by extracting a turn in that conversation.
	var convID string
	if err = h.db.QueryRow("SELECT conversation_id FROM recall_plan_item WHERE plan_id=? AND status='SENT'", plan.ID).Scan(&convID); err != nil {
		t.Fatal(err)
	}
	h.fixtureTurn(t, student, convID, deliveryAt.Add(time.Minute))
	_ = mustConversation(t, h, student)
	if worked, err := memSvc.Step(ctx, deliveryAt.Add(2*time.Minute)); err != nil || !worked {
		t.Fatalf("answer extraction: worked=%v err=%v", worked, err)
	}
	// AfterExtract -> OnExtract should mark the answered item REVIEWED.
	// We wire OnExtract manually here (in production it is the AfterExtract hook).
	rpSvc.OnExtract(ctx, student, []string{convID}, deliveryAt.Add(2*time.Minute))
	var reviewedCount int
	if err = h.db.QueryRow("SELECT COUNT(*) FROM recall_plan_item WHERE plan_id=? AND status='REVIEWED'", plan.ID).Scan(&reviewedCount); err != nil {
		t.Fatal(err)
	}
	if reviewedCount != 1 {
		t.Fatalf("expected 1 REVIEWED item, got %d", reviewedCount)
	}
}

func mustConversation(t *testing.T, h *harness, student string) string {
	t.Helper()
	c, err := h.queries.CreateConversation(context.Background(), student, idgen.New("req"))
	if err != nil {
		t.Fatal(err)
	}
	return c.ConversationID
}

type fakeQuestionSource struct{ stem string }

func (f fakeQuestionSource) Question(context.Context, string, string) (solve.View, error) {
	return solve.View{Question: solve.Question{Stem: f.stem, Type: "constructed_response", Difficulty: solve.DifficultyMedium}}, nil
}

type realSink struct{ queries *store.Queries }

func (r realSink) Open(ctx context.Context, student, requestID, message string) (string, error) {
	c, err := r.queries.CreateConversation(ctx, student, requestID)
	if err != nil {
		return "", err
	}
	if _, _, err := r.queries.Create(ctx, student, query.Input{
		Query: message, ConversationID: c.ConversationID, ClientRequestID: requestID,
	}); err != nil {
		return "", err
	}
	return c.ConversationID, nil
}

func TestTimezoneCyclesAndStalePending(t *testing.T) {
	h := setup(t)
	ctx := context.Background()
	student := idgen.New("student")
	p, e := h.rpRepo.UpsertPlan(ctx, student, 1, 19)
	if e != nil {
		t.Fatal(e)
	}
	loc := time.FixedZone("Asia/Shanghai", 8*3600)
	at := time.Date(2026, 11, 9, 19, 0, 0, 0, loc)
	due, e := h.rpRepo.DuePlans(ctx, at)
	if e != nil || len(due) != 1 {
		t.Fatal("local 19:00 not due", due, e)
	}
	early, e := h.rpRepo.DuePlans(ctx, at.Add(-time.Minute))
	if e != nil || len(early) != 1 {
		t.Fatal("notification refresh must not depend on send hour", early, e)
	}
	first, e := h.rpRepo.AcquirePlan(ctx, p, at)
	if e != nil || !first {
		t.Fatal(first, e)
	}
	second, e := h.rpRepo.AcquirePlan(ctx, p, at)
	if e != nil || second {
		t.Fatal("duplicate plan lease", second, e)
	}
	qid := idgen.New("q")
	item := rp.Item{QuestionID: qid, Area: "ALG", Topic: "求根", Retrievability: 0.1}
	n, e := h.rpRepo.AddItems(ctx, p.ID, []rp.Item{item})
	if e != nil || n != 1 {
		t.Fatal(n, e)
	}
	rows, e := h.rpRepo.PendingItems(ctx, p.ID, 1)
	if e != nil {
		t.Fatal(e)
	}
	if e = h.rpRepo.MarkSent(ctx, rows[0].ID, "review", at); e != nil {
		t.Fatal(e)
	}
	if e = h.rpRepo.MarkReviewed(ctx, rows[0].ID); e != nil {
		t.Fatal(e)
	}
	n, e = h.rpRepo.AddItems(ctx, p.ID, []rp.Item{item})
	if e != nil || n != 1 {
		t.Fatal("new review cycle blocked", n, e)
	}
	if e = h.rpRepo.SyncPending(ctx, p.ID, nil); e != nil {
		t.Fatal(e)
	}
	rows, e = h.rpRepo.PendingItems(ctx, p.ID, 10)
	if e != nil || len(rows) != 0 {
		t.Fatal("stale pending survived", rows, e)
	}
	if e = h.rpRepo.MarkRan(ctx, p.ID, "2026-11-09"); e != nil {
		t.Fatal(e)
	}
	due, e = h.rpRepo.DuePlans(ctx, at.Add(2*time.Hour))
	if e != nil || len(due) != 0 {
		t.Fatal("same local day redelivered", due, e)
	}
}

func (r realSink) OpenReview(ctx context.Context, student, request, message, question string) (string, error) {
	return (rp.AgentStoreSink{Queries: r.queries}).OpenReview(ctx, student, request, message, question)
}
