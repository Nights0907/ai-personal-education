package runtime

import (
	"context"
	"github.com/cloudwego/eino/schema"
	"github.com/musi-ai/education/internal/agent/runtime/state"
)

type invocationKey struct{}

type toolInvocationKey struct{}
type recoveryPrefixKey struct{}
type ToolInvocation struct{ CallID, Name, Arguments, ArgumentsHash string }

// CurrentTool is set by the trusted Eino middleware, never by model arguments.
func CurrentTool(ctx context.Context) (ToolInvocation, bool) {
	v, ok := ctx.Value(toolInvocationKey{}).(ToolInvocation)
	return v, ok
}

// RecoveryPrefix contains this turn's original input and generated protocol,
// ending at the current tool call. A finalizer freezes it with its receipt.
func RecoveryPrefix(ctx context.Context) ([]*schema.Message, bool) {
	v, ok := ctx.Value(recoveryPrefixKey{}).([]*schema.Message)
	return state.CleanMessages(v), ok
}

// Invocation returns server-bound coordinates. Business handlers must authorize
// these coordinates, never identity fields supplied in generated tool arguments.
func Invocation(ctx context.Context) (state.Input, bool) {
	v, ok := ctx.Value(invocationKey{}).(state.Input)
	return v, ok
}
