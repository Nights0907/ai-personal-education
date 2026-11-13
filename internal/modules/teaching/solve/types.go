package solve

import (
	"context"
	"errors"
	"github.com/cloudwego/eino/schema"
	"github.com/musi-ai/education/internal/agent/runtime/state"
	"time"
)

var ErrInvalid = errors.New("solve: invalid candidate")
var ErrReview = errors.New("solve: mathematical review not passed")

type Option struct {
	Key  string `json:"key"`
	Text string `json:"text"`
}
type Subquestion struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}
type Question struct {
	Stem         string        `json:"stem"`
	Type         string        `json:"type"`
	Difficulty   string        `json:"difficulty"`
	Options      []Option      `json:"options"`
	Subquestions []Subquestion `json:"subquestions"`
}

// Difficulty tiers are the question's objective difficulty, judged at solve time.
// It is answer-free metadata consumed by the student-memory extractor.
const (
	DifficultyEasy   = "easy"
	DifficultyMedium = "medium"
	DifficultyHard   = "hard"
)

var validDifficulty = map[string]bool{
	DifficultyEasy:   true,
	DifficultyMedium: true,
	DifficultyHard:   true,
}

type Answer struct {
	SubquestionID *string `json:"subquestion_id"`
	Content       string  `json:"content"`
}
type Step struct {
	StepID        string  `json:"step_id,omitempty"`
	Ordinal       int     `json:"ordinal"`
	SubquestionID *string `json:"subquestion_id"`
	Title         string  `json:"title"`
	Content       string  `json:"content"`
}
type Knowledge struct {
	Code      string `json:"code"`
	IsPrimary bool   `json:"is_primary"`
}
type Solution struct {
	TeacherResponse string      `json:"teacher_response,omitempty"`
	Answer          []Answer    `json:"answer"`
	Analysis        string      `json:"analysis"`
	Steps           []Step      `json:"steps"`
	KnowledgePoints []Knowledge `json:"knowledge_points"`
}
type Candidate struct {
	Question Question `json:"question"`
	Solution Solution `json:"solution"`
}
type Review struct {
	Passed bool     `json:"passed"`
	Issues []string `json:"issues"`
}
type View struct {
	Committed            bool      `json:"committed"`
	ResultID             string    `json:"result_id"`
	QuestionID           string    `json:"question_id"`
	SolutionID           string    `json:"solution_id"`
	ConversationID       string    `json:"conversation_id"`
	SourceMessageID      string    `json:"source_message_id"`
	TurnID               string    `json:"turn_id"`
	OriginalText         string    `json:"original_text"`
	InputType            string    `json:"input_type"`
	SourceImageObjectKey *string   `json:"source_image_object_key"`
	OCRResultObjectKey   *string   `json:"ocr_result_object_key"`
	Question             Question  `json:"question"`
	Solution             Solution  `json:"solution"`
	ModelRef             string    `json:"model_ref"`
	ReviewStatus         string    `json:"review_status"`
	CreatedAt            time.Time `json:"created_at"`
	HistoryStatus        string    `json:"history_status"`
}
type Saved struct {
	Input   state.Input
	Receipt state.Receipt
	View    View
}
type Repository interface {
	SaveDraft(context.Context, state.Input, string, Candidate, Review) (string, error)
	Commit(context.Context, state.Input, string, string, state.TerminalObservation, []*schema.Message) (Saved, error)
	Find(context.Context, state.Scope, string) (Saved, error)
	Get(context.Context, string, string) (View, error)
	List(context.Context, string, int) ([]View, error)
	RepairPending(context.Context, func(context.Context, Saved) error) error
}
