// Command seed-student simulates a real student's multi-session usage and
// accumulates genuine L0/L1/L2/L3 memory data through the real pipeline
// (Discover -> Claim -> BuildMonth -> Publish), using a deterministic extractor
// so no live LLM is required. The result is viewable in the profile page.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/go-sql-driver/mysql"
	store "github.com/musi-ai/education/internal/adapters/persistence/mysql/agentstore"
	redisdocs "github.com/musi-ai/education/internal/adapters/persistence/redis/studentmemory"
	sqlmemory "github.com/musi-ai/education/internal/adapters/persistence/mysql/studentmemory"
	"github.com/musi-ai/education/internal/agent/runtime/state"
	mem "github.com/musi-ai/education/internal/modules/student/memory"
	"github.com/musi-ai/education/internal/modules/teaching/solve"
	"github.com/musi-ai/education/internal/platform/idgen"
	"github.com/musi-ai/education/internal/query"
	"github.com/redis/go-redis/v9"
)

func strings64() string { return "0000000000000000000000000000000000000000000000000000000000000000" }

// A turn the seed plays back: what the student asked, when, and the extracted
// topic that the deterministic extractor will produce.
type turnSpec struct {
	query     string // student message
	assistant string // teacher reply
	area      string // taxonomy area code
	topic     string // knowledge point
	ability   string // ability code
	mastery   string // suggested mastery
	incorrect bool   // whether the student erred this turn
	at        time.Time
}

