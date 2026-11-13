package runtime

import (
	"github.com/musi-ai/education/internal/agent/adk/middleware"
	"github.com/musi-ai/education/internal/agent/completion"
	"github.com/musi-ai/education/internal/agent/runtime/afs"
	ctxbuild "github.com/musi-ai/education/internal/agent/runtime/context"
	"github.com/musi-ai/education/internal/agent/runtime/event"
	"github.com/musi-ai/education/internal/agent/runtime/observability"
	"github.com/musi-ai/education/internal/agent/runtime/storage"
)

type Option func(*Config)

func WithStorage(s storage.Storage) Option      { return func(c *Config) { c.Store = s } }
func WithEventProducer(p event.Producer) Option { return func(c *Config) { c.EventProducer = p } }
func WithFileSystem(fs afs.FileSystem) Option   { return func(c *Config) { c.FileSystem = fs } }
func WithoutTrace() Option                      { return func(c *Config) { c.DisableTrace = true } }
func WithTurnProviders(p ...ctxbuild.TurnProvider) Option {
	return func(c *Config) { c.TurnProviders = append(c.TurnProviders, p...) }
}
func WithBusinessHooks(h ...middleware.LifecycleHooks) Option {
	return func(c *Config) { c.BusinessHooks = append(c.BusinessHooks, h...) }
}

func WithStaticProviders(p ...ctxbuild.StaticBlockProvider) Option {
	return func(c *Config) { c.StaticProviders = append(c.StaticProviders, p...) }
}

func WithContextBudgetHandler(h ContextBudgetHandler) Option {
	return func(c *Config) { c.OnContextBudgetExceeded = h }
}

func WithTracer(t observability.Tracer) Option { return func(c *Config) { c.Tracer = t } }
func WithCompletionPolicy(p completion.Policy) Option {
	return func(c *Config) { c.CompletionPolicy = p }
}
