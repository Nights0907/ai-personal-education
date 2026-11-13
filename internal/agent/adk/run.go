package adk

import (
	"context"
	"errors"
	eino "github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/musi-ai/education/internal/agent/adk/middleware"
	"iter"
)

// Run returns a lazy event iterator. Consume it to exhaustion; stopping iteration
// cancels execution and reports cancellation to AfterTurn/OnError.
func (a *AgentLoop) Run(ctx context.Context, messages []*schema.Message) iter.Seq[*eino.AgentEvent] {
	return func(yield func(*eino.AgentEvent) bool) {
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		bridge := middleware.NewAdapter(a.cfg.Hooks)
		bridge.OnToolResult = a.cfg.OnToolResult
		terminal := make(map[string]bool, len(a.cfg.ReturnDirectly))
		for name, enabled := range a.cfg.ReturnDirectly {
			if name == "submit_solution" {
				bridge.StopOnCommittedSubmission = enabled
			} else {
				terminal[name] = enabled
			}
		}
		bridge.Messages = messages
		var runErr error
		stopped := false
		defer func() {
			in := &middleware.HookInput{Messages: bridge.Messages, Err: runErr}
			if runErr != nil && !a.cfg.ManagedTurn {
				_ = bridge.Call(ctx, func(h middleware.LifecycleHooks) middleware.Callback { return h.OnError }, in)
			}
			var e error
			if !a.cfg.ManagedTurn {
				e = bridge.Call(ctx, func(h middleware.LifecycleHooks) middleware.Callback { return h.AfterTurn }, in)
			}
			if a.cfg.OnCanonical != nil {
				e = errors.Join(e, a.cfg.OnCanonical(in.Messages))
			}
			if e != nil && !stopped {
				yield(&eino.AgentEvent{Err: e})
			}
		}()

		fail := func(e error) {
			runErr = errors.Join(runErr, e)
			if !yield(&eino.AgentEvent{Err: e}) {
				stopped = true
			}
		}
		before := &middleware.HookInput{Messages: messages}
		if e := func() error {
			if a.cfg.ManagedTurn {
				return nil
			}
			return bridge.Call(ctx, func(h middleware.LifecycleHooks) middleware.Callback { return h.BeforeTurn }, before)
		}(); e != nil {
			fail(e)
			return
		}
		messages = before.Messages
		bridge.Messages = messages
		for _, m := range messages {
			if m == nil || m.Role == schema.System {
				fail(errors.New("adk: input contains nil or duplicate system message"))
				return
			}
		}
		m, e := a.cfg.Models(ctx, a.cfg.ModelRef)
		if e != nil {
			fail(e)
			return
		}
		if m == nil {
			fail(errors.New("adk: nil resolved model"))
			return
		}
		handlers := append([]eino.ChatModelAgentMiddleware{bridge}, a.cfg.Middleware...)
		agent, e := eino.NewChatModelAgent(ctx, &eino.ChatModelAgentConfig{Name: a.cfg.Name, Description: "Agent loop", Instruction: a.cfg.SystemPrompt, Model: m, MaxIterations: a.cfg.MaxIterations, Handlers: handlers,
			ToolsConfig: eino.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{Tools: a.cfg.Tools, ExecuteSequentially: true}, ReturnDirectly: terminal},
			GenModelInput: func(_ context.Context, instruction string, in *eino.AgentInput) ([]*schema.Message, error) {
				return append([]*schema.Message{schema.SystemMessage(instruction)}, in.Messages...), nil
			},
		})
		if e != nil {
			fail(e)
			return
		}
		var root eino.Agent = agent
		if len(a.cfg.SubAgents) > 0 {
			subs := []eino.Agent{}
			for _, ref := range a.cfg.SubAgents {
				sub, e := ref.Create(ctx)
				if e != nil {
					fail(e)
					return
				}
				if sub == nil || sub.Name(ctx) != ref.Name {
					fail(errors.New("adk: subagent resolver mismatch"))
					return
				}
				subs = append(subs, sub)
			}
			root, e = eino.SetSubAgents(ctx, root, subs)
			if e != nil {
				fail(e)
				return
			}
		}
		it := eino.NewRunner(ctx, eino.RunnerConfig{Agent: root, EnableStreaming: false}).Run(ctx, messages)
		for {
			ev, ok := it.Next()
			if !ok {
				break
			}
			if ev.Err != nil {
				runErr = errors.Join(runErr, ev.Err)
			}
			if !yield(ev) {
				stopped = true
				runErr = context.Canceled
				cancel()
				return
			}
		}
	}
}