func main() {
	student := "demo-xiaoming"
	dsn := os.Getenv("RUNTIME_MYSQL_DSN")
	url := os.Getenv("MUSI_REDIS_URL")
	if dsn == "" || url == "" {
		fmt.Println("need RUNTIME_MYSQL_DSN and MUSI_REDIS_URL")
		os.Exit(1)
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		panic(err)
	}
	cfg.ParseTime = true
	cfg.Loc = time.UTC
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		panic(err)
	}
	ctx := context.Background()
	opts, err := redis.ParseURL(url)
	if err != nil {
		panic(err)
	}
	opts.Protocol = 2
	opts.DisableIdentity = true
	client := redis.NewClient(opts)

	memRepo := &sqlmemory.Repository{DB: db}
	docs := &redisdocs.Documents{Client: client}
	queries := store.NewQueries(store.New(db))

	// --- L0: basic profile ---
	if _, err := memRepo.SaveBasic(ctx, student, mem.BasicProfile{
		Name:                  "小明",
		Grade:                 "高一",
		LearningGoals:         []string{"攻克函数与导数", "提升立体几何空间想象", "解决数列求和易错点"},
		ExplanationPreference: "希望讲解步骤详细，多用画图辅助理解",
	}, 0); err != nil && err != mem.ErrConflict {
		fmt.Println("SaveBasic:", err)
	}

	now := time.Now().UTC()
	// Timelines: a mastered topic learned recently, a partial topic from ~3 weeks
	// ago (already forgotten), a familiar topic from ~6 weeks ago (deeply forgotten).
	turns := []turnSpec{
		{query: "解方程 x^2-5x+6=0，我算出来 x=2 和 x=3，对吗？", assistant: "完全正确，因式分解 (x-2)(x-3)=0。", area: "ALG", topic: "因式分解", ability: "AB-COMPUTE", mastery: mem.MasteryMastered, incorrect: false, at: now.Add(-2 * 24 * time.Hour)},
		{query: "解方程 x^2-4x+4=0，我漏了一个根，只有 x=2。", assistant: "这是完全平方式，注意重根要写 x=2（二重根）。", area: "ALG", topic: "一元二次方程求根", ability: "AB-COMPUTE", mastery: mem.MasteryPartial, incorrect: true, at: now.Add(-20 * 24 * time.Hour)},
		{query: "求 f(x)=x^2-4x+3 在 [0,3] 上的最值，我算最小值是 -1。", assistant: "对称轴 x=2 在区间内，端点也要比，最小 -1 最大 3。", area: "FUNC", topic: "二次函数最值", ability: "AB-REASON", mastery: mem.MasteryFamiliar, incorrect: true, at: now.Add(-42 * 24 * time.Hour)},
		{query: "等差数列 a3=5, a7=13，求通项，我算 a1=1, d=2。", assistant: "对，an=2n-1。", area: "SEQ", topic: "等差数列通项", ability: "AB-COMPUTE", mastery: mem.MasteryMastered, incorrect: false, at: now.Add(-2 * 24 * time.Hour)},
	}

	// --- Insert each turn as a real SEALED user+assistant pair ---
	conv, err := queries.CreateConversation(ctx, student, idgen.New("req"))
	if err != nil {
		panic(err)
	}
	conversation := conv.ConversationID
	for i, ts := range turns {
		at := ts.at
		// Create the user message through the real query path.
		r, _, err := queries.Create(ctx, student, query.Input{
			ClientRequestID: idgen.New("req"),
			ConversationID:  conversation,
			Query:           ts.query,
		})
		if err != nil {
			panic(err)
		}
		scope := (state.Scope{Namespace: "teacher", SubjectID: student, ConversationID: conversation}).Key()
		var branch string
		if err := db.QueryRow("SELECT current_branch_id FROM agent_conversation WHERE scope_key=?", scope).Scan(&branch); err != nil {
			panic(err)
		}
		if _, err := db.Exec("UPDATE agent_message SET status='SEALED',created_at=? WHERE message_id=?", at, r.ID); err != nil {
			panic(err)
		}
		original, _ := json.Marshal(schema.UserMessage(r.Query))
		canonical, _ := json.Marshal(map[string]any{"items": []*schema.Message{schema.UserMessage("<student-input>\n" + r.Query + "\n</student-input>")}})
		if _, err := db.Exec("UPDATE agent_message SET content=?,ext=JSON_SET(ext,'$.OriginalInput',CAST(? AS JSON)) WHERE message_id=?", string(canonical), string(original), r.ID); err != nil {
			panic(err)
		}
		content, _ := json.Marshal(map[string]any{"items": []*schema.Message{schema.AssistantMessage(ts.assistant, nil)}})
		if _, err := db.Exec(`INSERT INTO agent_message(scope_key,message_id,branch_id,position,version,role,status,content,ext,turn_id,run_id,turn_cursor,content_hash,prefix_hash,created_at) VALUES(?,?,?,NULL,1,'assistant','SEALED',?,'{}',?,'fixture',1,?,'',?)`, scope, idgen.New("ma"), branch, string(content), r.TurnID, strings64(), at); err != nil {
			panic(err)
		}
		// Attach teaching_question + solution receipt so the turn carries a
		// question_id AND the question is fully openable from the profile page
		// via GET /api/v1/questions/{qid} (mirrors the real solve.Commit flow).
		qid := idgen.New("q")
		sid := idgen.New("s")
		rid := idgen.New("r")
		q := solve.Question{Stem: ts.query, Type: "constructed_response", Difficulty: solve.DifficultyMedium}
		sol := solve.Solution{
			Answer:   []solve.Answer{{Content: "见解析"}},
			Analysis: ts.assistant,
			Steps:    []solve.Step{{StepID: idgen.New("st"), Ordinal: 1, Title: "解题", Content: ts.assistant}},
			KnowledgePoints: []solve.Knowledge{{Code: "equations_inequalities", IsPrimary: true}},
		}
		qjson, _ := json.Marshal(q)
		if _, err := db.Exec(`INSERT INTO teaching_question(question_id,student_id,scope_key,turn_id,source_message_id,original_text,source_hash,input_type,question_json,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, qid, student, scope, r.TurnID, r.ID, ts.query, strings64(), "text", string(qjson), at); err != nil {
			panic(err)
		}
		answer, _ := json.Marshal(sol.Answer)
		kps, _ := json.Marshal(sol.KnowledgePoints)
		if _, err := db.Exec(`INSERT INTO teaching_solution(solution_id,question_id,answer,analysis,knowledge_points,model_ref,review_status,schema_version,created_at) VALUES(?,?,?,?,?,?,?,?,?)`, sid, qid, string(answer), sol.Analysis, string(kps), "seed", "model_review_passed", "math-solution-v1", at); err != nil {
			panic(err)
		}
		for _, st := range sol.Steps {
			if _, err := db.Exec(`INSERT INTO teaching_solution_step(step_id,solution_id,ordinal,subquestion_id,title,content) VALUES(?,?,?,?,?,?)`, st.StepID, sid, st.Ordinal, st.SubquestionID, st.Title, st.Content); err != nil {
				panic(err)
			}
		}
		view := solve.View{Committed: true, ResultID: rid, QuestionID: qid, SolutionID: sid, ConversationID: conversation, SourceMessageID: r.ID, TurnID: r.TurnID, OriginalText: ts.query, InputType: "text", Question: q, Solution: sol, ModelRef: "seed", ReviewStatus: "model_review_passed", CreatedAt: at, HistoryStatus: "SEALED"}
		viewJSON, _ := json.Marshal(view)
		inJSON, _ := json.Marshal(state.Input{Identity: state.Identity{Scope: state.Scope{Namespace: "teacher", SubjectID: student, ConversationID: conversation}, TurnID: r.TurnID, RunID: "fixture"}, InputRef: r.ID, BindingRef: r.ID})
		receiptJSON, _ := json.Marshal(state.Receipt{Kind: "business", ResultID: rid, TurnID: r.TurnID, RunID: "fixture", CommittedAt: at})
		if _, err := db.Exec(`INSERT INTO teaching_solution_receipt(result_id,scope_key,turn_id,student_id,question_id,solution_id,input_json,receipt_json,view_json,history_status,repair_due_at,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, rid, scope, r.TurnID, student, qid, sid, string(inJSON), string(receiptJSON), string(viewJSON), "SEALED", at, at); err != nil {
			panic(err)
		}
		_ = i
	}

	// --- Run the real extraction pipeline with a deterministic extractor ---
	svc := &mem.Service{Repo: memRepo, Documents: docs, Extractor: deterministicExtractor{}}
	if err := memRepo.Discover(ctx); err != nil {
		panic(err)
	}
	// Claim + process each batch (a single claim carries up to 10 turns).
	for {
		claim, err := memRepo.Claim(ctx, time.Now())
		if err == mem.ErrNotFound {
			break
		}
		if err != nil {
			panic(err)
		}
		if err := processClaim(ctx, svc, claim); err != nil {
			panic(err)
		}
	}

	// --- Report what was produced ---
	plan, err := svc.RecallPlan(ctx, student, time.Now())
	if err != nil {
		fmt.Println("RecallPlan:", err)
		return
	}
	fmt.Printf("学生 %s 档案已生成，共 %d 个知识点，due %d 个：\n", student, len(plan.Items), plan.DueCount)
	for _, it := range plan.Items {
		due := " "
		if it.Due {
			due = "★"
		}
		fmt.Printf("  %s %-16s 掌握=%-9s 犯错=%d R=%.3f last_seen=%s\n", due, it.Topic, it.Mastery, it.ErrorCount, it.Retrievability, it.LastSeen)
	}
}

