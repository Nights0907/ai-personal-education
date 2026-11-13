// Package adk implements a pure Eino agent loop. It has no storage, filesystem,
// skill catalog, compactor, or application identity dependency.
package adk

import (
	"context"
	"errors"
	eino "github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/musi-ai/education/internal/agent/adk/middleware"
)

type Config struct {
	OnToolResult                 func(context.Context, *schema.Message) error
	ManagedTurn                  bool
	SubAgents                    []SubAgentRef
	OnCanonical                  func([]*schema.Message) error
	Name, SystemPrompt, ModelRef string
	Models                       ModelResolver
	MaxIterations                int
	Tools                        []tool.BaseTool
	Hooks                        []middleware.LifecycleHooks
	Middleware                   []eino.ChatModelAgentMiddleware
	ReturnDirectly               map[string]bool
}
type AgentLoop struct{ cfg Config }

func CreateAgentLoop(c Config, opts ...Option) (*AgentLoop, error) {
	for _, opt := range opts {
		opt(&c)
	}
	if c.Name == "" || c.Models == nil || c.ModelRef == "" || c.MaxIterations < 1 {
		return nil, errors.New("adk: invalid loop configuration")
	}
	c.SubAgents = append([]SubAgentRef(nil), c.SubAgents...)
	seen := map[string]bool{}
	for _, ref := range c.SubAgents {
		if ref.Name == "" || ref.Create == nil || seen[ref.Name] {
			return nil, errors.New("adk: invalid subagent reference")
		}
		seen[ref.Name] = true
	}
	cloned := map[string]bool{}
	for k, v := range c.ReturnDirectly {
		cloned[k] = v
	}
	c.ReturnDirectly = cloned
	c.Tools = append([]tool.BaseTool(nil), c.Tools...)
	c.Hooks = append([]middleware.LifecycleHooks(nil), c.Hooks...)
	c.Middleware = append([]eino.ChatModelAgentMiddleware(nil), c.Middleware...)
	return &AgentLoop{cfg: c}, nil
}
