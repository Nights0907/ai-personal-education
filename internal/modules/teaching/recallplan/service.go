package recallplan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"log/slog"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/musi-ai/education/internal/agent/modelstream"
	"github.com/musi-ai/education/internal/modules/student/memory"
	"github.com/musi-ai/education/internal/modules/teaching/solve"
)

// RecallSource yields the student's current forgetting state. It is satisfied
// by studentmemory.Service.RecallPlan via a thin adapter.
type RecallSource interface {
	DueItems(ctx context.Context, student string, now time.Time) ([]memory.RecallItem, error)
}

// ConversationSink creates a fresh conversation and presets an opening message.
type ConversationSink interface {
	Open(ctx context.Context, student, requestID, message string) (conversationID string, err error)
}

// QuestionSource returns a question's stem by ID, for building the delivery.
type QuestionSource interface {
	Question(ctx context.Context, student, questionID string) (solve.View, error)
}

// Service is the forgetting-prevention scheduler. It runs once a day per active
// plan, detects due knowledge points, and delivers a fixed number (default 1)
// of questions by opening a fresh conversation with a model-generated opening.
type Service struct {
	Repo          Repository
	Recall        RecallSource
	Questions     QuestionSource
	Conversations ConversationSink
	Model         model.BaseChatModel
	SendHour      int
	Wake          chan struct{}
}

func (s *Service) Notify() {
	select {
	case s.Wake <- struct{}{}:
	default:
	}
}

// Run polls until ctx is cancelled; each Step is cheap and idempotent.
func (s *Service) Run(ctx context.Context, onError func(error)) {
	for ctx.Err() == nil {
		worked, err := s.Step(ctx, time.Now())
		if err != nil && onError != nil && ctx.Err() == nil {
			onError(err)
		}
		delay := 30 * time.Second
		if worked {
			delay = time.Second
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-s.Wake:
			timer.Stop()
		case <-timer.C:
		}
	}
}

// OnExtract is the single hook wired to memory.Service.AfterExtract. After a
// student's memory is committed it (1) auto-enrolls them into a plan if any
// question is now due, and (2) marks SENT items as REVIEWED when the student
// actually answered in the review conversation. Both are best-effort.
func (s *Service) OnExtract(ctx context.Context, student string, conversations []string, now time.Time) {
	if s.Repo == nil {
		return
	}
	if err := s.EnsurePlan(ctx, student, now); err != nil {
		slog.WarnContext(ctx, "recallplan auto-enroll failed", "student", student, "err", err)
	}
	if err := s.MarkReviewedForConversations(ctx, student, conversations); err != nil {
		slog.WarnContext(ctx, "recallplan mark-reviewed failed", "student", student, "err", err)
	}
}

// MarkReviewedForConversations marks SENT items whose review conversation the
// student actually answered in as REVIEWED, removing them from the delivery
// queue so they are not sent again.
func (s *Service) MarkReviewedForConversations(ctx context.Context, student string, conversations []string) error {
	if s.Repo == nil || len(conversations) == 0 {
		return nil
	}
	plan, err := s.Repo.Plan(ctx, student)
	if err != nil {
		if err == ErrNotFound {
			return nil
		}
		return err
	}
	items, err := s.Repo.SentItemsIn(ctx, plan.ID, conversations)
	if err != nil {
		return err
	}
	for _, it := range items {
		if err := s.Repo.MarkReviewed(ctx, it.ID); err != nil && err != ErrConflict {
			return err
		}
	}
	return nil
}

// EnsurePlan checks whether the student now has any due knowledge points and,
// if so, creates (or keeps) a forgetting-prevention plan. It is the automatic
// enrollment hook: called after each memory extraction, so a student gains a
// plan the moment their first question decays past the recall threshold.
// It is a no-op when the student already has a plan or has nothing due.
func (s *Service) EnsurePlan(ctx context.Context, student string, now time.Time) error {
	if s.Repo == nil || s.Recall == nil {
		return nil
	}
	if _, err := s.Repo.Plan(ctx, student); err == nil {
		return nil // already enrolled
	} else if err != ErrNotFound {
		return err
	}
	// Only enroll students who have a L0 profile. The memory service owns that
	// check; here we simply detect due items. If none, no plan.
	_, err := s.Repo.UpsertPlan(ctx, student, 1, s.sendHour())
	if errors.Is(err, ErrNotFound) {
		return nil
	} // an existing paused plan stays paused
	return err
}

