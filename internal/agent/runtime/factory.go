package runtime

import (
	"context"
	"errors"
	"fmt"
	sdk "github.com/musi-ai/education/internal/agent/adk"
	hook "github.com/musi-ai/education/internal/agent/adk/middleware"
	"github.com/musi-ai/education/internal/agent/runtime/afs"
	fsinmem "github.com/musi-ai/education/internal/agent/runtime/afs/inmem"

	catalogskill "github.com/musi-ai/education/internal/agent/runtime/skill"
	"gopkg.in/yaml.v3"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/skill"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	ctxbuild "github.com/musi-ai/education/internal/agent/runtime/context"
	"github.com/musi-ai/education/internal/agent/runtime/state"
)

type ModelResolver = sdk.ModelResolver

// Factory is constructed once; registry maps must remain immutable after startup.
// Each Execute creates a fresh Eino agent and middleware state.
type skillResource struct {
	fs      afs.FileSystem
	catalog *catalogskill.Catalog
}

type Factory struct {
	catalogResources map[string]skillResource
	TraceEnabled     bool
	Models           ModelResolver
	Tools            map[string]tool.InvokableTool
	Skills           map[string]skill.Skill
	Guard            Guard
	Counter          ctxbuild.Counter
	FileSystem       afs.FileSystem
	SkillPaths       map[string]string
	Hooks            []hook.LifecycleHooks
}

// skillResources is runtime-owned. Legacy embedded skill values are mounted as
// immutable virtual files, not registered as Eino skill middleware.
func (f *Factory) skillResources(ctx context.Context, spec state.Spec) (afs.FileSystem, *catalogskill.Catalog, error) {
	if resource, ok := f.catalogResources[state.Hash(spec.SkillRefs)]; ok {
		return resource.fs, resource.catalog, nil
	}
	fs := f.FileSystem
	files := map[string][]byte{}
	refs := []catalogskill.Ref{}
	seen := map[string]bool{}
	for _, name := range spec.SkillRefs {
		if seen[name] || strings.ContainsAny(name, "/\\") {
			return nil, nil, state.ErrConflict
		}
		seen[name] = true
		p := f.SkillPaths[name]
		if fs == nil {
			s, ok := f.Skills[name]
			if !ok || s.Name != name || s.Context != "" || s.Model != "" || s.Agent != "" {
				return nil, nil, fmt.Errorf("runtime: missing or unsupported skill %s", name)
			}
			p = "/skills/" + name + "/SKILL.md"
			header, e := yaml.Marshal(s.FrontMatter)
			if e != nil {
				return nil, nil, e
			}
			files[p] = []byte("---\n" + string(header) + "---\n" + s.Content)
		}
		if p == "" {
			return nil, nil, fmt.Errorf("runtime: no mounted path for skill %s", name)
		}
		refs = append(refs, catalogskill.Ref{Path: p})
	}
	if fs == nil {
		var e error
		fs, e = fsinmem.New(files)
		if e != nil {
			return nil, nil, e
		}
	}
	c, e := catalogskill.NewCatalog(ctx, fs, refs)
	if e != nil {
		return nil, nil, e
	}
	for i, name := range spec.SkillRefs {
		if c.Items[i].Name != name {
			return nil, nil, state.ErrConflict
		}
	}
	return fs, c, nil
}
func (f *Factory) SkillCatalog(ctx context.Context, spec state.Spec) ([]state.SkillDescriptor, error) {
	_, c, e := f.skillResources(ctx, spec)
	if e != nil {
		return nil, e
	}
	return c.Items, nil
}

