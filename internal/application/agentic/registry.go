// Package agentic owns the business request pipeline, outside Managed Runtime.
package agentic

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/cloudwego/eino/components/tool"
	"github.com/musi-ai/education/internal/agent/adk/middleware"
	"github.com/musi-ai/education/internal/agent/runtime/afs"
	ctxbuild "github.com/musi-ai/education/internal/agent/runtime/context"
)

type PacketHook struct {
	BeforeSend func(context.Context, *Packet) (bool, error)
	AfterSend  func(context.Context, Packet) error
}
type BusinessLifecycle struct {
	BeforeBusiness, AfterBusiness func(context.Context, *Turn) error
}
type Feature interface {
	RuntimeHooks(context.Context, *Turn) ([]middleware.LifecycleHooks, error)
}
type FeatureFunc func(context.Context, *Turn) ([]middleware.LifecycleHooks, error)

func (f FeatureFunc) RuntimeHooks(ctx context.Context, t *Turn) ([]middleware.LifecycleHooks, error) {
	return f(ctx, t)
}

type Contribution struct {
	LongTermMemory    json.RawMessage
	Features          []Feature
	PreloadedFiles    []afs.FileRef
	Tools             map[string]tool.InvokableTool
	SkillPaths        map[string]string
	PreloadHooks      []func(context.Context, *Turn) error
	LifecycleHooks    []middleware.LifecycleHooks
	TurnProviders     []ctxbuild.TurnProvider
	StaticProviders   []ctxbuild.StaticBlockProvider
	PacketHooks       []PacketHook
	PostRuntimeHooks  []func(context.Context, *Turn) error
	BusinessLifecycle map[string]BusinessLifecycle
	ToolHandlers      map[string]func(context.Context, string) ([]Packet, error)
}
type Registration interface {
	Contribute(context.Context, *Turn) (Contribution, error)
}
type RegistrationFunc func(context.Context, *Turn) (Contribution, error)

func (f RegistrationFunc) Contribute(c context.Context, t *Turn) (Contribution, error) {
	return f(c, t)
}

type Registry struct{ modules []Registration }

func NewRegistry(modules ...Registration) *Registry {
	return &Registry{modules: append([]Registration(nil), modules...)}
}
func (r *Registry) Resolve(ctx context.Context, t *Turn) (Contribution, error) {
	out := Contribution{Tools: map[string]tool.InvokableTool{}, SkillPaths: map[string]string{}, BusinessLifecycle: map[string]BusinessLifecycle{}, ToolHandlers: map[string]func(context.Context, string) ([]Packet, error){}}
	if r == nil {
		return out, nil
	}
	for _, module := range r.modules {
		c, e := module.Contribute(ctx, t)
		if e != nil {
			return out, e
		}
		if len(c.LongTermMemory) > 0 {
			if len(out.LongTermMemory) > 0 {
				return out, errors.New("agentic: duplicate long-term memory provider")
			}
			out.LongTermMemory = append(json.RawMessage(nil), c.LongTermMemory...)
		}
		for k, v := range c.Tools {
			if _, ok := out.Tools[k]; ok {
				return out, errors.New("agentic: duplicate tool registration")
			}
			out.Tools[k] = v
		}
		for k, v := range c.SkillPaths {
			out.SkillPaths[k] = v
		}
		for k, v := range c.BusinessLifecycle {
			if _, ok := out.BusinessLifecycle[k]; ok {
				return out, errors.New("agentic: duplicate business lifecycle")
			}
			out.BusinessLifecycle[k] = v
		}
		for k, v := range c.ToolHandlers {
			if _, ok := out.ToolHandlers[k]; ok {
				return out, errors.New("agentic: duplicate tool projection handler")
			}
			out.ToolHandlers[k] = v
		}
		out.Features = append(out.Features, c.Features...)
		out.PreloadedFiles = append(out.PreloadedFiles, c.PreloadedFiles...)
		out.PreloadHooks = append(out.PreloadHooks, c.PreloadHooks...)
		out.LifecycleHooks = append(out.LifecycleHooks, c.LifecycleHooks...)
		out.TurnProviders = append(out.TurnProviders, c.TurnProviders...)
		out.StaticProviders = append(out.StaticProviders, c.StaticProviders...)
		out.PacketHooks = append(out.PacketHooks, c.PacketHooks...)
		out.PostRuntimeHooks = append(out.PostRuntimeHooks, c.PostRuntimeHooks...)
	}
	return out, nil
}
