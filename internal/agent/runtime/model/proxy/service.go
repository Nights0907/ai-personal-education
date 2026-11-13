package proxy

import (
	"context"
	"errors"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// Service is an in-process model proxy. Its allowlisted transports may be a
// Bailian Eino model or an RPC adapter. Full history is always forwarded.
type Service struct {
	models map[string]model.BaseChatModel
}

func NewService(models map[string]model.BaseChatModel) (*Service, error) {
	s := &Service{models: map[string]model.BaseChatModel{}}
	for ref, m := range models {
		if ref == "" || m == nil {
			return nil, errors.New("proxy: invalid model registration")
		}
		s.models[ref] = m
	}
	if len(s.models) == 0 {
		return nil, errors.New("proxy: at least one model required")
	}
	return s, nil
}
func (s *Service) Resolve(ctx context.Context, ref string) (model.BaseChatModel, error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	m, ok := s.models[ref]
	if !ok {
		return nil, errors.New("proxy: model is not allowlisted")
	}
	return &chatModel{inner: m}, nil
}

type chatModel struct {
	inner model.BaseChatModel
	tools []*schema.ToolInfo
}

var _ model.ToolCallingChatModel = (*chatModel)(nil)

func (m *chatModel) WithTools(ts []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return &chatModel{inner: m.inner, tools: append([]*schema.ToolInfo(nil), ts...)}, nil
}
func (m *chatModel) Generate(ctx context.Context, ms []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	if m.tools != nil {
		opts = append(append([]model.Option(nil), opts...), model.WithTools(m.tools))
	}
	return m.inner.Generate(ctx, ms, opts...)
}
func (m *chatModel) Stream(ctx context.Context, ms []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	if m.tools != nil {
		opts = append(append([]model.Option(nil), opts...), model.WithTools(m.tools))
	}
	return m.inner.Stream(ctx, ms, opts...)
}
