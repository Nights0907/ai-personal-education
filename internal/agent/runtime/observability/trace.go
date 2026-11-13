package observability

import (
	"context"
	"github.com/musi-ai/education/internal/agent/adk/middleware"
	"log/slog"
)

func Hooks(_ *slog.Logger) middleware.LifecycleHooks { return ModelHooks(NewTracer(nil)) }
func ModelHooks(t Tracer) middleware.LifecycleHooks {
	var active Span
	var parent context.Context
	tools := map[string]Span{}
	return middleware.LifecycleHooks{Name: "runtime.trace", Policy: middleware.HookPolicy{Mode: middleware.DegradeAndContinue}, BeforeModel: func(ctx context.Context, in *middleware.HookInput) error {
		parent = ctx
		in.Context, active = t.Start(ctx, "runtime.model", nil)
		return nil
	}, AfterModel: func(_ context.Context, in *middleware.HookInput) error {
		if active != nil {
			active.End(nil)
			active = nil
		}
		in.Context = parent
		return nil
	}, BeforeTool: func(ctx context.Context, in *middleware.HookInput) (middleware.ToolDecision, error) {
		c, s := t.Start(ctx, "runtime.tool", map[string]string{"tool": in.ToolName, "call_id": in.ToolCallID})
		tools[in.ToolCallID] = s
		in.Context = c
		return middleware.Allow, nil
	}, AfterTool: func(_ context.Context, in *middleware.HookInput) error {
		if s := tools[in.ToolCallID]; s != nil {
			s.End(nil)
			delete(tools, in.ToolCallID)
		}
		return nil
	}, OnError: func(_ context.Context, in *middleware.HookInput) error {
		if active != nil {
			active.End(in.Err)
			active = nil
		}
		for id, s := range tools {
			s.End(in.Err)
			delete(tools, id)
		}
		return nil
	}}
}
