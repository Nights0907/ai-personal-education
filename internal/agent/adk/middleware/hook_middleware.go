package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"io"
)

// Adapter bridges veto-capable Eino middleware, rather than observer callbacks.
// One instance is allocated for each Run; no conversation state survives a turn.
type Adapter struct {
	StopOnCommittedSubmission bool
	committedSubmission       string
	OnToolResult              func(context.Context, *schema.Message) error
	*adk.BaseChatModelAgentMiddleware
	Hooks      []LifecycleHooks
	Messages   []*schema.Message
	toolChoice *schema.ToolChoice
	toolInfos  []*schema.ToolInfo
}

func NewAdapter(h []LifecycleHooks) *Adapter {
	return &Adapter{BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{}, Hooks: h}
}
func (a *Adapter) Call(ctx context.Context, selectFn func(LifecycleHooks) Callback, in *HookInput) error {
	for _, h := range a.Hooks {
		if e := Invoke(ctx, h, selectFn(h), in); e != nil {
			return e
		}
	}
	return nil
}
func (a *Adapter) BeforeAgent(ctx context.Context, r *adk.ChatModelAgentContext) (context.Context, *adk.ChatModelAgentContext, error) {
	return ctx, r, a.Call(ctx, func(h LifecycleHooks) Callback { return h.BeforeAgent }, &HookInput{Messages: a.Messages})
}
func (a *Adapter) BeforeModelRewriteState(ctx context.Context, s *adk.ChatModelAgentState, _ *adk.ModelContext) (context.Context, *adk.ChatModelAgentState, error) {
	if a.toolInfos == nil {
		a.toolInfos = append([]*schema.ToolInfo(nil), s.ToolInfos...)
	} else {
		s.ToolInfos = append([]*schema.ToolInfo(nil), a.toolInfos...)
	}
	in := &HookInput{Messages: s.Messages, Context: ctx}
	e := a.Call(ctx, func(h LifecycleHooks) Callback { return h.BeforeModelRewriteState }, in)
	if e == nil {
		e = a.Call(ctx, func(h LifecycleHooks) Callback { return h.BeforeModel }, in)
	}
	if in.ToolAllowlist != nil {
		allowed := map[string]bool{}
		for _, name := range in.ToolAllowlist {
			allowed[name] = true
		}
		filtered := make([]*schema.ToolInfo, 0, len(s.ToolInfos))
		for _, info := range s.ToolInfos {
			if allowed[info.Name] {
				filtered = append(filtered, info)
			}
		}
		s.ToolInfos = filtered
	}
	s.Messages = in.Messages
	a.Messages = s.Messages
	a.toolChoice = in.ToolChoice
	if in.Context != nil {
		ctx = in.Context
	}
	return ctx, s, e
}
func (a *Adapter) AfterModelRewriteState(ctx context.Context, s *adk.ChatModelAgentState, _ *adk.ModelContext) (context.Context, *adk.ChatModelAgentState, error) {
	in := &HookInput{Messages: s.Messages, Context: ctx}
	e := a.Call(ctx, func(h LifecycleHooks) Callback { return h.AfterModel }, in)
	if e == nil {
		e = a.Call(ctx, func(h LifecycleHooks) Callback { return h.AfterModelRewriteState }, in)
	}
	s.Messages = in.Messages
	a.Messages = s.Messages
	if in.Context != nil {
		ctx = in.Context
	}
	return ctx, s, e
}
func (a *Adapter) WrapInvokableToolCall(_ context.Context, next adk.InvokableToolCallEndpoint, t *adk.ToolContext) (adk.InvokableToolCallEndpoint, error) {
	return func(ctx context.Context, args string, opts ...tool.Option) (string, error) {
		in := &HookInput{Messages: a.Messages, ToolName: t.Name, ToolCallID: t.CallID, Arguments: args}
		for _, h := range a.Hooks {
			if h.BeforeTool == nil {
				continue
			}
			decision := Allow
			e := Invoke(ctx, h, func(c context.Context, i *HookInput) error { d, e := h.BeforeTool(c, i); decision = d; return e }, in)
			if e != nil {
				return "", e
			}
			if decision != Allow {
				return "", errors.New("adk: tool denied or requires confirmation")
			}
		}
		if in.Context != nil {
			ctx = in.Context
		}
		out, e := next(ctx, args, opts...)
		if e != nil {
			return out, e
		}
		in.Result = out
		if e = a.Call(ctx, func(h LifecycleHooks) Callback { return h.AfterTool }, in); e != nil {
			return "", e
		}
		message := &schema.Message{Role: schema.Tool, Content: in.Result, ToolCallID: t.CallID, ToolName: t.Name}
		a.Messages = append(a.Messages, message)
		if a.OnToolResult != nil {
			if e = a.OnToolResult(ctx, message); e != nil {
				return "", e
			}
		}
		if a.StopOnCommittedSubmission && t.Name == "submit_solution" {
			var receipt struct {
				Committed bool `json:"committed"`
			}
			if json.Unmarshal([]byte(in.Result), &receipt) == nil && receipt.Committed {
				a.committedSubmission = in.Result
			}
		}
		return in.Result, nil
	}, nil
}

