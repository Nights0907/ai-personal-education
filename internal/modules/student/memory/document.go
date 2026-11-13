// Package memory defines student-owned long-term memory, separate from the
// conversation compactor. Documents use virtual .md paths and YAML values.
package memory

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const SchemaVersion = 2

// LegacySchemaVersion documents remain readable; writes always use SchemaVersion.
const LegacySchemaVersion = 1

var studentPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

type Paths struct {
	Index   string
	Summary string
	Content string
}

// MonthlyPaths accepts canonical months only. Student IDs come from the
// authenticated server identity, never from model arguments.
func MonthlyPaths(studentID, month string) (Paths, error) {
	if !studentPattern.MatchString(studentID) {
		return Paths{}, errors.New("student memory: invalid student ID")
	}
	t, err := time.Parse("2006-01", month)
	if err != nil || t.Format("2006-01") != month {
		return Paths{}, errors.New("student memory: invalid month")
	}
	base := "/user/memory/" + studentID + "/" + month
	return Paths{
		Index:   "/user/memory/indexes/" + studentID + "/" + month + ".md",
		Summary: base + "/meta.md",
		Content: base + "/content.md",
	}, nil
}

// IndexContent intentionally contains no summary or raw dialogue. It is the
// deterministic projection of L2's knowledge areas.
type IndexContent struct {
	Navigation *TeachingNavigation `yaml:"navigation,omitempty" json:"navigation,omitempty"`
	Keywords   []string            `yaml:"keywords" json:"keywords"`
	TurnCount  int                 `yaml:"turn_count" json:"turn_count"`
	Span       Span                `yaml:"span" json:"span"`
}

type Span struct {
	First string `yaml:"first" json:"first"`
	Last  string `yaml:"last" json:"last"`
}

type Evidence struct {
	ConversationID string   `yaml:"conversation_id" json:"conversation_id"`
	BranchID       string   `yaml:"branch_id" json:"branch_id"`
	TurnID         string   `yaml:"turn_id" json:"turn_id"`
	MessageIDs     []string `yaml:"message_ids" json:"message_ids"`
	QuestionID     string   `yaml:"question_id,omitempty" json:"question_id,omitempty"`
	At             string   `yaml:"at,omitempty" json:"at,omitempty"`
}

// Mastery levels are server-adjudicated, derived only from student-authored turns.
const (
	MasteryUnseen   = "unseen"
	MasteryFamiliar = "familiar"
	MasteryPartial  = "partial"
	MasteryMastered = "mastered"
)

// Topic is a single knowledge point's aggregated mastery state.
type Topic struct {
	EvidenceVersion    int    `yaml:"evidence_version,omitempty" json:"evidence_version,omitempty"`
	EvidenceCount      int    `yaml:"evidence_count,omitempty" json:"evidence_count,omitempty"`
	IndependentCorrect int    `yaml:"independent_correct,omitempty" json:"independent_correct,omitempty"`
	AssistedAttempts   int    `yaml:"assisted_attempts,omitempty" json:"assisted_attempts,omitempty"`
	NextAction         string `yaml:"next_action,omitempty" json:"next_action,omitempty"`

	Assisted     bool     `yaml:"assisted,omitempty" json:"assisted,omitempty"`
	Incorrect    bool     `yaml:"incorrect,omitempty" json:"incorrect,omitempty"`
	TopicID      string   `yaml:"topic_id,omitempty" json:"topic_id,omitempty"`
	BestMastery  string   `yaml:"best_mastery,omitempty" json:"best_mastery,omitempty"`
	TotalErrors  int      `yaml:"total_errors,omitempty" json:"total_errors,omitempty"`
	StateVersion int      `yaml:"state_version,omitempty" json:"state_version,omitempty"`
	Topic        string   `yaml:"topic" json:"topic"`
	Mastery      string   `yaml:"mastery" json:"mastery"`
	Ability      string   `yaml:"ability" json:"ability"`
	ErrorCount   int      `yaml:"error_count" json:"error_count"`
	LastSeen     string   `yaml:"last_seen" json:"last_seen"`
	Stability    float64  `yaml:"stability" json:"stability"`
	Difficulty   float64  `yaml:"difficulty" json:"difficulty"`
	QuestionIDs  []string `yaml:"question_ids" json:"question_ids"`
}

type KnowledgeArea struct {
	Area   string  `yaml:"area" json:"area"`
	Topics []Topic `yaml:"topics" json:"topics"`
}

// SummaryContent is the L2 mastery profile, aggregated by standardized area.
type SummaryContent struct {
	// v1 facts remain readable and preserved, but are never promoted into
	// independently observed mastery without new student evidence.
	LegacyKeywords    []string        `yaml:"keywords,omitempty" json:"legacy_keywords,omitempty"`
	LegacyInformation []any           `yaml:"key_information,omitempty" json:"legacy_information,omitempty"`
	Summary           string          `yaml:"summary" json:"summary"`
	KnowledgeAreas    []KnowledgeArea `yaml:"knowledge_areas" json:"knowledge_areas"`
	TurnCount         int             `yaml:"turn_count" json:"turn_count"`
	Span              Span            `yaml:"span" json:"span"`
}

