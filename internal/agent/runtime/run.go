package runtime

import (
	"context"
	"errors"
	"fmt"
	"github.com/cloudwego/eino/schema"
	"github.com/musi-ai/education/internal/agent/adk/middleware"
	ctxbuild "github.com/musi-ai/education/internal/agent/runtime/context"
	"github.com/musi-ai/education/internal/agent/runtime/observability"
	"github.com/musi-ai/education/internal/agent/runtime/prompt"
	"github.com/musi-ai/education/internal/agent/runtime/state"
	"github.com/musi-ai/education/internal/agent/runtime/storage"
	"time"
)

func (r *Runtime) RunTurn(ctx context.Context, in state.Input) (result state.Result, retErr error) {
	result = state.Result{RunID: in.Identity.RunID, Status: "failed", HistoryStatus: "unsealed"}
	spec, ok := r.cfg.Specs[in.ConfigRef]
	if !ok {
		return result, errors.New("runtime: unknown config ref")
	}
	ctx = context.WithValue(ctx, invocationKey{}, in)
	ctx, cancel := context.WithTimeout(ctx, spec.Timeout)
	defer cancel()
	var turnSpan observability.Span
	var traceErr error
	if !r.cfg.DisableTrace {
		ctx, turnSpan = r.cfg.Tracer.Start(ctx, "runtime.turn", map[string]string{"agent": spec.Name, "turn_id": in.Identity.TurnID, "run_id": in.Identity.RunID})
		defer func() { turnSpan.End(errors.Join(retErr, traceErr)) }()
	}
	inject := storage.NewInjectHook(r.cfg.Store, storage.InjectOptions{Input: in, AgentName: spec.Name, Producer: r.cfg.EventProducer, Policy: r.cfg.CompletionPolicy, Result: &result, RunError: &retErr})
	storageHooks := inject.Hooks()
	hooks := []middleware.LifecycleHooks{{Name: "runtime.guard", Policy: middleware.HookPolicy{Mode: middleware.FailTurn}, BeforeTurn: func(c context.Context, _ *middleware.HookInput) error { return r.cfg.Guard.Check(c, in) }, BeforeAssemble: func(c context.Context, _ *middleware.HookInput) error { return r.cfg.Guard.Check(c, in) }, BeforeAgent: func(c context.Context, _ *middleware.HookInput) error { return r.cfg.Guard.Check(c, in) }, BeforeModelRewriteState: func(c context.Context, _ *middleware.HookInput) error { return r.cfg.Guard.Check(c, in) }, BeforeModel: func(c context.Context, _ *middleware.HookInput) error { return r.cfg.Guard.Check(c, in) }, BeforeTool: func(c context.Context, _ *middleware.HookInput) (middleware.ToolDecision, error) {
		return middleware.Allow, r.cfg.Guard.Check(c, in)
	}}, storageHooks}
	if !r.cfg.DisableTrace {
		hooks = append(hooks, observability.ModelHooks(r.cfg.Tracer))
	}
	hooks = append(hooks, r.cfg.BusinessHooks...)
	chain := middleware.NewAdapter(hooks)
	ctx = context.WithValue(ctx, lifecycleKey{}, hooks)
	var canonical []*schema.Message
	var raw *schema.Message
	var current *schema.Message
	var generated []*schema.Message
	skipped := false
	defer func() {
		if skipped {
			return
		}
		cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer stop()
		canonical = state.CleanMessages(generated)
		if current != nil {
			canonical = append(state.CleanMessages([]*schema.Message{current}), canonical...)
		}
		if result.ErrorCode == "" && retErr != nil {
			result.ErrorCode = errorCode(retErr)
		}
		// Storage is first in this same chain; business AfterTurn sees sealed data.
		input := &middleware.HookInput{Messages: canonical, Err: retErr, Context: cleanup}
		originalErr := retErr
		afterErr := chain.Call(cleanup, func(h middleware.LifecycleHooks) middleware.Callback { return h.AfterTurn }, input)
		if afterErr != nil && result.Status == "succeeded" {
			result.WarningCode = "POST_TURN_HOOK_FAILED"
		}
		combined := errors.Join(retErr, afterErr)
		observedErr := errors.Join(originalErr, combined)
		traceErr = observedErr
		if observedErr != nil {
			_ = chain.Call(cleanup, func(h middleware.LifecycleHooks) middleware.Callback { return h.OnError }, &middleware.HookInput{Messages: canonical, Err: observedErr, Context: cleanup})
		}
		if result.Status != "succeeded" {
			retErr = combined
		}
		emitLive(cleanup, TurnEvent{Type: "turn_result", Result: &result, ErrorCode: result.ErrorCode})
	}()
	if err := in.Validate(); err != nil {
		return result, err
	}
	if err := r.cfg.Guard.Check(ctx, in); err != nil {
		return result, err
	}
	committed, err := r.cfg.CompletionPolicy.Preflight(ctx, in)
	if err != nil {
		return result, err
	}
	if committed != nil {
		if committed.ResultID == "" || committed.TurnID != in.Identity.TurnID {
			return result, state.ErrConflict
		}
		result.Status = "succeeded"
		result.CompletionID = committed.ResultID
		if committed.Kind != "engine" {
			result.CommittedResult = committed.ResultID
		}
		result.HistoryStatus = "check_repair"
		skipped = true
		if prior, e := r.cfg.Store.GetTurn(ctx, in.Identity.Scope, in.Identity.TurnID); e == nil {
			for _, m := range prior.Messages {
				if m.Role == schema.Assistant && len(m.ToolCalls) == 0 && m.Content != "" {
					emitLive(ctx, TurnEvent{Type: "message", Message: m})
				}
			}
		}
		emitLive(ctx, TurnEvent{Type: "turn_result", Result: &result})
		return result, nil
	}
	if spec.InstructionFile != "" {
		spec.Instruction, err = prompt.Read(spec.InstructionFile)
		if err != nil {
			result.ErrorCode = "PROMPT_UNAVAILABLE"
			return result, err
		}
		spec.Instruction = staticPrompt(spec.Instruction, r.cfg.staticSuffix)
	}
	binding, err := r.cfg.Provider.Provide(ctx, in)
	if err != nil {
		return result, err
	}
	if binding.Input == nil {
		return result, state.ErrProtocol
	}
	raw = state.CleanMessages([]*schema.Message{binding.Input})[0]
	initial := &middleware.HookInput{Messages: state.CleanMessages([]*schema.Message{raw}), Context: ctx}
	if err = chain.Call(ctx, func(h middleware.LifecycleHooks) middleware.Callback { return h.BeforeTurn }, initial); err != nil {
		return result, err
	}
	if err = inject.PersistUser(ctx, raw); err != nil {
		return result, err
	}
	if err = chain.Call(ctx, func(h middleware.LifecycleHooks) middleware.Callback { return h.BeforeAssemble }, initial); err != nil {
		return result, err
	}
	if len(initial.Messages) != 1 || initial.Messages[0].Role != schema.User {
		return result, state.ErrProtocol
	}
	binding.Input = initial.Messages[0]
	current = binding.Input
	var view state.View
	if reader, ok := r.cfg.Store.(storage.HistoryReader); ok {
		view, err = reader.ContextView(ctx, in.Identity.Scope)
	} else {
		view, err = r.cfg.Store.View(ctx, in.Identity.Scope)
	}
	if err != nil {
		return result, err
	}
	var catalog []state.SkillDescriptor
	if provider, ok := r.cfg.Engine.(SkillCatalogProvider); ok {
		catalog, err = provider.SkillCatalog(ctx, spec)
		if err != nil {
			return result, err
		}
	}
	snap, buildErr := (ctxbuild.Builder{Counter: r.cfg.Counter, Skills: catalog, Providers: r.cfg.TurnProviders}).Build(ctx, in, spec, binding, view)
	snap.OriginalInput = raw
	if snap.Hash != "" {
		if err = inject.Capture(ctx, snap); err != nil {
			return result, err
		}
	}
	for attempt := 0; errors.Is(buildErr, state.ErrBudget) && r.cfg.OnContextBudgetExceeded != nil && attempt < 3; attempt++ {
		if err = r.cfg.OnContextBudgetExceeded(ctx, in.Identity.Scope, spec.History); err != nil {
			return result, errors.Join(buildErr, err)
		}
		if reader, ok := r.cfg.Store.(storage.HistoryReader); ok {
			view, err = reader.ContextView(ctx, in.Identity.Scope)
		} else {
			view, err = r.cfg.Store.View(ctx, in.Identity.Scope)
		}
		if err != nil {
			return result, err
		}
		snap, buildErr = (ctxbuild.Builder{Counter: r.cfg.Counter, Skills: catalog, Providers: r.cfg.TurnProviders}).Build(ctx, in, spec, binding, view)
		snap.OriginalInput = raw
		if snap.Hash != "" {
			if err = inject.Capture(ctx, snap); err != nil {
				return result, err
			}
		}
	}
	if buildErr != nil {
		return result, buildErr
	}
	current = snap.CanonicalInput
	ctx = context.WithValue(ctx, PreambleKey, snap.Preamble)
	ctx = context.WithValue(ctx, MemoryKey, snap.Memory)
	observe := func(c context.Context, m *schema.Message) error {
		if e := inject.Observe(c, m); e != nil {
			return e
		}
		if m.Role == schema.Assistant {
			emitLive(c, TurnEvent{Type: "message", Message: state.CleanMessages([]*schema.Message{m})[0]})
		}
		return nil
	}
	generated, err = r.cfg.Engine.Execute(ctx, in, spec, snap, observe, inject.Capture)
	return result, err
}
func errorCode(err error) string {
	switch {
	case errors.Is(err, state.ErrBudget):
		return "CONTEXT_BUDGET_EXCEEDED"
	case errors.Is(err, state.ErrConflict):
		return "STALE_BINDING_OR_LEASE"
	case errors.Is(err, state.ErrIncomplete):
		return "AGENT_INCOMPLETE"
	case errors.Is(err, context.DeadlineExceeded):
		return "DEADLINE_EXCEEDED"
	case errors.Is(err, context.Canceled):
		return "CANCELLED"
	default:
		return "RUN_FAILED"
	}
}

