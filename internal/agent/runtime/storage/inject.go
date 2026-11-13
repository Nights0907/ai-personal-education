package storage

import (
	"context"
	"errors"
	"github.com/cloudwego/eino/schema"
	"github.com/musi-ai/education/internal/agent/adk/middleware"
	"github.com/musi-ai/education/internal/agent/completion"
	"github.com/musi-ai/education/internal/agent/runtime/event"
	"github.com/musi-ai/education/internal/agent/runtime/state"
	"log/slog"
	"time"
)

type CompletionResolver interface {
	FindCommittedResult(context.Context, state.Input) (*state.Receipt, error)
}

type InjectOptions struct {
	Result     *state.Result
	RunError   *error
	Input      state.Input
	AgentName  string
	Producer   event.Producer
	Completion CompletionResolver
	Policy     completion.Policy
}
type InjectHook struct {
	Store     Storage
	Opts      InjectOptions
	Snapshot  state.Snapshot
	StartedAt time.Time
	Began     bool
	seq       int
}

func NewInjectHook(s Storage, opts InjectOptions) *InjectHook {
	return &InjectHook{Store: s, Opts: opts}
}
func (h *InjectHook) Hooks() middleware.LifecycleHooks {
	return middleware.LifecycleHooks{Name: "runtime.storage", Policy: middleware.HookPolicy{Mode: middleware.FailTurn},
		BeforeTurn: func(ctx context.Context, input *middleware.HookInput) error {
			h.StartedAt = time.Now()
			if e := h.Store.Begin(ctx, h.Opts.Input); e != nil {
				return e
			}
			h.Began = true
			if len(input.Messages) > 0 {
				return h.PersistUser(ctx, input.Messages[0])
			}
			return nil
		},
		AfterTurn: func(ctx context.Context, in *middleware.HookInput) error {
			result := h.Opts.Result
			if result == nil {
				return errors.New("storage: result binding required")
			}
			runErr := h.Opts.RunError
			if runErr == nil {
				return errors.New("storage: run error binding required")
			}
			var input *schema.Message
			var generated []*schema.Message
			if len(in.Messages) > 0 {
				input = in.Messages[0]
				generated = in.Messages[1:]
			}
			return h.Finalize(ctx, input, generated, result, runErr)
		}}
}
func (h *InjectHook) PersistUser(ctx context.Context, m *schema.Message) error {
	return h.Store.PersistUser(ctx, h.Opts.Input, m)
}
func (h *InjectHook) Capture(ctx context.Context, s state.Snapshot) error {
	if s.Phase == "assembled" {
		h.Snapshot = s
	}
	if s.Usage.InputTokens > 0 {
		h.Snapshot.Usage = s.Usage
	}
	return h.Store.SaveSnapshot(ctx, h.Opts.Input, s)
}
func (h *InjectHook) Observe(ctx context.Context, m *schema.Message) error {
	h.seq++
	return h.Store.Append(ctx, h.Opts.Input, h.seq, m)
}

