// Package context builds exactly one immutable entry context per run.
package context

import (
	"bytes"
	stdctx "context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/schema"
	"github.com/musi-ai/education/internal/agent/runtime/state"
)

type Counter interface {
	Count([]*schema.Message, []*schema.ToolInfo) (int, error)
}

// ConservativeCounter is a configurable budgeting approximation, NOT the model's
// tokenizer. One token per UTF-8 byte + framing intentionally overestimates Chinese.
type ConservativeCounter struct{}

func (ConservativeCounter) Count(ms []*schema.Message, ts []*schema.ToolInfo) (int, error) {
	b, err := json.Marshal(ms)
	if err != nil {
		return 0, err
	}
	t, err := json.Marshal(ts)
	return len(b) + len(t) + 16*len(ms), err
}

type Builder struct {
	Counter   Counter
	Skills    []state.SkillDescriptor
	Providers []TurnProvider
}

func (b Builder) Build(ctx stdctx.Context, in state.Input, spec state.Spec, binding state.Binding, view state.View) (state.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return state.Snapshot{}, err
	}
	if err := spec.History.Validate(); err != nil {
		return state.Snapshot{}, err
	}
	if b.Counter == nil || binding.Input == nil || binding.Input.Role != schema.User || len(binding.Input.ToolCalls) > 0 || binding.Input.ToolCallID != "" || binding.SourceVersion == "" || spec.Instruction == "" {
		return state.Snapshot{}, errors.New("runtime: invalid context dependencies or user input")
	}
	if len(binding.Input.MultiContent) > 0 || len(binding.Input.UserInputMultiContent) > 0 || len(binding.Input.AssistantGenMultiContent) > 0 {
		return state.Snapshot{}, errors.New("runtime: text-only model input required")
	}
	memoryData, err := objectJSON(binding.LongTermMemory)
	if err != nil {
		return state.Snapshot{}, fmt.Errorf("long-term memory: %w", err)
	}
	catalog, err := b.catalog(spec.SkillRefs)
	if err != nil {
		return state.Snapshot{}, err
	}
	system := spec.Instruction
	if !strings.Contains(system, "<system-prompt>") {
		system = "<system-prompt>\n" + system + "\n</system-prompt>"
	}
	blocks := map[string]string{"system-prompt": state.Hash(system)}
	providers := append([]TurnProvider(nil), b.Providers...)
	add := func(name string, slot Slot, priority int, content string) {
		blocks[name] = state.Hash(content)
		providers = append(providers, TurnProvider{Name: name, Slot: slot, Priority: priority, Provide: func(stdctx.Context, BuildInput) ([]Block, error) {
			return []Block{{Tag: name, Content: content, Markup: true}}, nil
		}})
	}
	// Keep the shared SP stable; turn-specific data belongs in UserPrefix.
	var skillBlocks []Block
	for _, item := range catalog {
		skillBlocks = append(skillBlocks, Block{Tag: "skill", Attrs: map[string]string{"name": item.Name, "path": item.Path}, Content: item.Description})
	}
	skillText, err := RenderBlocks(skillBlocks)
	if err != nil {
		return state.Snapshot{}, err
	}
	if len(catalog) > 0 {
		add("available-skills", SlotPreamble, PrioritySkills, skillText)
	}
	var memoryObject map[string]json.RawMessage
	_ = json.Unmarshal(memoryData, &memoryObject)
	if l1, ok := memoryObject["l1"]; ok {
		add("user-long-term-memory", SlotMemory, PriorityLongTermMemory, string(l1))
	}

	summary, err := historySummary(view.Summary)
	if err != nil {
		return state.Snapshot{}, err
	}
	summaryJSON, err := json.Marshal(summary)
	if err != nil {
		return state.Snapshot{}, err
	}
	add("history-summary", SlotMemory, PriorityShortTermMemory, string(summaryJSON))
	for _, value := range binding.Blocks {
		v, e := objectJSON(value.Content)
		if e != nil {
			return state.Snapshot{}, e
		}
		encoded, e := json.Marshal(v)
		if e != nil {
			return state.Snapshot{}, e
		}
		add(value.Tag, Slot(value.Slot), value.Priority, string(encoded))
	}
	if binding.Constraints != "" {
		providers = append(providers, TurnProvider{Name: "runtime-constraints", Slot: SlotUserPrefix, Priority: 200, Provide: func(stdctx.Context, BuildInput) ([]Block, error) {
			return []Block{{Tag: "runtime-constraints", Content: binding.Constraints}}, nil
		}})
	}
	snap := state.Snapshot{Identity: in.Identity, SpecVersion: spec.Version, ConfigRef: in.ConfigRef, SourceVersion: binding.SourceVersion, PolicyVersion: spec.History.Version, SummaryRevision: view.Summary.Revision, CoveredCursor: view.Summary.ThroughCursor}
	snap.OriginalInput = state.CleanMessages([]*schema.Message{binding.Input})[0]
	snap.PromptSource = spec.InstructionFile
	if snap.PromptSource == "" {
		snap.PromptSource = "inline"
	}
	snap.PromptHash = state.Hash(spec.Instruction)
	snap.BlockHashes = blocks
	snap.Policy = spec.History
	snap.ReadToken = view.Token
	snap.ModelRef = spec.ModelRef
	snap.InstructionHash = state.Hash(system)
	var history []*schema.Message
	last := view.Summary.ThroughCursor
	for _, t := range view.Turns {
		if t.Cursor <= view.Summary.ThroughCursor {
			continue
		}
		if t.Cursor <= last {
			return snap, errors.New("runtime: history cursors not ordered")
		}
		if err := state.ValidateProtocol(t.Messages); err != nil {
			return snap, fmt.Errorf("history %s: %w", t.TurnID, err)
		}
		for _, m := range state.CleanMessages(t.Messages) {
			if m.Role == schema.User {
				m.Content = stripTurnContext(m.Content)
			}
			history = append(history, m)
		}
		snap.HistoryCursors = append(snap.HistoryCursors, t.Cursor)
		last = t.Cursor
	}
	assembled, err := Assemble(ctx, BuildInput{Input: in, Binding: binding, View: view}, providers, history, schema.UserMessage(TextBlock("student-input", binding.Input.Content)))
	if err != nil {
		return snap, err
	}
	snap.BlockHashes["preamble"] = state.Hash(assembled.Preamble)
	snap.BlockHashes["memory"] = state.Hash(assembled.Memory)
	snap.Preamble = assembled.Preamble
	snap.Memory = assembled.Memory
	ms := append([]*schema.Message{schema.SystemMessage(system)}, assembled.Messages...)
	snap.CanonicalInput = state.CleanMessages(ms[len(ms)-1:])[0]
	snap.BlockHashes["recent-dialogue"] = state.Hash(history)
	snap.BlockHashes["student-input"] = state.Hash(snap.CanonicalInput.Content)
	snap.Phase = "assembled"
	snap.Messages = ms
	n, err := b.Counter.Count(ms, nil)
	if err != nil {
		return snap, err
	}
	snap.EstimatedTokens = n
	snap.Hash = state.Hash(ms)
	// Over-budget snapshots are still returned for an auditable rejection.
	if n > spec.History.InputLimit() {
		return snap, state.ErrBudget
	}
	return snap, nil
}