func (s *Service) sendHour() int {
	if s.SendHour >= 0 && s.SendHour <= 23 {
		return s.SendHour
	}
	return 19
}

// Status returns a read-only snapshot of the student's forgetting-prevention
// plan. When the student has no plan, or the repository does not support
// reporting, it returns a zero Status with HasPlan=false.
func (s *Service) Status(ctx context.Context, student string) Status {
	out := Status{}
	if s.Repo == nil {
		return out
	}
	plan, err := s.Repo.Plan(ctx, student)
	if err != nil {
		return out
	}
	out.HasPlan = true
	out.DailyQuota = plan.DailyQuota
	out.SendHour = plan.SendHour
	out.Timezone = plan.Timezone
	if out.Timezone == "" {
		out.Timezone = "Asia/Shanghai"
	}
	out.LastRunDate = plan.LastRunDate
	if store, ok := s.Repo.(StatusStore); ok {
		out.Pending, out.Sent, out.Reviewed, _ = store.PlanItemCounts(ctx, plan.ID)
	}
	if pending, e := s.Repo.PendingItems(ctx, plan.ID, 100); e == nil {
		for _, item := range pending {
			notification := ReviewNotification{ID: item.ID, QuestionID: item.QuestionID, Topic: item.Topic, Retrievability: item.Retrievability}
			if s.Questions != nil {
				if question, err := s.Questions.Question(ctx, student, item.QuestionID); err == nil {
					preview := []rune(strings.Join(strings.Fields(question.Question.Stem), " "))
					if len(preview) > 140 {
						preview = append(preview[:140], '…')
					}
					notification.QuestionPreview = string(preview)
				}
			}
			out.Notifications = append(out.Notifications, notification)
		}
	}

	return out
}

// DefaultSendHour is the fallback delivery hour when none is configured.
const DefaultSendHour = 19

// ParseSendHour parses a 0-23 hour value from a config string. It returns
// DefaultSendHour when the value is empty or out of range.
func ParseSendHour(v string) int {
	if v == "" {
		return DefaultSendHour
	}
	n := 0
	for _, r := range v {
		if r < '0' || r > '9' {
			return DefaultSendHour
		}
		n = n*10 + int(r-'0')
		if n > 23 {
			return DefaultSendHour
		}
	}
	if n < 0 || n > 23 {
		return DefaultSendHour
	}
	return n
}

// Step delivers due questions for plans that have not run today and whose
// configured send hour has passed.
func (s *Service) Step(ctx context.Context, now time.Time) (bool, error) {
	if s.Repo == nil || s.Recall == nil {
		return false, nil
	}
	if monitor, ok := s.Repo.(MonitoringStore); ok {
		if e := monitor.DiscoverPlans(ctx, s.sendHour()); e != nil {
			return false, e
		}
	}
	plans, err := s.Repo.DuePlans(ctx, now)
	if err != nil {
		return false, err
	}
	worked := false
	for _, plan := range plans {
		if durable, ok := s.Repo.(DeliveryStore); ok {
			acquired, e := durable.AcquirePlan(ctx, plan, now)
			if e != nil {
				return worked, e
			}
			if !acquired {
				continue
			}
		}
		if err := s.deliver(ctx, plan, now); err != nil {
			if durable, ok := s.Repo.(DeliveryStore); ok {
				_ = durable.ReleasePlan(ctx, plan.ID, now.Add(time.Minute))
			}
			slog.WarnContext(ctx, "recallplan deliver failed", "student", plan.StudentID, "err", err)
			continue
		}
		worked = true
	}
	return worked, nil
}

func (s *Service) deliver(ctx context.Context, plan Plan, now time.Time) error {
	date := LocalTime(plan, now).Format("2006-01-02")
	// 1. Detect currently-due knowledge points.
	items, err := s.Recall.DueItems(ctx, plan.StudentID, now)
	if err != nil {
		return err
	}
	// 2. Enqueue due questions (dedup against already-pending/sent).
	queue := make([]Item, 0, len(items))
	for _, it := range items {
		if len(it.QuestionIDs) > 0 {
			queue = append(queue, Item{QuestionID: it.QuestionIDs[0], Area: it.Area, Topic: it.Topic, Retrievability: it.Retrievability})
		}
	}
	if durable, ok := s.Repo.(DeliveryStore); ok {
		if e := durable.SyncPending(ctx, plan.ID, queue); e != nil {
			return e
		}
	}
	if _, err := s.Repo.AddItems(ctx, plan.ID, queue); err != nil {
		return err
	}
	return s.Repo.MarkRan(ctx, plan.ID, date)
}

