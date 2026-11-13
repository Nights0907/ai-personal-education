package memory

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/musi-ai/education/internal/agent/runtime/state"
	"github.com/musi-ai/education/internal/application/agentic"
	"strings"
	"testing"
	"time"
)

func TestExtractionTriggers(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		n          int
		newSession bool
		last       time.Time
		want       string
	}{
		{0, true, now.Add(-3 * time.Hour), ""},
		{9, false, now, ""},
		{10, false, now, "ten_turns"},
		{1, true, now, "new_conversation"},
		{1, false, now.Add(-2*time.Hour + time.Second), ""},
		{1, false, now.Add(-2 * time.Hour), "idle_two_hours"},
		{1, false, time.Time{}, ""},
	} {
		if got := Due(tc.n, tc.newSession, tc.last, now); got != tc.want {
			t.Fatalf("%+v got %s", tc, got)
		}
	}
}

func rawFixture(turn string) OriginalTurn {
	return OriginalTurn{Source: Evidence{ConversationID: "c1", BranchID: "b1", TurnID: turn, MessageIDs: []string{"u" + turn, "a" + turn}, QuestionID: "q_" + turn}, User: "我解一元二次方程经常漏根", Assistant: "需要检查因式分解", CreatedAt: time.Date(2026, 10, 28, 1, 0, 0, 0, time.UTC), InputType: "text"}
}
func extraction(raw OriginalTurn) Extracted {
	return Extracted{Summary: "学生自述解方程时会漏根，尚待验证。", Topics: []ExtractedTopic{{Area: "ALG", Topic: "一元二次方程求根", Ability: "AB-COMPUTE", Suggested: MasteryPartial, Authored: true, QuestionID: raw.Source.QuestionID, SourceIDs: []string{SourceID(raw.Source)}}}}
}
func TestMonthPreservesRawAndAdjudicatesMastery(t *testing.T) {
	one := rawFixture("t1")
	first, err := BuildMonth(context.Background(), "s1", "2026-10", Month{}, []OriginalTurn{one}, extraction(one), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	two := rawFixture("t2")
	two.CreatedAt = one.CreatedAt.Add(time.Hour)
	e2 := extraction(two)
	e2.Topics[0].Suggested = MasteryMastered
	next, err := BuildMonth(context.Background(), "s1", "2026-10", first, []OriginalTurn{two}, e2, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if next.Raw.Revision != 2 || len(next.Raw.Content.Turns) != 2 || next.Raw.Content.Turns[0].User != one.User {
		t.Fatal("raw history lost")
	}
	// Same topic merged, mastery adjudicated to highest rank (mastered).
	areas := next.Summary.Content.KnowledgeAreas
	if len(areas) != 1 || len(areas[0].Topics) != 1 {
		t.Fatalf("expected one merged topic, got %+v", areas)
	}
	topic := areas[0].Topics[0]
	if topic.Mastery != MasteryMastered {
		t.Fatalf("mastery not adjudicated to highest: %s", topic.Mastery)
	}
	if len(topic.QuestionIDs) != 2 || topic.LastSeen != two.CreatedAt.Format(time.RFC3339) {
		t.Fatalf("incorrect aggregation: %+v", topic)
	}
	// seq must be contiguous and time-ordered.
	if next.Raw.Content.Turns[0].Seq != 1 || next.Raw.Content.Turns[1].Seq != 2 {
		t.Fatal("seq not contiguous")
	}
	bad := extraction(two)
	bad.Topics[0].SourceIDs = []string{"invented/source"}
	if _, err = BuildMonth(context.Background(), "s1", "2026-10", first, []OriginalTurn{two}, bad, time.Now()); err == nil {
		t.Fatal("fabricated evidence accepted")
	}
}

func TestRawAppendOnlyDoesNotReorderOlderTurns(t *testing.T) {
	one := rawFixture("t1")
	one.CreatedAt = time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	first, err := BuildMonth(context.Background(), "s1", "2026-10", Month{}, []OriginalTurn{one}, extraction(one), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	// A later batch contains a turn whose CreatedAt is EARLIER than the existing
	// turn (e.g. idle backfill / clock skew). L3 must stay append-only: the old
	// turn keeps position 0, the fresh turn is appended, not prepended.
	two := rawFixture("t2")
	two.CreatedAt = time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	next, err := BuildMonth(context.Background(), "s1", "2026-10", first, []OriginalTurn{two}, extraction(two), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	turns := next.Raw.Content.Turns
	if len(turns) != 2 {
		t.Fatalf("expected 2 turns, got %d", len(turns))
	}
	if turns[0].Source.TurnID != "t1" || turns[1].Source.TurnID != "t2" {
		t.Fatalf("append-only violated: order is %s,%s", turns[0].Source.TurnID, turns[1].Source.TurnID)
	}
	if turns[0].Seq != 1 || turns[1].Seq != 2 {
		t.Fatal("seq not contiguous after append")
	}
	// span still reflects the true min/max across all turns.
	if next.Summary.Content.Span.First != "2026-10-27" || next.Summary.Content.Span.Last != "2026-10-28" {
		t.Fatalf("span wrong: %+v", next.Summary.Content.Span)
	}
}

func TestTeacherDemonstrationDoesNotChangeMastery(t *testing.T) {
	raw := rawFixture("t1")
	e := extraction(raw)
	e.Topics[0].Authored = false
	m, err := BuildMonth(context.Background(), "s1", "2026-10", Month{}, []OriginalTurn{raw}, e, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Summary.Content.KnowledgeAreas) != 0 {
		t.Fatalf("teacher demonstration entered topics: %+v", m.Summary.Content.KnowledgeAreas)
	}
}

func TestErrorCountAccumulatesOnSameTopic(t *testing.T) {
	one := rawFixture("t1")
	e1 := extraction(one)
	e1.Topics[0].Incorrect = true
	first, err := BuildMonth(context.Background(), "s1", "2026-10", Month{}, []OriginalTurn{one}, e1, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if got := first.Summary.Content.KnowledgeAreas[0].Topics[0].ErrorCount; got != 1 {
		t.Fatalf("first incorrect turn should set error_count=1, got %d", got)
	}
	// Same (area, topic) reoccurs, incorrect again -> error_count increments.
	two := rawFixture("t2")
	two.CreatedAt = one.CreatedAt.Add(time.Hour)
	e2 := extraction(two)
	e2.Topics[0].Incorrect = true
	next, err := BuildMonth(context.Background(), "s1", "2026-10", first, []OriginalTurn{two}, e2, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	areas := next.Summary.Content.KnowledgeAreas
	if len(areas) != 1 || len(areas[0].Topics) != 1 {
		t.Fatalf("expected one merged topic, got %+v", areas)
	}
	if got := areas[0].Topics[0].ErrorCount; got != 2 {
		t.Fatalf("recurring incorrect topic should accumulate to error_count=2, got %d", got)
	}
	// A correct answer (mastered) on the same topic RESETS error_count to zero.
	three := rawFixture("t3")
	three.CreatedAt = two.CreatedAt.Add(time.Hour)
	e3 := extraction(three)
	e3.Topics[0].Incorrect = false
	e3.Topics[0].Suggested = MasteryMastered
	final, err := BuildMonth(context.Background(), "s1", "2026-10", next, []OriginalTurn{three}, e3, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if got := final.Summary.Content.KnowledgeAreas[0].Topics[0].ErrorCount; got != 0 {
		t.Fatalf("mastered turn must reset error_count to 0, got %d", got)
	}
}

func TestUnregisteredTaxonomyRejected(t *testing.T) {
	raw := rawFixture("t1")
	e := extraction(raw)
	e.Topics[0].Area = "NOT_REAL"
	if _, err := BuildMonth(context.Background(), "s1", "2026-10", Month{}, []OriginalTurn{raw}, e, time.Now()); err == nil {
		t.Fatal("unregistered area accepted")
	}
}

func TestMasteredContradictoryIncorrectIsNormalized(t *testing.T) {
	// Model erroneously reports mastered + incorrect. Server must force
	// incorrect=false so error_count is not polluted by contradictory input.
	raw := rawFixture("t1")
	e := extraction(raw)
	e.Topics[0].Suggested = MasteryMastered
	e.Topics[0].Incorrect = true
	m, err := BuildMonth(context.Background(), "s1", "2026-10", Month{}, []OriginalTurn{raw}, e, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Summary.Content.KnowledgeAreas[0].Topics[0].ErrorCount; got != 0 {
		t.Fatalf("mastered+incorrect must normalize to error_count=0, got %d", got)
	}
}

func TestUpdateStability(t *testing.T) {
	if got := UpdateStability(DefaultStability, MasteryMastered); got <= DefaultStability {
		t.Fatalf("mastered must raise stability, got %f", got)
	}
	if got := UpdateStability(DefaultStability, MasteryFamiliar); got >= DefaultStability {
		t.Fatalf("familiar must lower stability, got %f", got)
	}
	if got := UpdateStability(DefaultStability, MasteryUnseen); got != DefaultStability {
		t.Fatalf("unseen must leave stability unchanged, got %f", got)
	}
	if got := UpdateStability(0.99, MasteryMastered); got != 1.0 {
		t.Fatalf("stability must clamp to 1.0, got %f", got)
	}
	if got := UpdateStability(0.01, MasteryFamiliar); got != 0.0 {
		t.Fatalf("stability must clamp to 0.0, got %f", got)
	}
}

func TestUpdateDifficulty(t *testing.T) {
	if UpdateDifficulty(0) != DefaultDifficulty {
		t.Fatal("zero errors must map to default difficulty")
	}
	if got := UpdateDifficulty(2); got <= DefaultDifficulty {
		t.Fatalf("difficulty must rise with error count, got %f", got)
	}
	if UpdateDifficulty(-1) != DefaultDifficulty {
		t.Fatal("negative error count must clamp to default difficulty")
	}
	if UpdateDifficulty(1000) != 1.0 {
		t.Fatal("difficulty must clamp to 1.0")
	}
}

func TestRetrievabilityDecays(t *testing.T) {
	// Freshly seen (elapsed 0) is fully recallable regardless of params.
	if got := Retrievability(0.5, 0.2, 0); got != 1.0 {
		t.Fatalf("zero elapsed must be fully recallable, got %f", got)
	}
	// Higher stability -> slower decay.
	now := Retrievability(0.9, 0.2, 7*24*time.Hour)
	later := Retrievability(0.2, 0.2, 7*24*time.Hour)
	if now <= later {
		t.Fatalf("higher stability must decay slower: %f vs %f", now, later)
	}
	// Higher difficulty -> faster decay.
	easy := Retrievability(0.5, 0.1, 7*24*time.Hour)
	hard := Retrievability(0.5, 0.9, 7*24*time.Hour)
	if easy <= hard {
		t.Fatalf("higher difficulty must decay faster: %f vs %f", easy, hard)
	}
	// Monotonic: more elapsed -> lower retrievability.
	if Retrievability(0.5, 0.2, 30*24*time.Hour) >= Retrievability(0.5, 0.2, 1*24*time.Hour) {
		t.Fatal("retrievability must decrease over time")
	}
}

func TestBuildMonthComputesForgettingFields(t *testing.T) {
	// New topic authored with partial -> stability above default (strengthened),
	// difficulty from error_count.
	one := rawFixture("t1")
	e1 := extraction(one)
	e1.Topics[0].Incorrect = true // partial + incorrect -> error_count=1
	first, err := BuildMonth(context.Background(), "s1", "2026-10", Month{}, []OriginalTurn{one}, e1, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	tp := first.Summary.Content.KnowledgeAreas[0].Topics[0]
	if tp.Stability <= DefaultStability {
		t.Fatalf("partial should strengthen stability above default, got %f", tp.Stability)
	}
	if tp.Difficulty != UpdateDifficulty(1) {
		t.Fatalf("difficulty should reflect error_count=1, got %f", tp.Difficulty)
	}
	// Recurring mastered turn -> stability rises further, difficulty resets to default.
	two := rawFixture("t2")
	two.CreatedAt = one.CreatedAt.Add(time.Hour)
	e2 := extraction(two)
	e2.Topics[0].Suggested = MasteryMastered
	e2.Topics[0].Incorrect = false
	next, err := BuildMonth(context.Background(), "s1", "2026-10", first, []OriginalTurn{two}, e2, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	tp2 := next.Summary.Content.KnowledgeAreas[0].Topics[0]
	if tp2.Stability <= tp.Stability {
		t.Fatalf("mastered must raise stability above partial, %f vs %f", tp2.Stability, tp.Stability)
	}
	if tp2.Difficulty != DefaultDifficulty {
		t.Fatalf("mastered resets error_count, difficulty must return to default, got %f", tp2.Difficulty)
	}
}

func TestLegacyTopicBackfilledWithDefaults(t *testing.T) {
	// A legacy v2 topic with zero-valued stability/difficulty, merged with no new
	// authored turns, must be backfilled to neutral priors (not zero).
	legacy := Month{
		Summary: Document[SummaryContent]{Content: SummaryContent{
			KnowledgeAreas: []KnowledgeArea{{Area: "ALG", Topics: []Topic{{
				Topic: "一元二次方程求根", Mastery: MasteryPartial, Ability: "AB-COMPUTE",
				ErrorCount: 1, LastSeen: "2026-10-28T01:00:00Z",
			}}}},
		}},
	}
	// A fresh authored turn on an unrelated topic triggers a BuildMonth; the legacy
	// topic should survive with backfilled priors.
	raw := rawFixture("t-new")
	e := extraction(raw)
	e.Topics[0].Area = "SEQ"
	e.Topics[0].Topic = "等差数列求和"
	m, err := BuildMonth(context.Background(), "s1", "2026-10", legacy, []OriginalTurn{raw}, e, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range m.Summary.Content.KnowledgeAreas {
		for _, tp := range a.Topics {
			if tp.Topic == "一元二次方程求根" {
				if tp.Stability != DefaultStability || tp.Difficulty != DefaultDifficulty {
					t.Fatalf("legacy topic not backfilled: %+v", tp)
				}
			}
		}
	}
}

type fakeDocuments struct {
	month     Month
	published int
}

func (f *fakeDocuments) Load(context.Context, string, string) (Month, error) {
	if f.month.Raw.Revision == 0 {
		return Month{}, ErrNotFound
	}
	return f.month, nil
}
func (f *fakeDocuments) Publish(_ context.Context, _, _ string, old, next Month) error {
	if f.month.Raw.Revision != old.Raw.Revision {
		return ErrConflict
	}
	f.month = next
	f.published++
	return nil
}
func (f *fakeDocuments) Indexes(context.Context, string, int) ([]Document[IndexContent], error) {
	return []Document[IndexContent]{f.month.Index}, nil
}

type fakeExtractor struct{ calls int }

func (f *fakeExtractor) Extract(_ context.Context, _ SummaryContent, _ QuestionContext, turns []OriginalTurn) (Extracted, error) {
	f.calls++
	return extraction(turns[0]), nil
}

type fakeRepository struct {
	Repository
	claim       Claim
	completions int
	failOnce    bool
}

func (f *fakeRepository) Complete(context.Context, Claim) error {
	f.completions++
	if f.failOnce {
		f.failOnce = false
		return errors.New("SQL unavailable after Redis publish")
	}
	return nil
}
func TestRetryAfterRedisPublishDoesNotRegenerateOrDuplicate(t *testing.T) {
	raw := rawFixture("t1")
	claim := Claim{StudentID: "s1", Turns: []PendingTurn{{ID: 1, Month: "2026-10", Raw: raw}}}
	docs := &fakeDocuments{}
	model := &fakeExtractor{}
	repo := &fakeRepository{failOnce: true}
	s := Service{Repo: repo, Documents: docs, Extractor: model}
	if err := s.process(context.Background(), claim, time.Now()); err == nil {
		t.Fatal("expected SQL acknowledgement failure")
	}
	if err := s.process(context.Background(), claim, time.Now()); err != nil {
		t.Fatal(err)
	}
	if model.calls != 1 || docs.published != 1 || len(docs.month.Raw.Content.Turns) != 1 || repo.completions != 2 {
		t.Fatal("retry regenerated or duplicated history")
	}
}
func TestProgressiveReadRejectsOtherStudentAndPaginates(t *testing.T) {
	one, two := rawFixture("t1"), rawFixture("t2")
	m, _ := BuildMonth(context.Background(), "s1", "2026-10", Month{}, []OriginalTurn{one, two}, extraction(one), time.Now())
	s := Service{Documents: &fakeDocuments{month: m}}
	ctx := context.Background()
	for _, p := range []string{"/user/memory/s2/2026-10/content.md", "/user/memory/s1/2026-10/../2026-10/content.md", "/user/memory/s1/2026-10/content.md/extra", "/user/memory/indexes/s2/2026-10.md"} {
		if _, err := s.Read(ctx, "s1", p, 0, 1); err == nil {
			t.Fatalf("unauthorized path accepted: %s", p)
		}
	}
	b, err := s.Read(ctx, "s1", "/user/memory/s1/2026-10/content.md", 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) == 0 {
		t.Fatal("no page")
	}
	if _, err = s.Read(ctx, "s1", "/user/memory/s1/2026-10/content.md", 3, 1); err == nil {
		t.Fatal("invalid offset accepted")
	}
}

type preloadRepository struct {
	Repository
	profile Profile
}

func (r *preloadRepository) Profile(context.Context, string) (Profile, error) { return r.profile, nil }
func TestPreloadBudgetAndSuccessfulTurnWake(t *testing.T) {
	raw := rawFixture("t1")
	month, _ := BuildMonth(context.Background(), "s1", "2026-10", Month{}, []OriginalTurn{raw}, extraction(raw), time.Now())
	p := Profile{StudentID: "s1", Basic: BasicProfile{Name: "测试学生", Grade: "高一"}}
	for i := 0; i < 30; i++ {
		p.Basic.LearningGoals = append(p.Basic.LearningGoals, strings.Repeat("学", 100))
	}
	s := &Service{Repo: &preloadRepository{profile: p}, Documents: &fakeDocuments{month: month}, Wake: make(chan struct{}, 1)}
	turn := &agentic.Turn{Request: agentic.Request{Input: state.Input{Identity: state.Identity{Scope: state.Scope{Namespace: "teacher", SubjectID: "s1", ConversationID: "c1"}}}}}
	c, err := (Module{Service: s}).Contribute(context.Background(), turn)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.LongTermMemory) > 6000 {
		t.Fatal("unbounded preloaded context")
	}
	var data map[string]any
	if err = json.Unmarshal(c.LongTermMemory, &data); err != nil || data["context_truncated"] != true {
		t.Fatal("missing bounded context marker")
	}
	turn.Result.Status = "failed"
	_ = c.PostRuntimeHooks[0](context.Background(), turn)
	if len(s.Wake) != 0 {
		t.Fatal("failed turn woke extractor")
	}
	turn.Result.Status = "succeeded"
	_ = c.PostRuntimeHooks[0](context.Background(), turn)
	if len(s.Wake) != 1 {
		t.Fatal("successful turn did not wake extractor")
	}
}

func TestTopicLimitCountsChineseCharacters(t *testing.T) {
	c := SummaryContent{Summary: "测试摘要", KnowledgeAreas: []KnowledgeArea{{Area: "FUNC", Topics: []Topic{{Topic: strings.Repeat("函", 60), Mastery: MasteryPartial, Ability: "AB-REASON"}}}}}
	if err := ValidateSummaryContent(c); err != nil {
		t.Fatal(err)
	}
	c.KnowledgeAreas[0].Topics[0].Topic += "数"
	if err := ValidateSummaryContent(c); err == nil {
		t.Fatal("accepted more than 60 characters")
	}
}
