// Package middleware contains business-neutral lifecycle contracts.
package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/cloudwego/eino/schema"
	"log/slog"
	"time"
)

type Mode int

const (
	Default Mode = iota
	FailTurn
	DegradeAndContinue
	Compensate
)

type HookPolicy struct {
	Mode    Mode
	Timeout time.Duration
}
type ToolDecision int

const (
	Allow ToolDecision = iota
	Deny
	Confirm
)

type HookInput struct {
	// ToolChoice is a per-model-call constraint set by trusted BeforeModel hooks.
	ToolChoice                              *schema.ToolChoice
	ToolAllowlist                           []string
	Context                                 context.Context
	Messages                                []*schema.Message
	ToolName, ToolCallID, Arguments, Result string
	Err                                     error
}
type Callback func(context.Context, *HookInput) error

// ErrToolRequired requests one constrained regeneration of an invalid plain reply.
// It never authorizes or executes a tool on the model's behalf.
var ErrToolRequired = errors.New("adk: model output requires a tool call")

type LifecycleHooks struct {
	ValidateModelOutput                                                         Callback
	Name                                                                        string
	Policy                                                                      HookPolicy
	BeforeAssemble, BeforeTurn, AfterTurn, BeforeAgent, BeforeModel, AfterModel Callback
	BeforeModelRewriteState, AfterModelRewriteState                             Callback
	BeforeTool                                                                  func(context.Context, *HookInput) (ToolDecision, error)
	AfterTool, OnError, Compensation                                            Callback
}

// Invoke runs serially in registration order; callers register built-ins first.
func Invoke(ctx context.Context, h LifecycleHooks, fn Callback, in *HookInput) error {
	if fn == nil {
		return nil
	}
	c := ctx
	cancel := func() {}
	if h.Policy.Timeout > 0 {
		c, cancel = context.WithTimeout(ctx, h.Policy.Timeout)
	}
	defer cancel()
	backup := *in
	backup.Messages = nil
	raw, cloneErr := json.Marshal(in.Messages)
	if cloneErr != nil {
		return cloneErr
	}
	if cloneErr = json.Unmarshal(raw, &backup.Messages); cloneErr != nil {
		return cloneErr
	}
	err := fn(c, in)
	if err == nil {
		err = c.Err()
	}
	if err != nil && h.Policy.Mode == DegradeAndContinue {
		*in = backup
		slog.WarnContext(ctx, "lifecycle hook degraded", "hook", h.Name)
		return nil
	}
	if err == nil {
		return nil
	}
	if h.Policy.Mode == Compensate && h.Compensation != nil {
		in.Err = err
		cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer stop()
		if e := h.Compensation(cleanup, in); e != nil {
			return errors.Join(err, e)
		}
	}
	return err
}
