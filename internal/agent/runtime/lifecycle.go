package runtime

import (
	"context"
	"github.com/musi-ai/education/internal/agent/adk/middleware"
)

type lifecycleKey struct{}

func lifecycle(ctx context.Context) ([]middleware.LifecycleHooks, bool) {
	h, ok := ctx.Value(lifecycleKey{}).([]middleware.LifecycleHooks)
	return h, ok
}