// OriginalTurn preserves public messages, not model reasoning or tool secrets.
// Identity includes conversation and branch: a turn ID alone is not a source key.
type AttemptAssessment struct {
	Authored  bool   `yaml:"authored" json:"authored"`
	Mastery   string `yaml:"mastery" json:"mastery"`
	Incorrect bool   `yaml:"incorrect" json:"incorrect"`
}
type AttemptEvaluation struct {
	Verified  bool `yaml:"verified" json:"verified"`
	Incorrect bool `yaml:"incorrect" json:"incorrect"`
}
type OriginalTurn struct {
	ConversationTurn   int                `yaml:"conversation_turn,omitempty" json:"conversation_turn,omitempty"`
	Purpose            string             `yaml:"purpose,omitempty" json:"purpose,omitempty"`
	Evaluation         *AttemptEvaluation `yaml:"evaluation,omitempty" json:"evaluation,omitempty"`
	Assisted           bool               `yaml:"assisted,omitempty" json:"assisted,omitempty"`
	AttemptUnavailable bool               `yaml:"attempt_unavailable,omitempty" json:"attempt_unavailable,omitempty"`
	AttemptOCR         string             `yaml:"attempt_ocr,omitempty" json:"attempt_ocr,omitempty"`
	AttemptOCRID       string             `yaml:"attempt_ocr_id,omitempty" json:"attempt_ocr_id,omitempty"`
	Assessment         *AttemptAssessment `yaml:"assessment,omitempty" json:"assessment,omitempty"`
	Source             Evidence           `yaml:"source" json:"source"`
	Seq                int                `yaml:"seq" json:"seq"`
	InputType          string             `yaml:"input_type,omitempty" json:"input_type,omitempty"`
	User               string             `yaml:"user" json:"user"`
	Assistant          string             `yaml:"assistant" json:"assistant"`
	CreatedAt          time.Time          `yaml:"created_at" json:"created_at"`
	ImageAssetID       string             `yaml:"image_asset_id,omitempty" json:"image_asset_id,omitempty"`
	QuestionText       string             `yaml:"question_text,omitempty" json:"question_text,omitempty"`
	// KnowledgeTags carries answer-free question metadata mapped onto the memory
	// taxonomy: the knowledge-area codes and a difficulty tier. It never holds
	// the answer or analysis, so it is safe to feed the extractor as background.
	KnowledgeTags []string `yaml:"knowledge_tags,omitempty" json:"knowledge_tags,omitempty"`
	Difficulty    string   `yaml:"difficulty,omitempty" json:"difficulty,omitempty"`
}

type RawContent struct {
	Turns []OriginalTurn `yaml:"turns" json:"turns"`
}

// Document provides versioned metadata around each layer's content. L3 is a
// leaf, so next_paths must be an empty list rather than an invented L4 path.
type Document[T any] struct {
	SchemaVersion int       `yaml:"schema_version" json:"schema_version"`
	Layer         string    `yaml:"layer" json:"layer"`
	StudentID     string    `yaml:"student_id" json:"student_id"`
	Month         string    `yaml:"month" json:"month"`
	Revision      int64     `yaml:"revision" json:"revision"`
	UpdatedAt     time.Time `yaml:"updated_at" json:"updated_at"`
	NextPaths     []string  `yaml:"next_paths" json:"next_paths"`
	Content       T         `yaml:"content" json:"content"`
}

// Encode and Decode use explicit limits supplied by the calling storage layer.
// Monthly raw data may exceed the tool's read budget; callers must paginate it.
func Encode[T any](d Document[T], maxBytes int) ([]byte, error) {
	if err := d.Validate(); err != nil {
		return nil, err
	}
	b, err := yaml.Marshal(d)
	if err == nil && (maxBytes <= 0 || len(b) > maxBytes) {
		err = errors.New("student memory: document exceeds byte limit")
	}
	if err != nil {
		return nil, err
	}
	return b, nil
}

func Decode[T any](b []byte, maxBytes int) (Document[T], error) {
	var d Document[T]
	if maxBytes <= 0 || len(b) > maxBytes {
		return d, errors.New("student memory: document exceeds byte limit")
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&d); err != nil {
		return d, err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return d, errors.New("student memory: expected a single YAML document")
	}
	return d, d.Validate()
}

func (d Document[T]) Validate() error {
	switch any(d.Content).(type) {
	case IndexContent:
		if d.Layer != "L1" {
			return errors.New("student memory: wrong index layer")
		}
	case SummaryContent:
		if d.Layer != "L2" {
			return errors.New("student memory: wrong summary layer")
		}
	case RawContent:
		if d.Layer != "L3" {
			return errors.New("student memory: wrong raw layer")
		}
	default:
		return errors.New("student memory: unsupported document content")
	}
	p, err := MonthlyPaths(d.StudentID, d.Month)
	if err != nil {
		return err
	}
	if (d.SchemaVersion != SchemaVersion && d.SchemaVersion != LegacySchemaVersion) || d.Revision < 1 || d.UpdatedAt.IsZero() {
		return errors.New("student memory: invalid document metadata")
	}
	var next string
	switch d.Layer {
	case "L1":
		next = p.Summary
	case "L2":
		next = p.Content
	case "L3":
		if d.NextPaths == nil || len(d.NextPaths) != 0 {
			return errors.New("student memory: leaf requires empty next_paths")
		}
		return nil
	default:
		return fmt.Errorf("student memory: undefined layer %q", d.Layer)
	}
	if len(d.NextPaths) != 1 || d.NextPaths[0] != next || strings.Contains(next, "..") {
		return errors.New("student memory: invalid next-layer path")
	}
	return nil
}