// processClaim runs the same steps memory.Service.process does, but inlined to
// avoid depending on unexported internals.
func processClaim(ctx context.Context, svc *mem.Service, claim mem.Claim) error {
	months := map[string][]mem.OriginalTurn{}
	order := []string{}
	for _, t := range claim.Turns {
		if _, ok := months[t.Month]; !ok {
			order = append(order, t.Month)
		}
		months[t.Month] = append(months[t.Month], t.Raw)
	}
	for _, month := range order {
		old, err := svc.Documents.Load(ctx, claim.StudentID, month)
		if err != nil && err != mem.ErrNotFound {
			return err
		}
		seen := map[string]bool{}
		for _, raw := range old.Raw.Content.Turns {
			seen[mem.SourceID(raw.Source)] = true
		}
		fresh := []mem.OriginalTurn{}
		for _, raw := range months[month] {
			if !seen[mem.SourceID(raw.Source)] {
				fresh = append(fresh, raw)
				seen[mem.SourceID(raw.Source)] = true
			}
		}
		if len(fresh) == 0 {
			continue
		}
		result, err := svc.Extractor.Extract(ctx, old.Summary.Content, mem.QuestionContext{QuestionMeta: map[string]mem.QuestionMeta{}, RecentTopics: []mem.RecentTopic{}}, fresh)
		if err != nil {
			return err
		}
		next, err := mem.BuildMonth(ctx, claim.StudentID, month, old, fresh, result, time.Now())
		if err != nil {
			return err
		}
		if err = svc.Documents.Publish(ctx, claim.StudentID, month, old, next); err != nil {
			return err
		}
	}
	return svc.Repo.Complete(ctx, claim)
}

// deterministicExtractor produces authored topics derived from the student's
// own query text, so each turn maps to a distinct knowledge point.
type deterministicExtractor struct{}

func (deterministicExtractor) Extract(_ context.Context, _ mem.SummaryContent, _ mem.QuestionContext, turns []mem.OriginalTurn) (mem.Extracted, error) {
	out := mem.Extracted{Summary: "学生在复习高一核心知识点。"}
	seen := map[string]bool{}
	for _, t := range turns {
		q := t.User
		qid := t.Source.QuestionID
		var topic, area, ability, mastery string
		incorrect := false
		switch {
		case contains(q, "x^2-5x+6", "x=2", "x=3"):
			topic, area, ability, mastery = "因式分解", "ALG", "AB-COMPUTE", mem.MasteryMastered
		case contains(q, "漏了一个根", "x^2-4x+4"):
			topic, area, ability, mastery = "一元二次方程求根", "ALG", "AB-COMPUTE", mem.MasteryPartial
			incorrect = true
		case contains(q, "x^2-4x+3", "最值"):
			topic, area, ability, mastery = "二次函数最值", "FUNC", "AB-REASON", mem.MasteryFamiliar
			incorrect = true
		case contains(q, "等差数列", "通项"):
			topic, area, ability, mastery = "等差数列通项", "SEQ", "AB-COMPUTE", mem.MasteryMastered
		default:
			continue
		}
		key := area + "/" + topic
		if seen[key] {
			continue
		}
		seen[key] = true
		out.Topics = append(out.Topics, mem.ExtractedTopic{
			Area: area, Topic: topic, Ability: ability,
			Suggested: mastery, Authored: true, Incorrect: incorrect,
			QuestionID: qid, SourceIDs: []string{mem.SourceID(t.Source)},
		})
	}
	return out, nil
}

func contains(s string, subs ...string) bool {
	for _, sub := range subs {
		if !has(s, sub) {
			return false
		}
	}
	return true
}
func has(s, sub string) bool { return len(sub) > 0 && len(s) >= len(sub) && indexOf(s, sub) >= 0 }
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