// Repair seals an already committed turn without executing its agent or tools.
// The caller is the durable application repair job; it supplies its frozen canonical
// messages and original input/lease. A superseded run is deliberately rejected;
// a committed run may be sealed after its original execution lease expires.
func (r *Runtime) Repair(ctx context.Context, in state.Input, messages []*schema.Message) error {
	receipt, err := r.cfg.CompletionPolicy.Preflight(ctx, in)
	if err != nil {
		return err
	}
	if receipt == nil {
		return state.ErrIncomplete
	}
	messages, err = state.RestoreTerminal(messages, receipt)
	if err != nil {
		return err
	}
	if err = state.ValidateProtocol(messages); err != nil {
		return fmt.Errorf("repair needs complete source: %w", err)
	}
	return r.cfg.Store.Finish(ctx, in, "succeeded", "", receipt, messages)
}

// RepairFromStore is for a durable reconciliation job after process restart.
// It never re-enters Eino and requires the original run/lease coordinates.
func (r *Runtime) RepairFromStore(ctx context.Context, in state.Input) error {
	source, ok := r.cfg.Store.(storage.RecoveryReader)
	if !ok {
		return errors.New("runtime: store does not support durable recovery sources")
	}
	ms, err := source.LoadRecovery(ctx, in)
	if errors.Is(err, state.ErrNotFound) {
		receipt, resolveErr := r.cfg.CompletionPolicy.Preflight(ctx, in)
		if resolveErr != nil {
			return resolveErr
		}
		if receipt != nil && receipt.TurnID == in.Identity.TurnID && receipt.RunID == in.Identity.RunID && len(receipt.FrozenPrefix) > 0 {
			ms = state.CleanMessages(receipt.FrozenPrefix)
			err = nil
		}
	}
	if err != nil {
		return err
	}
	return r.Repair(ctx, in, ms)
}
