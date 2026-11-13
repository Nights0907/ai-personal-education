package memory

import (
	"context"
	"errors"
	"time"
)

var ErrNotFound = errors.New("student memory: not found")
var ErrConflict = errors.New("student memory: stale revision or lease")

type BasicProfile struct {
	Name                  string   `json:"name"`
	Grade                 string   `json:"grade"`
	LearningGoals         []string `json:"learning_goals"`
	ExplanationPreference string   `json:"explanation_preference"`
}

// Observations are suggestions, never a diagnosis or proof of mastery.
type Profile struct {
	StudentID string       `json:"student_id"`
	Revision  int64        `json:"revision"`
	Basic     BasicProfile `json:"basic"`
	UpdatedAt time.Time    `json:"updated_at"`
}

type PendingTurn struct {
	ID               int64
	StudentID, Month string
	Raw              OriginalTurn
}

type Claim struct {
	StudentID, Token, Reason string
	ConversationVersion      int64
	Turns                    []PendingTurn
}

type Repository interface {
	Profile(context.Context, string) (Profile, error)
	SaveBasic(context.Context, string, BasicProfile, int64) (Profile, error)
	Discover(context.Context) error
	Claim(context.Context, time.Time) (Claim, error)
	Complete(context.Context, Claim) error
	Retry(context.Context, Claim, time.Time) error
	Status(context.Context, string) (any, error)
}

type Month struct {
	Index   Document[IndexContent]
	Summary Document[SummaryContent]
	Raw     Document[RawContent]
	// Encoded provides compare-and-swap expectations. It never enters prompts.
	Encoded [3]string
}

type Documents interface {
	Load(context.Context, string, string) (Month, error)
	Publish(context.Context, string, string, Month, Month) error
	Indexes(context.Context, string, int) ([]Document[IndexContent], error)
}

// ExtractedTopic carries the model's candidate observation of one knowledge
// point on one authored turn. Mastery is adjudicated server-side, not trusted
// from the model.
type ExtractedTopic struct {
	TopicID    string   `json:"topic_id,omitempty"`
	Area       string   `json:"area"`
	Topic      string   `json:"topic"`
	Ability    string   `json:"ability"`
	Suggested  string   `json:"suggested"`
	Authored   bool     `json:"authored"`
	Incorrect  bool     `json:"incorrect"`
	QuestionID string   `json:"question_id"`
	SourceIDs  []string `json:"source_ids"`
}
type Extracted struct {
	Summary string           `json:"summary"`
	Topics  []ExtractedTopic `json:"topics"`
}

// QuestionMeta carries objective, answer-free facts about one question so the
// extractor can calibrate mastery against the question's own difficulty and
// knowledge point. It must never contain the answer, the analysis, or the
// standard solution — those would let the model copy the reference instead of
// observing the student's actual authored turn.
type QuestionMeta struct {
	QuestionID    string   `json:"question_id"`
	KnowledgeTags []string `json:"knowledge_tags,omitempty"` // mapped memory taxonomy codes
	Difficulty    string   `json:"difficulty,omitempty"`     // free-form tier, e.g. easy/medium/hard
	IsVariant     bool     `json:"is_variant,omitempty"`
}

// RecentTopic is a read-only projection of L2's already-adjudicated anchor
// state (error_count / last_seen / stability). It gives the extractor the
// recent history of a knowledge point without re-deriving it, so repeated
// errors or long gaps are visible to the model.
type RecentTopic struct {
	TopicID    string  `json:"topic_id"`
	Area       string  `json:"area"`
	Topic      string  `json:"topic"`
	ErrorCount int     `json:"error_count"`
	LastSeen   string  `json:"last_seen"`
	Stability  float64 `json:"stability"`
	Mastery    string  `json:"mastery"`
}

// QuestionContext is the second, separated input to Extract. It is background
// only: the model must still judge mastery solely from the student's authored
// turn, never from the question's standard answer.
type QuestionContext struct {
	QuestionMeta map[string]QuestionMeta `json:"question_meta,omitempty"` // keyed by SourceID
	RecentTopics []RecentTopic           `json:"recent_topics,omitempty"`
}

type Extractor interface {
	Extract(context.Context, SummaryContent, QuestionContext, []OriginalTurn) (Extracted, error)
}

func SourceID(e Evidence) string {
	return e.ConversationID + "/" + e.BranchID + "/" + e.TurnID
}

// Due is independent of polling and model calls, so timing behavior is testable.
func Due(pending int, newConversation bool, lastInput, now time.Time) string {
	if pending == 0 {
		return ""
	}
	if newConversation {
		return "new_conversation"
	}
	if pending >= 10 {
		return "ten_turns"
	}
	if !lastInput.IsZero() && !now.Before(lastInput.Add(2*time.Hour)) {
		return "idle_two_hours"
	}
	return ""
}

type EvidenceEvent struct {
	ID                                              int64
	StudentID, ConversationID, QuestionID, SourceID string
	At                                              time.Time
}
type EvidenceEvents interface {
	PendingEvidence(context.Context, time.Time) (EvidenceEvent, error)
	CompleteEvidence(context.Context, int64) error
	RetryEvidence(context.Context, int64, time.Time) error
}
