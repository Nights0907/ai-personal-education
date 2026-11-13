package agentic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/musi-ai/education/internal/agent/adk"
	"github.com/musi-ai/education/internal/agent/adk/middleware"
	"github.com/musi-ai/education/internal/agent/completion"
	runtime "github.com/musi-ai/education/internal/agent/runtime"
	"github.com/musi-ai/education/internal/agent/runtime/afs"
	ctxbuild "github.com/musi-ai/education/internal/agent/runtime/context"
	"github.com/musi-ai/education/internal/agent/runtime/event"
	"github.com/musi-ai/education/internal/agent/runtime/observability"
	"github.com/musi-ai/education/internal/agent/runtime/state"
	"github.com/musi-ai/education/internal/agent/runtime/storage"
	"sort"
	"time"
)

const (
	BusinessPreload          = "business_preload"
	RuntimeInit              = "runtime_init"
	RuntimeTurn              = "runtime_turn"
	RuntimeMessageProjection = "runtime_message_projection"
)

type Request struct {
	Input state.Input
	Query string
}
type Packet struct {
	Stage        string `json:"stage,omitempty"`
	Data         any    `json:"data,omitempty"`
	MessageID    string `json:"message_id,omitempty"`
	TurnID       string `json:"turn_id,omitempty"`
	EndBusiness  bool   `json:"-"`
	Type         string `json:"type"`
	BizName      string `json:"biz_name"`
	Text         string `json:"text,omitempty"`
	Status       string `json:"status,omitempty"`
	ErrorCode    string `json:"error_code,omitempty"`
	CompletionID string `json:"completion_id,omitempty"`
	WarningCode  string `json:"warning_code,omitempty"`
}
type Sink interface {
	Send(context.Context, Packet) error
}
type SinkFunc func(context.Context, Packet) error

func (f SinkFunc) Send(c context.Context, p Packet) error { return f(c, p) }

type Dependencies struct {
	OnContextBudgetExceeded runtime.ContextBudgetHandler
	Store                   storage.Storage
	Models                  adk.ModelResolver
	FileSystem              afs.FileSystem
	Spec                    state.Spec
	Completion              completion.Policy
	Producer                event.Producer
	Tracer                  observability.Tracer
}
type Turn struct {
	UserMessageID, AssistantMessageID string
	Emit                              func(context.Context, Packet) error
	Request                           Request
	Contribution                      Contribution
	Runtime                           *runtime.Runtime
	Source                            *runtime.EventSource
	Result                            state.Result
	Err                               error
	Sink                              Sink
	Stages                            []string
	Packets                           []Packet
	ShortCircuit                      bool
}
type Pipeline struct {
	deps     Dependencies
	registry *Registry
	graph    compose.Runnable[*Turn, *Turn]
}