func (f *Factory) Execute(ctx context.Context, in state.Input, spec state.Spec, snap state.Snapshot, observe Observer, capture Capture) ([]*schema.Message, error) {
	if f.Models == nil || f.Guard == nil || f.Counter == nil {
		return nil, errors.New("runtime: incomplete Eino factory")
	}
	if len(snap.Messages) == 0 || snap.Messages[0].Role != schema.System {
		return nil, state.ErrProtocol
	}
	var ts []tool.BaseTool
	snap.CapabilityHashes = map[string]string{}
	allowed := map[string]bool{}
	terminal := map[string]bool{}
	for _, name := range spec.ToolRefs {
		t, ok := f.Tools[name]
		if !ok || allowed[name] {
			return nil, fmt.Errorf("runtime: missing or duplicate tool %s", name)
		}
		info, e := t.Info(ctx)
		if e != nil {
			return nil, e
		}
		if info.Name != name {
			return nil, errors.New("runtime: registry/tool name mismatch")
		}
		allowed[name] = true
		snap.CapabilityHashes["tool:"+name] = state.Hash(info)
		ts = append(ts, t)
	}
	for _, name := range spec.TerminalTools {
		if !allowed[name] {
			return nil, errors.New("runtime: terminal tool not authorized")
		}
		terminal[name] = true
	}
	fs, catalog, err := f.skillResources(ctx, spec)
	if err != nil {
		return nil, err
	}
	if len(catalog.Items) > 0 || f.FileSystem != nil {
		for _, t := range afs.Tools(fs) {
			info, e := t.Info(ctx)
			if e != nil {
				return nil, e
			}
			if allowed[info.Name] {
				return nil, errors.New("runtime: filesystem tool name collision")
			}
			allowed[info.Name] = true
			ts = append(ts, t)
			snap.CapabilityHashes["tool:"+info.Name] = state.Hash(info)
		}
	}
	for _, item := range catalog.Items {
		snap.CapabilityHashes["skill:"+item.Name] = state.Hash(item)
	}
	policy := &runPolicy{BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{}, input: in, spec: spec, guard: f.Guard, counter: f.Counter, allowed: allowed, observe: observe, capture: capture, snapshot: snap}
	hooks := []hook.LifecycleHooks{{Name: "runtime.guard", Policy: hook.HookPolicy{Mode: hook.FailTurn}, BeforeTurn: func(c context.Context, _ *hook.HookInput) error { return f.Guard.Check(c, in) }, BeforeAgent: func(c context.Context, _ *hook.HookInput) error { return f.Guard.Check(c, in) }, BeforeModelRewriteState: func(c context.Context, _ *hook.HookInput) error { return f.Guard.Check(c, in) }, BeforeModel: func(c context.Context, _ *hook.HookInput) error { return f.Guard.Check(c, in) }, BeforeTool: func(c context.Context, _ *hook.HookInput) (hook.ToolDecision, error) {
		return hook.Allow, f.Guard.Check(c, in)
	}}}
	managedHooks, managed := lifecycle(ctx)
	if managed {
		hooks = managedHooks
	} else {
		hooks = append(hooks, f.Hooks...)
	}
	loop, err := sdk.CreateAgentLoop(sdk.Config{OnToolResult: func(c context.Context, m *schema.Message) error {
		emitLive(c, TurnEvent{Type: "message", Message: state.CleanMessages([]*schema.Message{m})[0]})
		return nil
	}, ManagedTurn: managed, Name: spec.Name, SystemPrompt: snap.Messages[0].Content, ModelRef: spec.ModelRef, Models: f.Models, MaxIterations: spec.MaxIterations, ReturnDirectly: terminal, OnCanonical: func(messages []*schema.Message) error {
		// After all rewrite hooks, reconcile from the canonical state, not the
		// incremental observer. Only this turn's suffix may enter storage.
		if len(messages) >= len(snap.Messages) && messages[0].Role == schema.System {
			if e := policy.checkPrefix(messages); e != nil {
				return e
			}
			current := messages[len(snap.Messages)-1]
			if snap.CanonicalInput != nil {
				*snap.CanonicalInput = *state.CleanMessages([]*schema.Message{current})[0]
			}
			policy.mu.Lock()
			policy.generated = state.CleanMessages(messages[len(snap.Messages):])
			policy.mu.Unlock()
		}
		return nil
	}}, sdk.WithTools(ts...), sdk.WithHooks(hooks...), sdk.WithMiddleware(policy))
	if err != nil {
		return nil, err
	}
	ctx = context.WithValue(ctx, invocationKey{}, in)
	var runErr error
	for event := range loop.Run(ctx, state.CleanMessages(snap.Messages[1:])) {
		if event.Err != nil {
			runErr = errors.Join(runErr, event.Err)
		}
	}
	return policy.messages(), runErr
}

