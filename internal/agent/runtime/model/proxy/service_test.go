package proxy

import (
	"context"
	"errors"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"testing"
)

type transport struct {
	seen  []*schema.Message
	tools int
}

func (t *transport) Generate(_ context.Context, ms []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	t.seen = ms
	t.tools = len(model.GetCommonOptions(nil, opts...).Tools)
	return schema.AssistantMessage("answer", nil), nil
}
func (*transport) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("unused")
}
func TestProxyForwardsFullContextAndImmutableTools(t *testing.T) {
	ctx := context.Background()
	inner := &transport{}
	service, e := NewService(map[string]model.BaseChatModel{"allowed": inner})
	if e != nil {
		t.Fatal(e)
	}
	base, e := service.Resolve(ctx, "allowed")
	if e != nil {
		t.Fatal(e)
	}
	bound, e := base.(model.ToolCallingChatModel).WithTools([]*schema.ToolInfo{{Name: "lookup"}})
	if e != nil {
		t.Fatal(e)
	}
	ms := []*schema.Message{schema.SystemMessage("fixed"), schema.UserMessage("history"), schema.AssistantMessage("past", nil), schema.UserMessage("current")}
	if _, e = bound.Generate(ctx, ms); e != nil || len(inner.seen) != 4 || inner.tools != 1 {
		t.Fatal("proxy lost history/tools", e)
	}
	_, _ = base.Generate(ctx, ms)
	if inner.tools != 0 {
		t.Fatal("base model tools were mutated")
	}
	if _, e = service.Resolve(ctx, "unapproved"); e == nil {
		t.Fatal("unapproved model accepted")
	}
}
