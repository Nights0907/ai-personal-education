// Package state defines runtime records; business facts remain in application modules.
package state

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/cloudwego/eino/schema"
)

var (
	ErrConflict   = errors.New("runtime: conflict or stale lease")
	ErrNotFound   = errors.New("runtime: not found")
	ErrBudget     = errors.New("CONTEXT_BUDGET_EXCEEDED: context compaction required")
	ErrProtocol   = errors.New("runtime: incomplete message protocol")
	ErrIncomplete = errors.New("AGENT_INCOMPLETE: turn did not satisfy completion policy")
)

type Scope struct{ Namespace, SubjectID, ConversationID string }

func (s Scope) Validate() error {
	if s.Namespace == "" || s.SubjectID == "" || s.ConversationID == "" {
		return errors.New("runtime: full authenticated scope required")
	}
	return nil
}
func Hash(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func (s Scope) Key() string { return Hash(s) }

type Identity struct {
	Scope
	TurnID, RunID string
}
type Lease struct {
	Token int64
	Until time.Time
}
type Input struct {
	Identity                        Identity
	InputRef, BindingRef, ConfigRef string
	Lease                           Lease
}

func (i Input) Validate() error {
	if err := i.Identity.Scope.Validate(); err != nil {
		return err
	}
	if i.Identity.TurnID == "" || i.Identity.RunID == "" || i.InputRef == "" || i.BindingRef == "" || i.ConfigRef == "" || i.Lease.Token <= 0 || !i.Lease.Until.After(time.Now()) {
		return errors.New("runtime: input references, identity and live fencing lease required")
	}
	return nil
}

type Policy struct {
	MaxInputTokens   int    `json:"max_input_tokens"`
	OutputReserve    int    `json:"output_reserve"`
	TriggerPercent   int    `json:"trigger_percent"`
	KeepRecentTurns  int    `json:"keep_recent_turns"`
	MaxSummaryTokens int    `json:"max_summary_tokens"`
	Version          string `json:"version"`
}

func DefaultPolicy() Policy { return Policy{16384, 4096, 80, 4, 2048, "async-v2-80"} }
func (p Policy) Validate() error {
	if p.MaxInputTokens <= p.OutputReserve || p.OutputReserve < 1 || p.TriggerPercent < 1 || p.TriggerPercent > 100 || p.KeepRecentTurns < 1 || p.MaxSummaryTokens < 1 || p.Version == "" {
		return errors.New("runtime: invalid history policy")
	}
	return nil
}
func (p Policy) InputLimit() int { return p.MaxInputTokens - p.OutputReserve }

type Spec struct {
	Name, Version, Instruction, ModelRef string
	InstructionFile                      string
	ToolRefs, SkillRefs, TerminalTools   []string
	MaxIterations, MaxToolCalls          int
	Timeout                              time.Duration
	History                              Policy
}
type ContextValue struct {
	Tag      string
	Slot     int
	Priority int
	Content  json.RawMessage
}

type Binding struct {
	Blocks []ContextValue
	// Only server-owned providers may construct constraints. Context values are untrusted JSON.
	Constraints    string
	SourceVersion  string
	Input          *schema.Message
	LongTermMemory json.RawMessage
}
type SkillDescriptor struct {
	Path        string `json:"path,omitempty"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}
type Snapshot struct {
	Usage                                                      ContextUsage
	Preamble, Memory                                           string
	CanonicalInput                                             *schema.Message
	PromptSource, PromptHash                                   string
	OriginalInput                                              *schema.Message
	BlockHashes                                                map[string]string
	ReadToken                                                  ReadToken
	Policy                                                     Policy
	ModelRef, InstructionHash                                  string
	CapabilityHashes                                           map[string]string
	Identity                                                   Identity
	Phase                                                      string
	ModelIteration                                             int
	SpecVersion, ConfigRef, SourceVersion, PolicyVersion, Hash string
	SummaryRevision, CoveredCursor                             int64
	HistoryCursors                                             []int64
	Messages                                                   []*schema.Message
	ToolSchemas                                                []*schema.ToolInfo
	EstimatedTokens                                            int
}

func (s Snapshot) Key() string { return fmt.Sprintf("%s:%d", s.Phase, s.ModelIteration) }

type Receipt struct {
	OriginalInput                         *schema.Message `json:",omitempty"`
	Kind                                  string          `json:",omitempty"`
	ResultID, TurnID, RunID, InputVersion string
	CommittedAt                           time.Time
	Terminal                              *TerminalObservation `json:",omitempty"`
	FrozenPrefix                          []*schema.Message    `json:",omitempty"`
}
type Result struct {
	WarningCode                                              string `json:",omitempty"`
	RunID, Status, HistoryStatus, CommittedResult, ErrorCode string
	CompletionID                                             string
}
type Turn struct {
	End           MessageCursor
	Cursor        int64
	TurnID, RunID string
	Messages      []*schema.Message
}
type Summary struct {
	GenerationUsage                    ContextUsage    `json:",omitempty"`
	CompactID                          string          `json:",omitempty"`
	Structured                         json.RawMessage `json:",omitempty"`
	ModelRef                           string
	Revision, ThroughCursor            int64
	Content, SourceHash, PolicyVersion string
}
type View struct {
	Usage   ContextUsage
	Token   ReadToken
	Turns   []Turn
	Summary Summary
}
type SummaryJob struct {
	ModelRef                    string
	ReadToken                   ReadToken
	Through                     MessageCursor
	ID                          string
	Scope                       Scope
	BaseRevision, ThroughCursor int64
	Policy                      Policy
	State                       string
	ClaimToken                  string
	LeaseUntil, NextAttempt     time.Time
	Attempts                    int
	LastError                   string
}
type Run struct {
	Usage             ContextUsage
	LastSeq           int
	Policy            Policy
	Input             Input
	Cursor            int64
	Status, ErrorCode string
	Receipt           *Receipt
	Messages          []*schema.Message
}
type SealedEvent struct {
	Attempts    int
	NextAttempt time.Time
	Failed      bool
	LastError   string
	ID          string
	Scope       Scope
	Policy      Policy
	TurnID      string
}

// CleanMessages uses Eino messages while deliberately excluding hidden reasoning,
// arbitrary provider extras and multimodal payloads. This runtime accepts OCR text only.
func CleanMessages(ms []*schema.Message) []*schema.Message {
	out := make([]*schema.Message, 0, len(ms))
	for _, m := range ms {
		if m == nil {
			continue
		}
		c := &schema.Message{Role: m.Role, Content: m.Content, Name: m.Name, ToolCallID: m.ToolCallID, ToolName: m.ToolName}
		for _, tc := range m.ToolCalls {
			c.ToolCalls = append(c.ToolCalls, schema.ToolCall{ID: tc.ID, Type: tc.Type, Function: tc.Function})
		}
		out = append(out, c)
	}
	return out
}

// ValidateProtocol rejects orphaned/duplicate calls, unfinished batches and injected system roles.
func ValidateProtocol(ms []*schema.Message) error {
	pending := map[string]string{}
	seen := map[string]bool{}
	for _, m := range ms {
		if m == nil {
			return ErrProtocol
		}
		switch m.Role {
		case schema.User, schema.Assistant:
			if len(pending) > 0 {
				return ErrProtocol
			}
			if m.Role == schema.User && len(m.ToolCalls) > 0 {
				return ErrProtocol
			}
			for _, c := range m.ToolCalls {
				if c.ID == "" || c.Function.Name == "" || seen[c.ID] {
					return ErrProtocol
				}
				pending[c.ID] = c.Function.Name
				seen[c.ID] = true
			}
		case schema.Tool:
			if pending[m.ToolCallID] == "" || (m.ToolName != "" && m.ToolName != pending[m.ToolCallID]) || len(m.ToolCalls) > 0 {
				return ErrProtocol
			}
			delete(pending, m.ToolCallID)
		default:
			return fmt.Errorf("%w: role %s", ErrProtocol, m.Role)
		}
	}
	if len(pending) > 0 {
		return ErrProtocol
	}
	return nil
}
