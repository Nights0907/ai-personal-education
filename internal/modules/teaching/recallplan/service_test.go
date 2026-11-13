package recallplan

import (
	"context"
	"testing"
	"time"

	"github.com/musi-ai/education/internal/modules/student/memory"
	"github.com/musi-ai/education/internal/modules/teaching/solve"
)

type fakeRepo struct {
	plan         Plan
	hasPlan      bool
	upsertCalls  int
	items        map[int64]Item
	nextItemID   int64
	markedRan    map[int64]string
	addedCount   int
	pendingCalls int
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{
		plan:       Plan{ID: 1, StudentID: "s1", Status: StatusActive, DailyQuota: 1, SendHour: 19},
		hasPlan:    true,
		items:      map[int64]Item{},
		nextItemID: 1,
		markedRan:  map[int64]string{},
	}
}

func (f *fakeRepo) Migrate(context.Context) error { return nil }
func (f *fakeRepo) Plan(_ context.Context, _ string) (Plan, error) {
	if !f.hasPlan {
		return Plan{}, ErrNotFound
	}
	return f.plan, nil
}
func (f *fakeRepo) UpsertPlan(_ context.Context, _ string, q, h int) (Plan, error) {
	f.upsertCalls++
	f.hasPlan = true
	f.plan.DailyQuota = q
	f.plan.SendHour = h
	if f.plan.ID == 0 {
		f.plan.ID = 1
	}
	return f.plan, nil
}
func (f *fakeRepo) AddItems(_ context.Context, planID int64, items []Item) (int, error) {
	n := 0
	for _, it := range items {
		it.ID = f.nextItemID
		it.PlanID = planID
		it.Status = ItemPending
		f.items[f.nextItemID] = it
		f.nextItemID++
		n++
	}
	f.addedCount += n
	return n, nil
}
func (f *fakeRepo) PendingItems(_ context.Context, planID int64, limit int) ([]Item, error) {
	f.pendingCalls++
	out := []Item{}
	for _, it := range f.items {
		if it.PlanID == planID && it.Status == ItemPending {
			out = append(out, it)
		}
	}
	// Forgetting priority: lowest retrievability first.
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].Retrievability < out[i].Retrievability {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	if limit < len(out) {
		out = out[:limit]
	}
	return out, nil
}
func (f *fakeRepo) MarkSent(_ context.Context, itemID int64, convID string, at time.Time) error {
	it := f.items[itemID]
	it.Status = ItemSent
	it.ConversationID = convID
	t := at
	it.SentAt = &t
	f.items[itemID] = it
	return nil
}
func (f *fakeRepo) MarkReviewed(_ context.Context, itemID int64) error {
	it := f.items[itemID]
	it.Status = ItemReviewed
	f.items[itemID] = it
	return nil
}
func (f *fakeRepo) SentItemsIn(_ context.Context, planID int64, conversations []string) ([]Item, error) {
	out := []Item{}
	convSet := map[string]bool{}
	for _, c := range conversations {
		convSet[c] = true
	}
	for _, it := range f.items {
		if it.PlanID == planID && it.Status == ItemSent && convSet[it.ConversationID] {
			out = append(out, it)
		}
	}
	return out, nil
}
func (f *fakeRepo) DuePlans(_ context.Context, now time.Time) ([]Plan, error) {
	// Mirror the real SQL filter: skip plans already run today.
	if f.plan.LastRunDate == now.UTC().Format("2006-01-02") {
		return []Plan{}, nil
	}
	return []Plan{f.plan}, nil
}
func (f *fakeRepo) MarkRan(_ context.Context, planID int64, date string) error {
	f.markedRan[planID] = date
	return nil
}

type fakeRecall struct{ items []memory.RecallItem }

func (f fakeRecall) DueItems(context.Context, string, time.Time) ([]memory.RecallItem, error) {
	return f.items, nil
}

type fakeQuestions struct{}

func (fakeQuestions) Question(context.Context, string, string) (solve.View, error) {
	return solve.View{Question: solve.Question{Stem: "解方程 x^2-5x+6=0"}}, nil
}

type fakeSink struct {
	opened []string
}

func (f *fakeSink) Open(_ context.Context, _, _, _ string) (string, error) {
	f.opened = append(f.opened, "conv")
	return "conv", nil
}

func TestSchedulerQueuesOneQuestionPerTopicWithoutOpeningConversation(t *testing.T) {
	repo := newFakeRepo()
	sink := &fakeSink{}
	s := &Service{
		Repo: repo,
		Recall: fakeRecall{items: []memory.RecallItem{
			{Area: "ALG", Topic: "一元二次方程求根", Retrievability: 0.3, Due: true, QuestionIDs: []string{"q1", "q2"}},
		}},
		Questions:     fakeQuestions{},
		Conversations: sink,
		Model:         nil, // fallback opening, no model call in test
	}
	now := time.Now()
	worked, err := s.Step(context.Background(), now)
	if err != nil || !worked {
		t.Fatalf("step failed: %v %v", worked, err)
	}
	// daily_quota = 1, so exactly one conversation opened.
	if len(sink.opened) != 0 || len(repo.items) != 1 {
		t.Fatalf("expected no automatic delivery, got %d", len(sink.opened))
	}
	if repo.markedRan[repo.plan.ID] == "" {
		t.Fatal("plan not marked as ran")
	}
	// Second step same day: no new delivery (last_run_date matches).
	sink2 := &fakeSink{}
	s.Conversations = sink2
	repo.plan.LastRunDate = now.UTC().Format("2006-01-02")
	worked, err = s.Step(context.Background(), now)
	if err != nil || worked {
		t.Fatalf("same-day re-run should not deliver: %v %v", worked, err)
	}
}

