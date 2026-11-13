// Package memory manages conversation memory; it never owns student profiles.
package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	runtimeevent "github.com/musi-ai/education/internal/agent/runtime/event"
	teachingsummary "github.com/musi-ai/education/internal/modules/teaching/summary"
	"time"

	"github.com/cloudwego/eino/schema"
	ctxbuild "github.com/musi-ai/education/internal/agent/runtime/context"
	"github.com/musi-ai/education/internal/agent/runtime/state"
	"github.com/musi-ai/education/internal/agent/runtime/storage"
)

type SummarySource struct {
	Previous state.Summary
	Turns    []state.Turn
}
type Summarizer interface {
	Summarize(context.Context, SummarySource, int) (string, error)
}
type StructuredSummarizer interface {
	SummarizeStructured(context.Context, SummarySource, int) (teachingsummary.DialogueSummary, error)
}
type MeasuredStructuredSummarizer interface {
	SummarizeMeasured(context.Context, SummarySource, int) (teachingsummary.DialogueSummary, state.ContextUsage, error)
}
type Manager struct {
	ModelRef   string
	Store      storage.Store
	Counter    ctxbuild.Counter
	Summarizer Summarizer
}

// DispatchOne repairs the crash window between SealTurn and summary enqueue.
// Concurrent dispatchers may read the same event: enqueue and acknowledge are
// idempotent, and acknowledgement happens only after successful scheduling.
func (m Manager) DispatchOne(ctx context.Context) (bool, error) {
	event, e := m.Store.NextSealed(ctx)
	if errors.Is(e, state.ErrNotFound) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	turn, loadErr := m.Store.GetTurn(ctx, event.Scope, event.TurnID)
	if loadErr != nil {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		return true, errors.Join(loadErr, m.Store.DeferSealed(cleanup, event, loadErr.Error()))
	}
	payload := &runtimeevent.TurnCompleted{Identity: runtimeevent.Identity{Tenant: event.Scope.Namespace, SubjectID: event.Scope.SubjectID, ConversationID: event.Scope.ConversationID, TurnID: event.TurnID, BranchID: turn.End.BranchID}, Policy: event.Policy, Status: "completed", Items: runtimeevent.ItemsForTurn(event.Scope, turn), TailSeq: turn.End.Position, ItemCount: len(turn.Messages)}
	if e = m.Consume(ctx, runtimeevent.Event{Type: runtimeevent.TypeTurnCompleted, Payload: payload}); e != nil {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		return true, errors.Join(e, m.Store.DeferSealed(cleanup, event, e.Error()))
	}
	return true, m.Store.AcknowledgeSealed(ctx, event)
}

