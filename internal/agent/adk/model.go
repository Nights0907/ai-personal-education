package adk

import (
	"context"
	eino "github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/musi-ai/education/internal/agent/adk/middleware"
)

type ModelResolver func(context.Context, string) (model.BaseChatModel, error)
type Option func(*Config)

func WithTools(tools ...tool.BaseTool) Option {
	return func(c *Config) { c.Tools = append(c.Tools, tools...) }
}
func WithHooks(h ...middleware.LifecycleHooks) Option {
	return func(c *Config) { c.Hooks = append(c.Hooks, h...) }
}

// WithMiddleware is a low-level integration point for trusted runtime policies.
func WithMiddleware(h ...eino.ChatModelAgentMiddleware) Option {
	return func(c *Config) { c.Middleware = append(c.Middleware, h...) }
}

// Each subagent is resolved afresh for a Run, so its mutable Eino state is never
// shared across students. Managed teacher specs do not enable delegation yet.
type SubAgentRef struct {
	Name   string
	Create func(context.Context) (eino.Agent, error)
}

func WithSubAgents(refs ...SubAgentRef) Option {
	return func(c *Config) { c.SubAgents = append(c.SubAgents, refs...) }
}