type runPolicy struct {
	*adk.BaseChatModelAgentMiddleware
	mu        sync.Mutex
	input     state.Input
	spec      state.Spec
	guard     Guard
	counter   ctxbuild.Counter
	allowed   map[string]bool
	observe   Observer
	generated []*schema.Message
	calls     int
	capture   Capture
	snapshot  state.Snapshot
	iteration int
}

func (p *runPolicy) messages() []*schema.Message {
	p.mu.Lock()
	defer p.mu.Unlock()
	return state.CleanMessages(p.generated)
}
func (p *runPolicy) record(ctx context.Context, m *schema.Message) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.generated = append(p.generated, state.CleanMessages([]*schema.Message{m})...)
	return p.observe(ctx, m)
}
func (p *runPolicy) BeforeAgent(ctx context.Context, rc *adk.ChatModelAgentContext) (context.Context, *adk.ChatModelAgentContext, error) {
	for _, t := range rc.Tools {
		info, err := t.Info(ctx)
		if err != nil {
			return ctx, rc, err
		}
		if !p.allowed[info.Name] {
			return ctx, rc, state.ErrConflict
		}
		if _, ok := t.(tool.InvokableTool); !ok {
			return ctx, rc, errors.New("runtime: only synchronous tools supported")
		}
	}
	return ctx, rc, p.guard.Check(ctx, p.input)
}
func (p *runPolicy) BeforeModelRewriteState(ctx context.Context, s *adk.ChatModelAgentState, mc *adk.ModelContext) (context.Context, *adk.ChatModelAgentState, error) {
	if err := p.guard.Check(ctx, p.input); err != nil {
		return ctx, s, err
	}
	for _, ti := range s.ToolInfos {
		if !p.allowed[ti.Name] {
			return ctx, s, state.ErrConflict
		}
	}
	if err := p.checkPrefix(s.Messages); err != nil {
		return ctx, s, err
	}
	// Normalize the actual request too, so the persisted model-input snapshot
	// exactly matches the text-only protocol sent to the provider.
	s.Messages = state.CleanMessages(s.Messages)
	n, err := p.counter.Count(s.Messages, s.ToolInfos)
	if err != nil {
		return ctx, s, err
	}
	p.iteration++
	snap := p.snapshot
	snap.BlockHashes = map[string]string{}
	for name, hash := range p.snapshot.BlockHashes {
		snap.BlockHashes[name] = hash
	}
	snap.Phase = "model_input"
	snap.ModelIteration = p.iteration
	snap.Messages = state.CleanMessages(s.Messages)
	snap.ToolSchemas = s.ToolInfos
	if len(snap.Messages) > 0 {
		snap.InstructionHash = state.Hash(snap.Messages[0].Content)
		snap.BlockHashes["system-prompt"] = snap.InstructionHash
	}
	snap.EstimatedTokens = n
	snap.Usage = state.ContextUsage{InputTokens: n, EstimatedInputTokens: n, Source: "estimate", ModelRef: snap.ModelRef, TurnID: p.input.Identity.TurnID, SummaryRevision: snap.SummaryRevision, RecordedAt: time.Now().UTC()}
	p.snapshot.Usage = snap.Usage
	snap.Hash = state.Hash(struct {
		Messages []*schema.Message
		Tools    []*schema.ToolInfo
	}{snap.Messages, snap.ToolSchemas})
	if err = p.capture(ctx, snap); err != nil {
		return ctx, s, err
	}
	if n > p.spec.History.InputLimit() {
		err = state.ErrBudget
	}
	return ctx, s, err
}
func (p *runPolicy) AfterModelRewriteState(ctx context.Context, s *adk.ChatModelAgentState, mc *adk.ModelContext) (context.Context, *adk.ChatModelAgentState, error) {
	if len(s.Messages) == 0 {
		return ctx, s, state.ErrProtocol
	}
	m := s.Messages[len(s.Messages)-1]
	if m.ResponseMeta != nil && m.ResponseMeta.Usage != nil && m.ResponseMeta.Usage.PromptTokens > 0 {
		snap := p.snapshot
		snap.Phase = "model_output"
		snap.ModelIteration = p.iteration
		snap.Usage = state.ContextUsage{InputTokens: m.ResponseMeta.Usage.PromptTokens, EstimatedInputTokens: p.snapshot.Usage.EstimatedInputTokens, CompletionTokens: m.ResponseMeta.Usage.CompletionTokens, Source: "provider", ModelRef: snap.ModelRef, TurnID: p.input.Identity.TurnID, SummaryRevision: snap.SummaryRevision, RecordedAt: time.Now().UTC()}
		p.snapshot.Usage = snap.Usage
		if err := p.capture(ctx, snap); err != nil {
			return ctx, s, err
		}
	}
	if err := p.record(ctx, m); err != nil {
		return ctx, s, err
	}
	p.mu.Lock()
	if len(s.Messages) >= len(p.snapshot.Messages) {
		p.generated = state.CleanMessages(s.Messages[len(p.snapshot.Messages):])
	}
	p.mu.Unlock()
	if err := p.checkPrefix(s.Messages); err != nil {
		return ctx, s, err
	}
	// Reject the whole batch before any tool runs (including filesystem calls).
	if m.Role != schema.Assistant || len(m.ToolCalls) > 1 {
		return ctx, s, errors.New("runtime: invalid assistant role or parallel tool batch")
	}
	for _, tc := range m.ToolCalls {
		if tc.ID == "" || !p.allowed[tc.Function.Name] {
			return ctx, s, state.ErrConflict
		}
	}
	return ctx, s, nil
}
func (p *runPolicy) WrapInvokableToolCall(ctx context.Context, next adk.InvokableToolCallEndpoint, t *adk.ToolContext) (adk.InvokableToolCallEndpoint, error) {
	return func(c context.Context, args string, opts ...tool.Option) (string, error) {
		if err := p.guard.Check(c, p.input); err != nil {
			return "", err
		}
		p.mu.Lock()
		p.calls++
		count := p.calls
		p.mu.Unlock()
		if !p.allowed[t.Name] || count > p.spec.MaxToolCalls {
			return "", errors.New("runtime: tool denied or budget exceeded")
		}
		c = context.WithValue(c, toolInvocationKey{}, ToolInvocation{CallID: t.CallID, Name: t.Name, Arguments: args, ArgumentsHash: state.Hash(args)})
		p.mu.Lock()
		var prefix []*schema.Message
		if p.snapshot.CanonicalInput != nil {
			prefix = state.CleanMessages([]*schema.Message{p.snapshot.CanonicalInput})
		} else if p.snapshot.OriginalInput != nil {
			prefix = state.CleanMessages([]*schema.Message{p.snapshot.OriginalInput})
		} else if len(p.snapshot.Messages) > 0 {
			prefix = state.CleanMessages(p.snapshot.Messages[len(p.snapshot.Messages)-1:])
		}
		prefix = append(prefix, state.CleanMessages(p.generated)...)
		p.mu.Unlock()
		c = context.WithValue(c, recoveryPrefixKey{}, prefix)
		out, err := next(c, args, opts...)
		if err != nil {
			return out, err
		}
		msg := &schema.Message{Role: schema.Tool, Content: out, ToolCallID: t.CallID, ToolName: t.Name}
		if err = p.record(c, msg); err != nil {
			return "", err
		}
		return out, nil
	}, nil
}

// Already-stored history and the stable prefix may not be rewritten by a
// per-turn hook. Rewrites are restricted to this turn's user and generated tail.
func (p *runPolicy) checkPrefix(messages []*schema.Message) error {
	n := len(p.snapshot.Messages)
	if n < 1 || len(messages) < n {
		return state.ErrProtocol
	}
	if state.Hash(state.CleanMessages(messages[:n-1])) != state.Hash(state.CleanMessages(p.snapshot.Messages[:n-1])) {
		return state.ErrConflict
	}
	user := messages[n-1]
	if user == nil || user.Role != schema.User || len(user.ToolCalls) > 0 || user.ToolCallID != "" {
		return state.ErrProtocol
	}
	return nil
}
