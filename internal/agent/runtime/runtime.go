// Package runtime embeds a managed, stateless-across-turns Eino runtime in workers.
package runtime

import (
	"context"
	"errors"
	"github.com/musi-ai/education/internal/agent/completion"
	"github.com/musi-ai/education/internal/agent/runtime/observability"
	"strings"

	"github.com/cloudwego/eino/schema"
	"github.com/musi-ai/education/internal/agent/adk/middleware"
	"github.com/musi-ai/education/internal/agent/runtime/afs"
	ctxbuild "github.com/musi-ai/education/internal/agent/runtime/context"
	"github.com/musi-ai/education/internal/agent/runtime/event"
	"github.com/musi-ai/education/internal/agent/runtime/event/noop"
	"github.com/musi-ai/education/internal/agent/runtime/prompt"
	"github.com/musi-ai/education/internal/agent/runtime/state"
	"github.com/musi-ai/education/internal/agent/runtime/storage"
)

type AgentSpec = state.Spec
type RunTurnInput = state.Input
type RunResult = state.Result
type ContextProvider interface {
	Provide(context.Context, state.Input) (state.Binding, error)
}
type Guard interface {
	Check(context.Context, state.Input) error
}
type CompletionResolver interface {
	FindCommittedResult(context.Context, state.Input) (*state.Receipt, error)
}

// Observer must succeed before the next external action. Engine does not own queues or SQL.
type Observer func(context.Context, *schema.Message) error
type Capture func(context.Context, state.Snapshot) error
type Engine interface {
	Execute(context.Context, state.Input, state.Spec, state.Snapshot, Observer, Capture) ([]*schema.Message, error)
}
type SkillCatalogProvider interface {
	SkillCatalog(context.Context, state.Spec) ([]state.SkillDescriptor, error)
}
type ContextBudgetHandler func(context.Context, state.Scope, state.Policy) error

type Config struct {
	Tracer                  observability.Tracer
	Store                   storage.Storage
	Provider                ContextProvider
	Guard                   Guard
	Completion              CompletionResolver
	CompletionPolicy        completion.Policy
	Engine                  Engine
	Specs                   map[string]state.Spec
	Counter                 ctxbuild.Counter
	OnContextBudgetExceeded ContextBudgetHandler
	EventProducer           event.Producer
	FileSystem              afs.FileSystem
	TurnProviders           []ctxbuild.TurnProvider
	BusinessHooks           []middleware.LifecycleHooks
	DisableTrace            bool
	StaticProviders         []ctxbuild.StaticBlockProvider
	staticSuffix            string
}
type Runtime struct{ cfg Config }

func New(cfg Config, opts ...Option) (*Runtime, error) {
	for _, opt := range opts {
		opt(&cfg)
	}
	suffix, e := ctxbuild.AssembleSystemPrompt("", cfg.StaticProviders...)
	if e != nil {
		return nil, e
	}
	cfg.staticSuffix = strings.TrimSpace(suffix)
	if cfg.Tracer == nil {
		cfg.Tracer = observability.NewTracer(nil)
	}
	if cfg.EventProducer == nil {
		cfg.EventProducer = noop.Producer{}
	}
	cfg.TurnProviders = append([]ctxbuild.TurnProvider(nil), cfg.TurnProviders...)
	cfg.BusinessHooks = append([]middleware.LifecycleHooks(nil), cfg.BusinessHooks...)
	if factory, ok := cfg.Engine.(*Factory); ok {
		clone := *factory
		clone.TraceEnabled = !cfg.DisableTrace
		clone.catalogResources = map[string]skillResource{}
		if cfg.FileSystem != nil {
			clone.FileSystem = cfg.FileSystem
		}
		cfg.BusinessHooks = append(append([]middleware.LifecycleHooks(nil), factory.Hooks...), cfg.BusinessHooks...)
		clone.Hooks = nil
		for i := range cfg.BusinessHooks {
			if cfg.BusinessHooks[i].Policy.Mode == middleware.Default {
				cfg.BusinessHooks[i].Policy.Mode = middleware.DegradeAndContinue
			}
		}
		cfg.Engine = &clone
	} else if cfg.FileSystem != nil || len(cfg.BusinessHooks) > 0 {
		return nil, errors.New("runtime: filesystem and business lifecycle options require the Eino Factory")
	}
	if cfg.Store == nil || cfg.Provider == nil || cfg.Guard == nil || cfg.Engine == nil || cfg.Counter == nil || len(cfg.Specs) == 0 {
		return nil, errors.New("runtime: all dependencies and server-side specs required")
	}
	copySpecs := map[string]state.Spec{}
	for key, s := range cfg.Specs {
		if s.Name == "" || s.Version == "" || s.ModelRef == "" || s.MaxIterations < 1 || s.MaxToolCalls < 1 || s.Timeout <= 0 {
			return nil, errors.New("runtime: invalid spec")
		}
		if s.Instruction != "" && s.InstructionFile != "" {
			return nil, errors.New("runtime: choose an inline instruction or Markdown file, not both")
		}
		if s.Instruction == "" {
			path, err := prompt.Resolve(s.InstructionFile)
			if err != nil {
				return nil, err
			}
			if _, err = prompt.Read(path); err != nil {
				return nil, err
			}
			s.InstructionFile = path
		}
		if s.Instruction != "" {
			s.Instruction = staticPrompt(s.Instruction, cfg.staticSuffix)
		}
		if err := s.History.Validate(); err != nil {
			return nil, err
		}
		s.ToolRefs = append([]string(nil), s.ToolRefs...)
		s.SkillRefs = append([]string(nil), s.SkillRefs...)
		s.TerminalTools = append([]string(nil), s.TerminalTools...)
		copySpecs[key] = s
	}
	if factory, ok := cfg.Engine.(*Factory); ok {
		for _, spec := range copySpecs {
			fs, catalog, e := factory.skillResources(context.Background(), spec)
			if e != nil {
				return nil, e
			}
			factory.catalogResources[state.Hash(spec.SkillRefs)] = skillResource{fs: fs, catalog: catalog}
		}
	}
	if cfg.CompletionPolicy == nil {
		if cfg.Completion != nil {
			cfg.CompletionPolicy = completion.Receipts{Resolver: cfg.Completion}
		} else {
			cfg.CompletionPolicy = completion.Dialogue{Store: cfg.Store}
		}
	}
	cfg.Specs = copySpecs
	return &Runtime{cfg: cfg}, nil
}

func staticPrompt(base, suffix string) string {
	if !strings.Contains(base, "<system-prompt>") {
		base = "<system-prompt>\n" + base + "\n</system-prompt>"
	}
	if suffix != "" {
		base += "\n" + suffix
	}
	return base
}
