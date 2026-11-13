package middleware

import (
	"context"
	"errors"
	"github.com/cloudwego/eino/schema"
	"testing"
	"time"
)

func TestFailedDegradedRewriteRollsBack(t *testing.T) {
	in := &HookInput{Messages: []*schema.Message{schema.AssistantMessage("verified", nil)}, Result: "original"}
	e := Invoke(context.Background(), LifecycleHooks{Policy: HookPolicy{Mode: DegradeAndContinue}}, func(_ context.Context, h *HookInput) error {
		h.Messages[0].Content = "broken"
		h.Result = "partial"
		return errors.New("failed review")
	}, in)
	if e != nil || in.Messages[0].Content != "verified" || in.Result != "original" {
		t.Fatal("failed rewrite leaked", in, e)
	}
}
func TestCompensationUsesLiveCleanupContext(t *testing.T) {
	called := false
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e := Invoke(ctx, LifecycleHooks{Policy: HookPolicy{Mode: Compensate, Timeout: time.Second}, Compensation: func(c context.Context, _ *HookInput) error { called = true; return c.Err() }}, func(c context.Context, _ *HookInput) error { return c.Err() }, &HookInput{})
	if !called || !errors.Is(e, context.Canceled) {
		t.Fatal("compensation did not run", called, e)
	}
}