func New(ctx context.Context, d Dependencies, r *Registry) (*Pipeline, error) {
	if d.Store == nil || d.Models == nil || d.Spec.Name == "" {
		return nil, errors.New("agentic: store, model resolver and spec required")
	}
	p := &Pipeline{deps: d, registry: r}
	g := compose.NewGraph[*Turn, *Turn]()
	nodes := []struct {
		name string
		run  func(context.Context, *Turn) (*Turn, error)
	}{{BusinessPreload, p.preload}, {RuntimeInit, p.init}, {RuntimeTurn, p.run}, {RuntimeMessageProjection, p.project}}
	previous := compose.START
	for _, n := range nodes {
		if e := g.AddLambdaNode(n.name, compose.InvokableLambda(n.run)); e != nil {
			return nil, e
		}
		if e := g.AddEdge(previous, n.name); e != nil {
			return nil, e
		}
		previous = n.name
	}
	if e := g.AddEdge(previous, compose.END); e != nil {
		return nil, e
	}
	compiled, e := g.Compile(ctx, compose.WithGraphName("agentic_request"))
	if e != nil {
		return nil, e
	}
	p.graph = compiled
	return p, nil
}
func (p *Pipeline) Run(ctx context.Context, r Request, sink Sink) (*Turn, error) {
	if sink == nil {
		return nil, errors.New("agentic: output sink required")
	}
	t := &Turn{Request: r, Sink: sink}
	out, e := p.graph.Invoke(ctx, t)
	if e != nil {
		if t.Source != nil {
			t.Source.Close()
		}
		return t, e
	}
	return out, out.Err
}
func (p *Pipeline) preload(ctx context.Context, t *Turn) (*Turn, error) {
	t.Stages = append(t.Stages, BusinessPreload)
	if e := t.Request.Input.Identity.Scope.Validate(); e != nil {
		return t, e
	}
	if t.Request.Query == "" {
		return t, errors.New("agentic: query required")
	}
	c, e := p.registry.Resolve(ctx, t)
	if e != nil {
		return t, e
	}
	t.Contribution = c
	for _, hook := range c.PreloadHooks {
		if e = hook(ctx, t); e != nil {
			return t, e
		}
		if t.ShortCircuit {
			break
		}
	}
	return t, nil
}
func (p *Pipeline) init(ctx context.Context, t *Turn) (*Turn, error) {
	t.Stages = append(t.Stages, RuntimeInit)
	if t.ShortCircuit {
		return t, nil
	}
	in := t.Request.Input
	if in.Identity.TurnID == "" {
		return t, errors.New("agentic: server-bound turn ID required")
	}
	if in.Identity.RunID == "" {
		in.Identity.RunID = "run-" + state.Hash(time.Now().String())[:32]
	}
	if in.InputRef == "" {
		in.InputRef = state.MessageID(in.Identity.Scope, in.Identity.TurnID, "user")
	}
	if in.BindingRef == "" {
		in.BindingRef = in.InputRef
	}
	if in.ConfigRef == "" {
		in.ConfigRef = p.deps.Spec.Version
	}
	if in.Lease.Token == 0 {
		in.Lease = state.Lease{Token: time.Now().UnixNano(), Until: time.Now().Add(p.deps.Spec.Timeout + time.Minute)}
	}
	if e := in.Validate(); e != nil {
		return t, e
	}
	t.Request.Input = in
	t.UserMessageID = state.MessageID(in.Identity.Scope, in.Identity.TurnID, "user")
	t.AssistantMessageID = state.MessageID(in.Identity.Scope, in.Identity.TurnID, "assistant")
	spec := p.deps.Spec
	spec.ToolRefs = append([]string(nil), spec.ToolRefs...)
	for name := range t.Contribution.Tools {
		if !contains(spec.ToolRefs, name) {
			spec.ToolRefs = append(spec.ToolRefs, name)
		}
	}
	sort.Strings(spec.ToolRefs)
	// Terminal capabilities apply only when authorized in this turn.
	terminal := make([]string, 0, len(spec.TerminalTools))
	for _, name := range spec.TerminalTools {
		if contains(spec.ToolRefs, name) {
			terminal = append(terminal, name)
		}
	}
	spec.TerminalTools = terminal
	spec.SkillRefs = append([]string(nil), spec.SkillRefs...)
	for name := range t.Contribution.SkillPaths {
		if !contains(spec.SkillRefs, name) {
			spec.SkillRefs = append(spec.SkillRefs, name)
		}
	}
	sort.Strings(spec.SkillRefs)
	for _, feature := range t.Contribution.Features {
		hooks, e := feature.RuntimeHooks(ctx, t)
		if e != nil {
			return t, e
		}
		t.Contribution.LifecycleHooks = append(t.Contribution.LifecycleHooks, hooks...)
	}
	if len(t.Contribution.PreloadedFiles) > 0 {
		t.Contribution.TurnProviders = append(t.Contribution.TurnProviders, afs.FilesProvider("preloaded-files", "preloaded-files", ctxbuild.SlotPreamble, 300, p.deps.FileSystem, t.Contribution.PreloadedFiles))
	}
	startedCalls := map[string]time.Time{}
	emitTool := func(c context.Context, h *middleware.HookInput, status string) error {
		kind, name := "tool", h.ToolName
		if h.ToolName == "read_file" {
			var args struct {
				Path string `json:"path"`
			}
			_ = json.Unmarshal([]byte(h.Arguments), &args)
			for skill, path := range t.Contribution.SkillPaths {
				if path == args.Path {
					kind, name = "skill", skill
					break
				}
			}
		}
		data := map[string]string{"id": h.ToolCallID, "kind": kind, "name": name, "status": status, "run_id": in.Identity.RunID, "turn_id": in.Identity.TurnID, "at": time.Now().UTC().Format(time.RFC3339Nano)}
		if status == "running" {
			startedCalls[h.ToolCallID] = time.Now()
			data["arguments_json"] = h.Arguments
		} else {
			if start, ok := startedCalls[h.ToolCallID]; ok {
				data["elapsed_ms"] = fmt.Sprint(time.Since(start).Milliseconds())
			}
			data["result_json"] = h.Result
			if kind == "skill" {
				data["content_hash"] = state.Hash(h.Result)
			}
			if h.Err != nil {
				data["error"] = h.Err.Error()
			}
		}
		return t.Emit(c, Packet{Type: "progress", Stage: "execution_node", Data: data})
	}
	t.Contribution.LifecycleHooks = append(t.Contribution.LifecycleHooks, middleware.LifecycleHooks{Name: "visible-execution-nodes", BeforeTool: func(c context.Context, h *middleware.HookInput) (middleware.ToolDecision, error) {
		return middleware.Allow, emitTool(c, h, "running")
	}, AfterTool: func(c context.Context, h *middleware.HookInput) error {
		status := "completed"
		if h.Err != nil {
			status = "failed"
		}
		if h.ToolName == "validate_solution" {
			var v struct {
				Passed bool `json:"passed"`
			}
			if json.Unmarshal([]byte(h.Result), &v) == nil && !v.Passed {
				status = "needs_revision"
			}
		}
		return emitTool(c, h, status)
	}})
	bound := &binding{input: in, text: t.Request.Query, longTermMemory: t.Contribution.LongTermMemory}
	counter := ctxbuild.ConservativeCounter{}
	engine := &runtime.Factory{Models: p.deps.Models, Tools: t.Contribution.Tools, SkillPaths: t.Contribution.SkillPaths, FileSystem: p.deps.FileSystem, Guard: bound, Counter: counter}
	budgetHandler := p.deps.OnContextBudgetExceeded
	if budgetHandler != nil {
		original := budgetHandler
		budgetHandler = func(c context.Context, scope state.Scope, policy state.Policy) error {
			if err := t.Emit(c, Packet{Type: "progress", Stage: "compacting", Text: "正在整理之前的对话，请稍候，完成后将自动继续~"}); err != nil {
				return err
			}
			if err := original(c, scope, policy); err != nil {
				return err
			}
			return t.Emit(c, Packet{Type: "progress", Stage: "context_ready", Text: "对话整理完成，AI教师继续解题中~"})
		}
	}
	rt, e := runtime.New(runtime.Config{OnContextBudgetExceeded: budgetHandler, Store: p.deps.Store, Provider: bound, Guard: bound, Engine: engine, Counter: counter, Specs: map[string]state.Spec{in.ConfigRef: spec}, CompletionPolicy: p.deps.Completion, EventProducer: p.deps.Producer, Tracer: p.deps.Tracer}, runtime.WithBusinessHooks(t.Contribution.LifecycleHooks...), runtime.WithTurnProviders(t.Contribution.TurnProviders...), runtime.WithStaticProviders(t.Contribution.StaticProviders...))
	if e != nil {
		return t, e
	}
	t.Runtime = rt
	return t, nil
}
func (p *Pipeline) run(ctx context.Context, t *Turn) (*Turn, error) {
	t.Stages = append(t.Stages, RuntimeTurn)
	if !t.ShortCircuit {
		t.Source = t.Runtime.StreamTurn(ctx, t.Request.Input)
	}
	return t, nil
}
func contains(v []string, s string) bool {
	for _, x := range v {
		if x == s {
			return true
		}
	}
	return false
}

type binding struct {
	input          state.Input
	text           string
	longTermMemory json.RawMessage
}

func (b *binding) Check(ctx context.Context, in state.Input) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	if state.Hash(b.input) != state.Hash(in) {
		return state.ErrConflict
	}
	return in.Validate()
}
func (b *binding) Provide(ctx context.Context, in state.Input) (state.Binding, error) {
	if e := b.Check(ctx, in); e != nil {
		return state.Binding{}, e
	}
	return state.Binding{SourceVersion: state.Hash([]string{b.text, string(b.longTermMemory)}), Input: schema.UserMessage(b.text), LongTermMemory: b.longTermMemory}, nil
}