// TextBlock escapes data delimiters. Tags are structural hints, not authorization.
func TextBlock(name, text string) string {
	escaped := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(text)
	return "<" + name + ">\n" + escaped + "\n</" + name + ">"
}
func JSONBlock(name string, value any) (string, error) {
	b, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return "<" + name + ">\n" + string(b) + "\n</" + name + ">", nil
}
func objectJSON(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return json.RawMessage(`{}`), nil
	}
	if !json.Valid(raw) {
		return nil, errors.New("runtime: context data must be a JSON object")
	}
	var value map[string]json.RawMessage
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		return nil, errors.New("runtime: context data must be a JSON object")
	}
	// encoding/json preserves JSON numbers and escapes literal HTML/XML delimiters.
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		return nil, err
	}
	return json.RawMessage(compact.Bytes()), nil
}
func (b Builder) catalog(refs []string) ([]state.SkillDescriptor, error) {
	byName := map[string]state.SkillDescriptor{}
	for _, s := range b.Skills {
		if s.Name == "" {
			return nil, errors.New("runtime: empty skill name")
		}
		if _, ok := byName[s.Name]; ok {
			return nil, errors.New("runtime: duplicate skill catalog entry")
		}
		byName[s.Name] = s
	}
	out := []state.SkillDescriptor{}
	seen := map[string]bool{}
	for _, name := range refs {
		if seen[name] {
			return nil, errors.New("runtime: duplicate skill reference")
		}
		seen[name] = true
		s, ok := byName[name]
		if !ok {
			if len(b.Skills) > 0 {
				return nil, fmt.Errorf("runtime: missing skill catalog entry %s", name)
			}
			s = state.SkillDescriptor{Name: name}
		}
		out = append(out, s)
	}
	if len(byName) > 0 && len(byName) != len(out) {
		return nil, errors.New("runtime: skill catalog exceeds authorized references")
	}
	return out, nil
}
func historySummary(summary state.Summary) (any, error) {
	if summary.Content == "" && len(summary.Structured) == 0 {
		return map[string]any{"available": false}, nil
	}
	var content any
	if len(summary.Structured) > 0 {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(summary.Structured, &obj); err != nil || obj == nil {
			return nil, errors.New("runtime: summary must be a JSON object")
		}
		content = summary.Structured
	} else {
		content = map[string]string{"overview": summary.Content}
	}
	return struct {
		Available     bool   `json:"available"`
		Revision      int64  `json:"revision"`
		ThroughCursor int64  `json:"through_cursor"`
		SourceHash    string `json:"source_hash"`
		Content       any    `json:"content"`
	}{true, summary.Revision, summary.ThroughCursor, summary.SourceHash, content}, nil
}

// stripTurnContext drops server-built context blocks from a historical user
// message. Blocks are re-issued every turn, so replaying them as history only
// repeats stale task modes — a tutoring conversation would otherwise keep
// carrying the SOLVE block of the turn that produced the question.
func stripTurnContext(content string) string {
	const (
		prefix = "<current-problem>"
		keep   = "<student-input>"
	)
	if !strings.Contains(content, prefix) {
		return content
	}
	if i := strings.Index(content, keep); i >= 0 {
		return strings.TrimSpace(content[i:])
	}
	return content
}