func TestEnsurePlanCreatesPlanWhenDue(t *testing.T) {
	repo := newFakeRepo()
	repo.plan = Plan{} // no existing plan
	repo.hasPlan = false
	s := &Service{
		Repo:   repo,
		Recall: fakeRecall{items: []memory.RecallItem{{Topic: "x", Due: true, QuestionIDs: []string{"q1"}}}},
	}
	if err := s.EnsurePlan(context.Background(), "s1", time.Now()); err != nil {
		t.Fatal(err)
	}
	if !repo.hasPlan {
		t.Fatal("expected plan to be created when due items exist")
	}
}

func TestEnsurePlanMonitorsBeforeFirstDue(t *testing.T) {
	repo := newFakeRepo()
	repo.plan = Plan{}
	repo.hasPlan = false
	s := &Service{
		Repo:   repo,
		Recall: fakeRecall{items: []memory.RecallItem{}}, // nothing due
	}
	if err := s.EnsurePlan(context.Background(), "s1", time.Now()); err != nil {
		t.Fatal(err)
	}
	if !repo.hasPlan {
		t.Fatal("monitoring must exist before the first natural due date")
	}
}

func TestEnsurePlanNoopWhenAlreadyEnrolled(t *testing.T) {
	repo := newFakeRepo()
	repo.hasPlan = true
	s := &Service{
		Repo:   repo,
		Recall: fakeRecall{items: []memory.RecallItem{{Topic: "x", Due: true, QuestionIDs: []string{"q1"}}}},
	}
	if err := s.EnsurePlan(context.Background(), "s1", time.Now()); err != nil {
		t.Fatal(err)
	}
	// Already enrolled: plan unchanged, no duplicate.
	if repo.upsertCalls != 0 {
		t.Fatalf("expected no upsert when already enrolled, got %d", repo.upsertCalls)
	}
}

func TestMarkReviewedForConversations(t *testing.T) {
	repo := newFakeRepo()
	// Seed a SENT item bound to a review conversation.
	sent := Item{ID: 100, PlanID: 1, QuestionID: "q1", Status: ItemSent, ConversationID: "conv-review"}
	repo.items[100] = sent
	s := &Service{Repo: repo}
	if err := s.MarkReviewedForConversations(context.Background(), "s1", []string{"conv-review"}); err != nil {
		t.Fatal(err)
	}
	if repo.items[100].Status != ItemReviewed {
		t.Fatalf("expected REVIEWED, got %s", repo.items[100].Status)
	}
}

func TestMarkReviewedSkipsOtherConversations(t *testing.T) {
	repo := newFakeRepo()
	sent := Item{ID: 100, PlanID: 1, QuestionID: "q1", Status: ItemSent, ConversationID: "conv-review"}
	repo.items[100] = sent
	s := &Service{Repo: repo}
	// A different conversation was answered, not the review one.
	if err := s.MarkReviewedForConversations(context.Background(), "s1", []string{"other-conv"}); err != nil {
		t.Fatal(err)
	}
	if repo.items[100].Status != ItemSent {
		t.Fatalf("expected still SENT, got %s", repo.items[100].Status)
	}
}

func TestParseSendHour(t *testing.T) {
	cases := map[string]int{
		"":      DefaultSendHour,
		"8":     8,
		"19":    19,
		"23":    23,
		"0":     0,
		"24":    DefaultSendHour,
		"-1":    DefaultSendHour,
		"abc":   DefaultSendHour,
		"19:30": DefaultSendHour,
	}
	for in, want := range cases {
		if got := ParseSendHour(in); got != want {
			t.Fatalf("ParseSendHour(%q) = %d, want %d", in, got, want)
		}
	}
}

func (f *fakeRepo) NotificationItem(_ context.Context, plan, id int64) (Item, error) {
	item, ok := f.items[id]
	if !ok || item.PlanID != plan {
		return Item{}, ErrNotFound
	}
	return item, nil
}
func (f *fakeSink) OpenReview(ctx context.Context, student, request, message, question string) (string, error) {
	return f.Open(ctx, student, request, message)
}
func TestClaimNotificationIsScopedAndIdempotent(t *testing.T) {
	repo := newFakeRepo()
	repo.items[1] = Item{ID: 1, PlanID: repo.plan.ID, QuestionID: "q1", Status: ItemPending}
	repo.items[2] = Item{ID: 2, PlanID: 99, QuestionID: "other", Status: ItemPending}
	sink := &fakeSink{}
	s := Service{Repo: repo, Questions: fakeQuestions{}, Conversations: sink}
	if _, err := s.ClaimNotification(context.Background(), "s1", 2); err == nil {
		t.Fatal("another plan notification accepted")
	}
	first, err := s.ClaimNotification(context.Background(), "s1", 1)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.ClaimNotification(context.Background(), "s1", 1)
	if err != nil || first != second || len(sink.opened) != 1 {
		t.Fatalf("claim not idempotent: %s %s %v %d", first, second, err, len(sink.opened))
	}
	if repo.items[1].Status != ItemSent {
		t.Fatal("claim not recorded")
	}
}
func TestStatusExposesPendingNotificationsWithoutDelivery(t *testing.T) {
	repo := newFakeRepo()
	repo.items[7] = Item{ID: 7, PlanID: repo.plan.ID, QuestionID: "q7", Topic: "二次函数", Status: ItemPending}
	sink := &fakeSink{}
	s := Service{Repo: repo, Conversations: sink}
	status := s.Status(context.Background(), "s1")
	if len(status.Notifications) != 1 || status.Notifications[0].ID != 7 || len(sink.opened) != 0 {
		t.Fatal("pending notification missing or delivered")
	}
}
