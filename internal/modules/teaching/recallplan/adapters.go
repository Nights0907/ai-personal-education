package recallplan

import (
	"context"
	"errors"
	"time"

	"github.com/musi-ai/education/internal/modules/student/memory"
	"github.com/musi-ai/education/internal/modules/teaching/solve"
	"github.com/musi-ai/education/internal/query"
)

// MemoryRecall adapts studentmemory.Service.RecallPlan to RecallSource, keeping
// only the items that are currently due.
type MemoryRecall struct{ Service *memory.Service }

func (m MemoryRecall) DueItems(ctx context.Context, student string, now time.Time) ([]memory.RecallItem, error) {
	plan, err := m.Service.RecallPlan(ctx, student, now)
	if err != nil {
		return nil, err
	}
	out := []memory.RecallItem{}
	for _, it := range plan.Items {
		if it.Due {
			out = append(out, it)
		}
	}
	return out, nil
}

// AgentStoreSink opens a fresh conversation and presets the opening message as
// the conversation's first user input, so the runtime picks it up on the next
// turn.
type AgentStoreSink struct {
	Queries interface {
		ConversationByRequest(context.Context, string, string) (string, error)
		CreateConversation(context.Context, string, string) (query.Conversation, error)
		Create(context.Context, string, query.Input) (query.Record, bool, error)
	}
}

func (a AgentStoreSink) Open(ctx context.Context, student, requestID, message string) (string, error) {
	return a.open(ctx, student, requestID, message, "", "")
}
func (a AgentStoreSink) OpenReview(ctx context.Context, student, requestID, message, questionID string) (string, error) {
	return a.open(ctx, student, requestID, message, query.ReviewMode, questionID)
}
func (a AgentStoreSink) open(ctx context.Context, student, requestID, message, mode, questionID string) (string, error) {
	// Delivery must be idempotent: the opening is model-authored, so a retry
	// produces different text for the same request id. Reuse the conversation
	// already opened for this request instead of failing the whole plan.
	if conversationID, err := a.Queries.ConversationByRequest(ctx, student, requestID); err == nil {
		return conversationID, nil
	} else if !errors.Is(err, query.ErrNotFound) {
		return "", err
	}
	c, err := a.Queries.CreateConversation(ctx, student, requestID)
	if err != nil {
		return "", err
	}
	if _, _, err := a.Queries.Create(ctx, student, query.Input{
		Mode: mode, GuideQuestionID: questionID,
		Query:           message,
		ConversationID:  c.ConversationID,
		ClientRequestID: requestID,
	}); err != nil {
		return "", err
	}
	return c.ConversationID, nil
}

// SolveQuestions adapts solve.Repository to QuestionSource.
type SolveQuestions struct{ Repo solve.Repository }

func (s SolveQuestions) Question(ctx context.Context, student, questionID string) (solve.View, error) {
	return s.Repo.Get(ctx, student, questionID)
}