// WrapModel applies the business hook's constraint without selecting a tool or
// inventing a tool call. Ordinary dialogue keeps the provider default.
func (a *Adapter) WrapModel(_ context.Context, m model.BaseChatModel, _ *adk.ModelContext) (model.BaseChatModel, error) {
	return &constrainedModel{BaseChatModel: m, adapter: a}, nil
}

type constrainedModel struct {
	model.BaseChatModel
	adapter *Adapter
}

func (m *constrainedModel) options(opts []model.Option) []model.Option {
	if m.adapter.toolChoice == nil {
		return opts
	}
	return append(append([]model.Option(nil), opts...), model.WithToolChoice(*m.adapter.toolChoice))
}
func (m *constrainedModel) Generate(ctx context.Context, ms []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	if m.adapter.committedSubmission != "" {
		return schema.AssistantMessage("", nil), nil
	}
	return m.validated(ctx, ms, opts, func(options ...model.Option) (*schema.Message, error) {
		return m.BaseChatModel.Generate(ctx, ms, options...)
	})
}
func (m *constrainedModel) validated(ctx context.Context, ms []*schema.Message, opts []model.Option, call func(...model.Option) (*schema.Message, error)) (*schema.Message, error) {
	out, err := call(m.options(opts)...)
	if err != nil {
		return nil, err
	}
	validate := func(message *schema.Message) error {
		return m.adapter.Call(ctx, func(h LifecycleHooks) Callback { return h.ValidateModelOutput }, &HookInput{Messages: append(append([]*schema.Message(nil), ms...), message)})
	}
	if err = validate(out); errors.Is(err, ErrToolRequired) {
		// Discard the unverified draft; ask the same model to choose an authorized
		// tool. No synthetic tool call, tool result or student message is created.
		retryOpts := append(append([]model.Option(nil), m.options(opts)...), model.WithToolChoice(schema.ToolChoiceForced))
		out, err = call(retryOpts...)
		if err != nil {
			return nil, err
		}
		err = validate(out)
	}
	return out, err
}
func (m *constrainedModel) Stream(ctx context.Context, ms []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	if m.adapter.committedSubmission != "" {
		return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("", nil)}), nil
	}
	for _, h := range m.adapter.Hooks {
		if h.ValidateModelOutput != nil {
			out, err := m.validated(ctx, ms, opts, func(options ...model.Option) (*schema.Message, error) {
				reader, err := m.BaseChatModel.Stream(ctx, ms, options...)
				if err != nil {
					return nil, err
				}
				defer reader.Close()
				var chunks []*schema.Message
				for {
					chunk, e := reader.Recv()
					if errors.Is(e, io.EOF) {
						break
					}
					if e != nil {
						return nil, e
					}
					chunks = append(chunks, chunk)
				}
				return schema.ConcatMessages(chunks)
			})
			if err != nil {
				return nil, err
			}
			return schema.StreamReaderFromArray([]*schema.Message{out}), nil
		}
	}
	return m.BaseChatModel.Stream(ctx, ms, m.options(opts)...)
}
