package memory

import (
	"context"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestInterleavedTopicsKeepLatestEvidenceAndCurrentMastery(t *testing.T) {
	a, b, c := rawFixture("a"), rawFixture("b"), rawFixture("c")
	b.CreatedAt = a.CreatedAt.Add(time.Minute)
	c.CreatedAt = b.CreatedAt.Add(time.Minute)
	e := extraction(a)
	e.Topics[0].Topic = "A"
	e.Topics[0].Suggested = MasteryFamiliar
	e.Topics[0].Incorrect = true
	second := extraction(b).Topics[0]
	second.Topic = "B"
	third := extraction(c).Topics[0]
	third.Topic = "A"
	third.Suggested = MasteryMastered
	e.Topics = append(e.Topics, second, third)
	m, err := BuildMonth(context.Background(), "s1", "2026-10", Month{}, []OriginalTurn{a, b, c}, e, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	got := m.Summary.Content.KnowledgeAreas[0].Topics[0]
	if got.Topic != "A" || got.Mastery != MasteryMastered || got.ErrorCount != 0 || got.TotalErrors != 1 || got.LastSeen != c.CreatedAt.Format(time.RFC3339) {
		t.Fatal("lost update", got)
	}
	d := rawFixture("d")
	d.CreatedAt = c.CreatedAt.Add(time.Minute)
	bad := extraction(d)
	bad.Topics[0].Topic = "A"
	bad.Topics[0].Suggested = MasteryFamiliar
	bad.Topics[0].Incorrect = true
	n, err := BuildMonth(context.Background(), "s1", "2026-10", m, []OriginalTurn{d}, bad, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	got = n.Summary.Content.KnowledgeAreas[0].Topics[0]
	if got.Mastery != MasteryFamiliar || got.BestMastery != MasteryMastered || got.TotalErrors != 2 {
		t.Fatal("portrait did not reflect regression", got)
	}
}
func TestChineseSummaryAndAssistedEvidence(t *testing.T) {
	raw := rawFixture("a")
	raw.Assisted = true
	e := extraction(raw)
	e.Summary = strings.Repeat("学", 2000)
	e.Topics[0].Suggested = MasteryMastered
	m, err := BuildMonth(context.Background(), "s1", "2026-10", Month{}, []OriginalTurn{raw}, e, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	got := m.Summary.Content.KnowledgeAreas[0].Topics[0]
	if got.Mastery != MasteryPartial || got.ErrorCount != 0 {
		t.Fatal("hinted completion marked independent or incorrect", got)
	}
	if m.Raw.Content.Turns[0].Assessment == nil || !m.Raw.Content.Turns[0].Assessment.Authored {
		t.Fatal("adjudicated evidence not recoverable")
	}
	e.Summary += "学"
	if _, err = BuildMonth(context.Background(), "s1", "2026-10", Month{}, []OriginalTurn{raw}, e, time.Now()); err == nil {
		t.Fatal("summary over limit")
	}
	e.Summary = "学生作答"
	e.Topics[0].QuestionID = "fabricated"
	if _, err = BuildMonth(context.Background(), "s1", "2026-10", Month{}, []OriginalTurn{raw}, e, time.Now()); err == nil {
		t.Fatal("unbound question id accepted")
	}
}
func TestRetryRecoversPublishedEvidenceNotification(t *testing.T) {
	raw := rawFixture("t1")
	claim := Claim{StudentID: "s1", Turns: []PendingTurn{{ID: 1, Month: "2026-10", Raw: raw}}}
	docs := &fakeDocuments{}
	model := &fakeExtractor{}
	repo := &fakeRepository{failOnce: true}
	var notified []string
	s := Service{Repo: repo, Documents: docs, Extractor: model, AfterExtract: func(_ context.Context, _ string, ids []string, _ time.Time) { notified = append(notified, ids...) }}
	if e := s.process(context.Background(), claim, time.Now()); e == nil {
		t.Fatal("fault was not injected")
	}
	if e := s.process(context.Background(), claim, time.Now()); e != nil {
		t.Fatal(e)
	}
	if model.calls != 1 || len(notified) != 1 || notified[0] != "c1" {
		t.Fatal("retry lost evidence", notified, model.calls)
	}
}
func TestStableTopicIDsMergeArithmeticSynonyms(t *testing.T) {
	if CanonicalTopicID("SEQ", "等差数列通项公式", "") != CanonicalTopicID("SEQ", "等差数列求和", "") {
		t.Fatal("synonyms forked")
	}
}

type monthDocuments struct{ months map[string]Month }

func (d *monthDocuments) Load(_ context.Context, _ string, month string) (Month, error) {
	m, ok := d.months[month]
	if !ok {
		return Month{}, ErrNotFound
	}
	return m, nil
}
func (d *monthDocuments) Publish(_ context.Context, _, month string, _, next Month) error {
	d.months[month] = next
	return nil
}
func (d *monthDocuments) Indexes(context.Context, string, int) ([]Document[IndexContent], error) {
	out := []Document[IndexContent]{}
	for _, m := range d.months {
		out = append(out, m.Index)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Month > out[j].Month })
	return out, nil
}
func TestMonthBoundaryInheritsLearningStateWithoutCopyingRaw(t *testing.T) {
	raw := rawFixture("oct")
	e := extraction(raw)
	e.Topics[0].Suggested = MasteryMastered
	old, err := BuildMonth(context.Background(), "s1", "2026-10", Month{}, []OriginalTurn{raw}, e, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	nextRaw := rawFixture("nov")
	nextRaw.CreatedAt = time.Date(2026, 11, 1, 1, 0, 0, 0, time.UTC)
	docs := &monthDocuments{months: map[string]Month{"2026-10": old}}
	s := Service{Repo: &fakeRepository{}, Documents: docs, Extractor: &fakeExtractor{}}
	if err = s.process(context.Background(), Claim{StudentID: "s1", Turns: []PendingTurn{{Month: "2026-11", Raw: nextRaw}}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	next := docs.months["2026-11"]
	got := next.Summary.Content.KnowledgeAreas[0].Topics[0]
	if got.Stability < 0.69 || got.BestMastery != MasteryMastered || got.Mastery != MasteryPartial || len(next.Raw.Content.Turns) != 1 || next.Raw.Content.Turns[0].Source.TurnID != "nov" {
		t.Fatal("month boundary reset state or copied history", got, next.Raw)
	}
}
func TestUnavailableImageAndDuplicateSourceCannotInflateMastery(t *testing.T) {
	raw := rawFixture("photo")
	raw.AttemptUnavailable = true
	e := extraction(raw)
	m, err := BuildMonth(context.Background(), "s1", "2026-10", Month{}, []OriginalTurn{raw}, e, time.Now())
	if err != nil || len(m.Summary.Content.KnowledgeAreas) > 0 {
		t.Fatal("missing image evidence counted", m, err)
	}
	raw.AttemptUnavailable = false
	e.Topics = append(e.Topics, e.Topics[0])
	m, err = BuildMonth(context.Background(), "s1", "2026-10", Month{}, []OriginalTurn{raw}, e, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Summary.Content.KnowledgeAreas[0].Topics[0].Stability; got > 0.56 {
		t.Fatal("duplicate source inflated stability", got)
	}
}

func TestQuestionAndUnverifiedAttemptCannotBecomeMastered(t *testing.T) {
	for _, purpose := range []string{"question", "attempt"} {
		raw := rawFixture("eligibility")
		raw.Purpose = purpose
		e := extraction(raw)
		e.Topics[0].Suggested = MasteryMastered
		m, err := BuildMonth(context.Background(), "s1", "2026-10", Month{}, []OriginalTurn{raw}, e, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range m.Summary.Content.KnowledgeAreas {
			for _, topic := range a.Topics {
				if topic.Mastery == MasteryMastered {
					t.Fatal("unverified mastery", purpose)
				}
			}
		}
	}
}

func TestEvidenceDimensionsAreDerivedWithoutNewExtractionFields(t *testing.T) {
	raw := rawFixture("independent")
	raw.Purpose = "attempt"
	raw.Evaluation = &AttemptEvaluation{Verified: true}
	extracted := extraction(raw)
	extracted.Topics[0].Suggested = MasteryMastered
	m, err := BuildMonth(context.Background(), "s1", "2026-10", Month{}, []OriginalTurn{raw}, extracted, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	topic := m.Summary.Content.KnowledgeAreas[0].Topics[0]
	if topic.EvidenceCount != 1 || topic.IndependentCorrect != 1 || topic.AssistedAttempts != 0 || topic.NextAction == "" {
		t.Fatal(topic)
	}
	raw.Assisted = true
	m, err = BuildMonth(context.Background(), "s1", "2026-10", Month{}, []OriginalTurn{raw}, extracted, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	topic = m.Summary.Content.KnowledgeAreas[0].Topics[0]
	if topic.IndependentCorrect != 0 || topic.AssistedAttempts != 1 || topic.Mastery == MasteryMastered {
		t.Fatal(topic)
	}
}
func TestVerifiedFeynmanReviewStrengthensTopicAndDelaysForgetting(t *testing.T) {
	first := rawFixture("before-review")
	old, err := BuildMonth(context.Background(), "s1", "2026-10", Month{}, []OriginalTurn{first}, extraction(first), first.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	before := old.Summary.Content.KnowledgeAreas[0].Topics[0]
	review := rawFixture("feynman-correct")
	review.CreatedAt = first.CreatedAt.Add(3 * 24 * time.Hour)
	review.Purpose = "attempt"
	review.Evaluation = &AttemptEvaluation{Verified: true}
	review.Assisted = false
	evidence := extraction(review)
	evidence.Topics[0].Suggested = MasteryMastered
	next, err := BuildMonth(context.Background(), "s1", "2026-10", old, []OriginalTurn{review}, evidence, review.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	after := next.Summary.Content.KnowledgeAreas[0].Topics[0]
	if after.Mastery != MasteryMastered || after.Stability <= before.Stability || after.IndependentCorrect < 1 {
		t.Fatalf("review not reflected: before=%+v after=%+v", before, after)
	}
	if Retrievability(after.Stability, after.Difficulty, 7*24*time.Hour) <= Retrievability(before.Stability, before.Difficulty, 7*24*time.Hour) {
		t.Fatal("review did not slow forgetting")
	}
	duplicate, err := BuildMonth(context.Background(), "s1", "2026-10", next, []OriginalTurn{review}, evidence, review.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	if duplicate.Summary.Content.KnowledgeAreas[0].Topics[0].Stability != after.Stability {
		t.Fatal("duplicate review inflated stability")
	}
}
func TestExistingTopicIdentityReusedBeforeCreatingNewTopic(t *testing.T) {
	first := rawFixture("same-concept-1")
	ex := extraction(first)
	ex.Topics[0].Topic = "配方法的运算步骤"
	ex.Topics[0].TopicID = ""
	old, err := BuildMonth(context.Background(), "s1", "2026-10", Month{}, []OriginalTurn{first}, ex, first.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	id := old.Summary.Content.KnowledgeAreas[0].Topics[0].TopicID
	second := rawFixture("same-concept-2")
	second.CreatedAt = first.CreatedAt.Add(time.Minute)
	nextEx := extraction(second)
	nextEx.Topics[0].TopicID = id
	nextEx.Topics[0].Topic = "通过配方整理表达式"
	next, err := BuildMonth(context.Background(), "s1", "2026-10", old, []OriginalTurn{second}, nextEx, second.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	topics := next.Summary.Content.KnowledgeAreas[0].Topics
	if len(topics) != 1 || topics[0].TopicID != id || topics[0].EvidenceCount != 2 {
		t.Fatalf("same concept duplicated or count lost: %+v", topics)
	}
	third := rawFixture("different-concept")
	third.CreatedAt = second.CreatedAt.Add(time.Minute)
	different := extraction(third)
	different.Topics[0].TopicID = ""
	different.Topics[0].Topic = "余式定理"
	result, err := BuildMonth(context.Background(), "s1", "2026-10", next, []OriginalTurn{third}, different, third.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Summary.Content.KnowledgeAreas[0].Topics) != 2 {
		t.Fatal("distinct mathematical concepts merged")
	}
}
