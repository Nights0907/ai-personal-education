// Package query defines the web input contract persisted as Agent Store user messages.
package query

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const MaxQueryRunes = 20000

var (
	ErrInvalid  = errors.New("query: invalid input")
	ErrConflict = errors.New("query: request ID already used for different input")
	ErrNotFound = errors.New("query: not found")
	identifier  = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
)

type Input struct {
	RadarSourceID   string    `json:"radar_source_id,omitempty"`
	RadarSeed       int       `json:"radar_seed,omitempty"`
	SubmittedAt     time.Time `json:"-"` // server-owned acceptance time for queued work
	RegenerateOf    string    `json:"regenerate_of,omitempty"`
	Query           string    `json:"query"`
	ConversationID  string    `json:"conversation_id"`
	ClientRequestID string    `json:"client_request_id"`
	ImageAssetID    string    `json:"image_asset_id,omitempty"`
	// Mode selects the teaching entry: "solve" (default) produces and commits
	// the full solution then shows it; "guide" produces and commits the same
	// solution but withholds it, entering step-by-step guided tutoring instead.
	Mode string `json:"mode,omitempty"`
	// GuideQuestionID, when Mode is "guide", references the already-committed
	// question the tutor is guiding the student through.
	GuideQuestionID string `json:"guide_question_id,omitempty"`
}

// SolveMode is the default entry: produce the full solution and show it.
const SolveMode = "solve"

// GuideMode produces and commits the full solution but withholds it, then
// guides the student step by step through the question.
const GuideMode = "guide"
const ReviewMode = "review"

func (in *Input) IsGuide() bool { return in.Mode == GuideMode }

func (in *Input) Normalize() {
	if in.ImageAssetID != "" && strings.TrimSpace(in.Query) == "" {
		in.Query = "[图片题目]"
	}
}

func (in Input) Validate() error {
	if in.RadarSourceID != "" && (!ValidID(in.RadarSourceID) || in.RadarSeed < 1 || in.RadarSeed > 1000000000) {
		return ErrInvalid
	}
	if in.RegenerateOf != "" && !ValidID(in.RegenerateOf) {
		return ErrInvalid
	}
	if in.ImageAssetID != "" && !ValidID(in.ImageAssetID) {
		return ErrInvalid
	}
	if in.Mode != "" && in.Mode != SolveMode && in.Mode != GuideMode && in.Mode != ReviewMode {
		return ErrInvalid
	}
	if in.GuideQuestionID != "" && !ValidID(in.GuideQuestionID) {
		return ErrInvalid
	}
	if !utf8.ValidString(in.Query) || strings.TrimSpace(in.Query) == "" || utf8.RuneCountInString(in.Query) > MaxQueryRunes || !identifier.MatchString(in.ConversationID) || !identifier.MatchString(in.ClientRequestID) {
		return ErrInvalid
	}
	return nil
}
func (in Input) Hash() string {
	b, _ := json.Marshal(in)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

type Record struct {
	TurnID string `json:"turn_id,omitempty"`
	ID     string `json:"id"`
	Input
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}
type Cursor struct {
	CreatedAt time.Time `json:"created_at"`
	ID        string    `json:"id"`
}
type Page struct {
	Items      []Record `json:"items"`
	NextCursor string   `json:"next_cursor,omitempty"`
}
type Repository interface {
	Create(context.Context, string, Input) (Record, bool, error)
	Get(context.Context, string, string) (Record, error)
	List(context.Context, string, string, *Cursor, int) ([]Record, bool, error)
}

func ValidID(id string) bool { return identifier.MatchString(id) }