// Finalize reconciles the canonical turn once, then seals it. A durable business
// receipt wins over late model errors; it must never cause tool re-execution.
func (h *InjectHook) Finalize(ctx context.Context, input *schema.Message, generated []*schema.Message, result *state.Result, runErr *error) error {
	if !h.Began {
		return nil
	}
	canonical := state.CleanMessages(generated)
	if input != nil {
		canonical = append(state.CleanMessages([]*schema.Message{input}), canonical...)
	}
	policy := h.Opts.Policy
	if policy == nil && h.Opts.Completion != nil {
		policy = completion.Receipts{Resolver: h.Opts.Completion}
	}
	receipt, e := policy.Resolve(ctx, h.Opts.Input, canonical, *runErr)
	if errors.Is(e, completion.ErrUnknown) {
		result.Status = "unknown"
		result.HistoryStatus = "pending_reconciliation"
		result.ErrorCode = "COMPLETION_UNKNOWN"
		*runErr = errors.Join(*runErr, e)
		return e
	}
	if e != nil {
		*runErr = errors.Join(*runErr, e)
	}
	if receipt != nil && receipt.ResultID != "" && receipt.TurnID == h.Opts.Input.Identity.TurnID && receipt.RunID == h.Opts.Input.Identity.RunID {
		result.Status = "succeeded"
		result.CompletionID = receipt.ResultID
		if receipt.Kind != "engine" {
			result.CommittedResult = receipt.ResultID
		}
		result.ErrorCode = ""
		*runErr = nil
		canonical, e = state.RestoreTerminal(canonical, receipt)
		if e != nil {
			result.HistoryStatus = "pending_repair"
			if receipt.Kind == "engine" {
				result.Status = "failed"
				result.HistoryStatus = "unsealed"
				result.ErrorCode = "STORE_SEAL_FAILED"
				*runErr = errors.Join(*runErr, e)
				_ = h.Store.Finish(ctx, h.Opts.Input, "failed", result.ErrorCode, nil, canonical)
			}
			return e
		}
		if e = h.Store.Finish(ctx, h.Opts.Input, "succeeded", "", receipt, canonical); e != nil {
			result.HistoryStatus = "pending_repair"
			if receipt.Kind == "engine" {
				result.Status = "failed"
				result.HistoryStatus = "unsealed"
				result.ErrorCode = "STORE_SEAL_FAILED"
				*runErr = errors.Join(*runErr, e)
				_ = h.Store.Finish(ctx, h.Opts.Input, "failed", result.ErrorCode, nil, canonical)
			}
			return e
		}
		result.HistoryStatus = "sealed"
		if h.Opts.Producer != nil {
			if e = h.emit(ctx, canonical); e != nil {
				slog.WarnContext(ctx, "turn event delivery failed; sealed history retained", "turn_id", h.Opts.Input.Identity.TurnID)
			}
		}
		return nil
	}
	if *runErr == nil {
		*runErr = state.ErrIncomplete
		result.ErrorCode = "AGENT_INCOMPLETE"
	}
	if result.ErrorCode == "" {
		result.ErrorCode = "RUN_FAILED"
		if errors.Is(*runErr, state.ErrIncomplete) {
			result.ErrorCode = "AGENT_INCOMPLETE"
		}
	}
	if e = h.Store.Finish(ctx, h.Opts.Input, "failed", result.ErrorCode, nil, canonical); e != nil {
		*runErr = errors.Join(*runErr, e)
	}
	if e == nil {
		h.emitFailed(ctx, result.ErrorCode)
	}
	return e
}
func (h *InjectHook) emitFailed(ctx context.Context, code string) {
	if h.Opts.Producer == nil {
		return
	}
	payload := &event.TurnCompleted{Identity: h.identity(""), Status: "failed", StartedAt: h.StartedAt, EndedAt: time.Now(), Error: code, Policy: h.Snapshot.Policy}
	if err := h.Opts.Producer.Emit(ctx, event.Event{Type: event.TypeTurnCompleted, Payload: payload}); err != nil {
		slog.WarnContext(ctx, "failed turn event delivery failed")
	}
}
func (h *InjectHook) identity(branch string) event.Identity {
	scope := h.Opts.Input.Identity.Scope
	return event.Identity{AgentName: h.Opts.AgentName, ConversationID: scope.ConversationID, TurnID: h.Opts.Input.Identity.TurnID, RunID: h.Opts.Input.Identity.RunID, Tenant: scope.Namespace, SubjectID: scope.SubjectID, BranchID: branch}
}
func (h *InjectHook) emit(ctx context.Context, ms []*schema.Message) error {
	scope := h.Opts.Input.Identity.Scope
	// Read the sealed view so event coordinates are allocated by storage, not the
	// loop. Do not emit an event with made-up branch positions.
	turn, e := h.Store.GetTurn(ctx, scope, h.Opts.Input.Identity.TurnID)
	if e != nil {
		return e
	}
	items := event.ItemsForTurn(scope, turn)
	payload := &event.TurnCompleted{Identity: event.Identity{AgentName: h.Opts.AgentName, ConversationID: scope.ConversationID, TurnID: h.Opts.Input.Identity.TurnID, RunID: h.Opts.Input.Identity.RunID, Tenant: scope.Namespace, SubjectID: scope.SubjectID, BranchID: turn.End.BranchID}, Status: "completed", Policy: h.Snapshot.Policy, StartedAt: h.StartedAt, EndedAt: time.Now(), TailSeq: turn.End.Position, ItemCount: len(items), Items: items, Preamble: h.Snapshot.Preamble, Memory: h.Snapshot.Memory}
	return h.Opts.Producer.Emit(ctx, event.Event{Type: event.TypeTurnCompleted, Payload: payload})
}