// ClaimNotification opens a review only in response to an explicit student action.
func (s *Service) ClaimNotification(ctx context.Context, student string, itemID int64) (string, error) {
	plan, err := s.Repo.Plan(ctx, student)
	if err != nil {
		return "", err
	}
	store, ok := s.Repo.(NotificationClaimStore)
	if !ok {
		return "", errors.New("notification claims unavailable")
	}
	item, err := store.NotificationItem(ctx, plan.ID, itemID)
	if err != nil {
		return "", err
	}
	if item.ConversationID != "" {
		return item.ConversationID, nil
	}
	if item.Status != ItemPending {
		return "", ErrConflict
	}
	if s.Questions == nil || s.Conversations == nil {
		return "", errors.New("review unavailable")
	}
	question, err := s.Questions.Question(ctx, student, item.QuestionID)
	if err != nil {
		return "", err
	}
	opening := "## 防遗忘复习\n\n" + question.Question.Stem + "\n\n请先用自己的话复述解题思路和关键步骤，也可以写出计算过程。提交后老师再帮你核对。"
	request := fmt.Sprintf("recall-%d-%d", plan.ID, item.ID)
	sink, ok := s.Conversations.(interface {
		OpenReview(context.Context, string, string, string, string) (string, error)
	})
	if !ok {
		return "", errors.New("review sink unavailable")
	}
	conversation, err := sink.OpenReview(ctx, student, request, opening, item.QuestionID)
	if err != nil {
		return "", err
	}
	if err = s.Repo.MarkSent(ctx, item.ID, conversation, time.Now()); err != nil {
		existing, readErr := store.NotificationItem(ctx, plan.ID, item.ID)
		if readErr == nil && existing.ConversationID == conversation {
			return conversation, nil
		}
		return "", err
	}
	return conversation, nil
}

// buildOpening produces a model-generated, personalized opening message that
// presents the question and prompts a Feynman-style re-derivation. It must not
// contain the answer.
func (s *Service) buildOpening(ctx context.Context, student string, it Item) (string, error) {
	stem := ""
	if s.Questions != nil {
		v, e := s.Questions.Question(ctx, student, it.QuestionID)
		if e != nil {
			return "", e
		}
		stem = v.Question.Stem
	}
	if s.Model == nil {
		// Fallback: a minimal, answer-free prompt.
		return "这是一道防遗忘复习题，请先自己动手做一遍，再用自己的话讲清解题思路。\n\n" + stem, nil
	}
	sp := "你是数学老师，正在给学生发一道防遗忘复习题。生成一条自然的开场消息：先简短说明这是之前做过的题、现在到了该复习的时机，再呈现题目，引导学生先用费曼法自述思路或动笔做一遍。语气亲切、不啰嗦、不直接给答案或提示解题方向。"
	payload := map[string]any{"topic": it.Topic, "stem": stem}
	msg, err := s.Model.Generate(modelstream.Without(ctx), []*schema.Message{
		schema.SystemMessage(sp),
		schema.UserMessage(mustJSON(payload)),
	})
	if err != nil {
		return "", err
	}
	if msg == nil || msg.Role != schema.Assistant || strings.TrimSpace(msg.Content) == "" {
		return "", errors.New("recallplan: empty opening")
	}
	return msg.Content, nil
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// OnEvidence consumes the durable memory outbox. Only the bound original
// question is completed; other answers in the conversation do not count.
func (s *Service) OnEvidence(ctx context.Context, event memory.EvidenceEvent) error {
	if e := s.EnsurePlan(ctx, event.StudentID, event.At); e != nil {
		return e
	}
	p, e := s.Repo.Plan(ctx, event.StudentID)
	if errors.Is(e, ErrNotFound) {
		return nil
	}
	if e != nil {
		return e
	}
	items, e := s.Repo.SentItemsIn(ctx, p.ID, []string{event.ConversationID})
	if e != nil {
		return e
	}
	for _, it := range items {
		if it.QuestionID == event.QuestionID {
			if e = s.Repo.MarkReviewed(ctx, it.ID); e != nil && !errors.Is(e, ErrConflict) {
				return e
			}
		}
	}
	return nil
}