// Schedule retains the newest complete turns and freezes a range, not generated text.
// Jobs are deduplicated by scope/base/cursor/config. No model call runs on this path.
func (m Manager) Schedule(ctx context.Context, scope state.Scope, p state.Policy, force bool) (bool, error) {
	if err := p.Validate(); err != nil {
		return false, err
	}
	if m.Store == nil || m.Counter == nil {
		return false, errors.New("runtime: memory store and counter required")
	}
	v, err := m.loadView(ctx, scope)
	if err != nil {
		return false, err
	}
	var rest []state.Turn
	var ms []*schema.Message
	for _, t := range v.Turns {
		if t.Cursor > v.Summary.ThroughCursor {
			rest = append(rest, t)
			ms = append(ms, t.Messages...)
		}
	}
	if len(rest) <= p.KeepRecentTurns {
		return false, nil
	}
	n, err := m.Counter.Count(ms, nil)
	if err != nil {
		return false, err
	}
	if v.Usage.InputTokens > 0 && v.Usage.SummaryRevision == v.Summary.Revision {
		n = v.Usage.InputTokens
	}
	if !force && n < p.TriggerTokens() {
		return false, nil
	}
	// Bound each compaction batch as well as the final summary. A long backlog
	// is reduced incrementally, never sent as an unbounded model request.
	source := SummarySource{Previous: v.Summary}
	through := int64(0)
	for _, turn := range rest[:len(rest)-p.KeepRecentTurns] {
		source.Turns = append(source.Turns, turn)
		size, e := m.sourceTokens(source)
		if e != nil {
			return false, e
		}
		if size+p.MaxSummaryTokens+1024 > p.MaxInputTokens {
			source.Turns = source.Turns[:len(source.Turns)-1]
			break
		}
		through = turn.Cursor
	}
	if through == 0 {
		return false, fmt.Errorf("%w: oldest complete turn cannot fit summary model budget", state.ErrBudget)
	}
	j := state.SummaryJob{Scope: scope, ModelRef: m.ModelRef, ReadToken: v.Token, BaseRevision: v.Summary.Revision, ThroughCursor: through, Policy: p, State: "pending"}
	j.ID = state.Hash(struct {
		Scope         state.Scope
		Base, Through int64
		Policy        string
		Branch        string
		Epoch         int64
		Model         string
	}{scope, j.BaseRevision, through, p.Version, v.Token.BranchID, v.Token.HistoryEpoch, m.ModelRef})
	return true, m.Store.EnqueueSummary(ctx, j)
}
func (m Manager) ProcessOne(ctx context.Context, worker string) (bool, error) {
	if m.Store == nil || m.Counter == nil {
		return false, errors.New("runtime: memory store and counter required")
	}
	j, err := m.Store.ClaimSummary(ctx, worker, 2*time.Minute)
	if errors.Is(err, state.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	// Cancellation of the model request must not prevent releasing the job lease.
	fail := func(e error) (bool, error) {
		c, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		delay := time.Duration(min(300, 1<<min(j.Attempts, 8))) * time.Second
		saveErr := m.Store.RetrySummary(c, j, e.Error(), time.Now().Add(delay))
		return true, errors.Join(e, saveErr)
	}
	v, err := m.loadView(ctx, j.Scope)
	if err != nil {
		return fail(err)
	}
	if v.Summary.Revision != j.BaseRevision || (j.ReadToken.BranchID != "" && (v.Token.BranchID != j.ReadToken.BranchID || v.Token.HistoryEpoch != j.ReadToken.HistoryEpoch)) {
		return true, m.Store.PublishSummary(ctx, j, state.Summary{})
	}
	source := SummarySource{Previous: v.Summary}
	for _, t := range v.Turns {
		if t.Cursor > v.Summary.ThroughCursor && t.Cursor <= j.ThroughCursor {
			if err := state.ValidateProtocol(t.Messages); err != nil {
				return fail(err)
			}
			source.Turns = append(source.Turns, t)
		}
	}
	if len(source.Turns) == 0 || source.Turns[len(source.Turns)-1].Cursor != j.ThroughCursor {
		return fail(state.ErrConflict)
	}
	if m.Summarizer == nil {
		return fail(errors.New("runtime: summary model not configured"))
	}
	if j.ModelRef != m.ModelRef {
		return fail(errors.New("runtime: summary model configuration changed"))
	}
	sourceTokens, err := m.sourceTokens(source)
	if err != nil {
		return fail(err)
	}
	if sourceTokens+j.Policy.MaxSummaryTokens+1024 > j.Policy.MaxInputTokens {
		return fail(state.ErrBudget)
	}
	var structured teachingsummary.DialogueSummary
	var generationUsage state.ContextUsage
	if summarizer, ok := m.Summarizer.(MeasuredStructuredSummarizer); ok {
		structured, generationUsage, err = summarizer.SummarizeMeasured(ctx, source, j.Policy.MaxSummaryTokens)
	} else if summarizer, ok := m.Summarizer.(StructuredSummarizer); ok {
		structured, err = summarizer.SummarizeStructured(ctx, source, j.Policy.MaxSummaryTokens)
	} else {
		var text string
		text, err = m.Summarizer.Summarize(ctx, source, j.Policy.MaxSummaryTokens)
		structured = teachingsummary.DialogueSummary{SchemaVersion: teachingsummary.DialogueSummaryVersion, Overview: text}
	}
	if err != nil {
		return fail(err)
	}
	structured = teachingsummary.NormalizeSummary(structured)
	if err = ValidateSummary(structured, source); err != nil {
		return fail(err)
	}
	encoded, err := json.Marshal(structured)
	if err != nil {
		return fail(err)
	}
	n, err := m.Counter.Count([]*schema.Message{schema.UserMessage(string(encoded))}, nil)
	if err != nil {
		return fail(err)
	}
	if generationUsage.Source == "provider" && generationUsage.CompletionTokens > 0 {
		n = generationUsage.CompletionTokens
	}
	if n > j.Policy.MaxSummaryTokens {
		return fail(fmt.Errorf("runtime: summary empty or exceeds token budget: %d", n))
	}
	s := state.Summary{GenerationUsage: generationUsage, Structured: teachingsummary.Encode(structured), ModelRef: m.ModelRef, Revision: j.BaseRevision + 1, ThroughCursor: j.ThroughCursor, Content: structured.Overview, SourceHash: state.Hash(source), PolicyVersion: j.Policy.Version}
	if err = m.Store.PublishSummary(ctx, j, s); err != nil {
		return fail(err)
	}
	return true, nil
}
func (m Manager) sourceTokens(source SummarySource) (int, error) {
	b, e := json.Marshal(source)
	if e != nil {
		return 0, e
	}
	return m.Counter.Count([]*schema.Message{schema.UserMessage(string(b))}, nil)
}

// Production stores read only the uncovered suffix, in complete-turn pages.
func (m Manager) loadView(ctx context.Context, scope state.Scope) (state.View, error) {
	if reader, ok := m.Store.(storage.HistoryReader); ok {
		return reader.ContextView(ctx, scope)
	}
	return m.Store.View(ctx, scope)
}
