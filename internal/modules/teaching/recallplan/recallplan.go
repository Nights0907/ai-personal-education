// Package recallplan implements the forgetting-prevention follow-up plan: a
// stateful, scheduled deliverer that detects knowledge points whose
// retrievability has decayed past the recall threshold and, once a day, sends
// a fixed number of due questions to the student in a fresh conversation.
//
// It is distinct from memory.RecallPlan, the stateless detector this module
// consumes. Here "plan" means the durable bookkeeping that remembers which
// questions were already sent, so no question is delivered twice.
package recallplan

import (
	"context"
	"errors"
	"time"
)

var (
	ErrNotFound = errors.New("recallplan: not found")
	ErrConflict = errors.New("recallplan: stale state")
)

// Plan statuses.
const (
	StatusActive    = "ACTIVE"
	StatusPaused    = "PAUSED"
	StatusCompleted = "COMPLETED"
)

// Item statuses.
const (
	ItemPending  = "PENDING"
	ItemSent     = "SENT"
	ItemReviewed = "REVIEWED"
)

// Status is a read-only snapshot of a student's forgetting-prevention plan for
// display: the plan's schedule plus item counts by state.
type Status struct {
	Notifications []ReviewNotification `json:"notifications,omitempty"`
	Timezone      string               `json:"timezone"`
	HasPlan       bool                 `json:"has_plan"`
	DailyQuota    int                  `json:"daily_quota"`
	SendHour      int                  `json:"send_hour"`
	LastRunDate   string               `json:"last_run_date"`
	Pending       int                  `json:"pending"`
	Sent          int                  `json:"sent"`
	Reviewed      int                  `json:"reviewed"`
}

// StatusStore yields item counts by status for a plan. It is a separate,
// read-only interface so the core Repository is not burdened with reporting.
type StatusStore interface {
	PlanItemCounts(context.Context, int64) (pending, sent, reviewed int, err error)
}

// Plan is one student's active forgetting-prevention plan.
type Plan struct {
	Timezone    string
	ID          int64
	StudentID   string
	Status      string
	DailyQuota  int
	SendHour    int
	LastRunDate string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Item is a queued question awaiting delivery.
type Item struct {
	ID             int64
	PlanID         int64
	QuestionID     string
	Area           string
	Topic          string
	Retrievability float64
	Status         string
	SentAt         *time.Time
	ConversationID string
	CreatedAt      time.Time
}

// Repository is the durable store for plans and their queued items.
type Repository interface {
	Migrate(context.Context) error
	// Plan returns the active plan, or ErrNotFound.
	Plan(context.Context, string) (Plan, error)
	// UpsertPlan creates or updates a plan and returns its current row.
	UpsertPlan(context.Context, string, int, int) (Plan, error)
	// AddItems enqueues questions, skipping any that already have a pending/sent
	// entry for the same question in the same plan (dedup).
	AddItems(context.Context, int64, []Item) (int, error)
	// PendingItems lists undelivered items ordered by forgetting priority
	// (lowest retrievability first).
	PendingItems(context.Context, int64, int) ([]Item, error)
	// MarkSent records delivery: status -> SENT, sets sent_at and conversation.
	MarkSent(context.Context, int64, string, time.Time) error
	// MarkReviewed records the student answered: status -> REVIEWED.
	MarkReviewed(context.Context, int64) error
	// SentItemsIn lists SENT items whose conversation matches one of the given
	// IDs, i.e. review conversations the student has actually answered in.
	SentItemsIn(context.Context, int64, []string) ([]Item, error)
	// DuePlans lists active plans whose last_run_date is before today.
	DuePlans(context.Context, time.Time) ([]Plan, error)
	// MarkRan records today's delivery, preventing a second run the same day.
	MarkRan(context.Context, int64, string) error
}

func LocalTime(p Plan, now time.Time) time.Time {
	zone := p.Timezone
	if zone == "" {
		zone = "Asia/Shanghai"
	}
	loc, e := time.LoadLocation(zone)
	if e != nil {
		loc = time.FixedZone("Asia/Shanghai", 8*3600)
	}
	return now.In(loc)
}

type MonitoringStore interface {
	DiscoverPlans(context.Context, int) error
}
type DeliveryStore interface {
	AcquirePlan(context.Context, Plan, time.Time) (bool, error)
	ReleasePlan(context.Context, int64, time.Time) error
	SyncPending(context.Context, int64, []Item) error
	SentBetween(context.Context, int64, time.Time, time.Time) (int, error)
	CancelItem(context.Context, int64) error
}

type NotificationClaimStore interface {
	NotificationItem(context.Context, int64, int64) (Item, error)
}

type ReviewNotification struct {
	QuestionPreview string    `json:"question_preview,omitempty"`
	ID              int64     `json:"id"`
	Topic           string    `json:"topic"`
	Retrievability  float64   `json:"retrievability"`
	QuestionID      string    `json:"question_id"`
	ConversationID  string    `json:"conversation_id"`
	SentAt          time.Time `json:"sent_at"`
}
type NotificationStore interface {
	ReviewNotifications(context.Context, int64) ([]ReviewNotification, error)
}
