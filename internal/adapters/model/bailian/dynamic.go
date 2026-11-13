package bailian

import (
	"context"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	settings "github.com/musi-ai/education/internal/platform/modelsettings"
)

type Dynamic struct {
	Role    string
	Default Config
	Slots   *Limiter
}

func (d *Dynamic) client(ctx context.Context) (model.BaseChatModel, error) {
	settings.Mu.Lock()
	v, e := settings.Read()
	settings.Mu.Unlock()
	if e != nil {
		return nil, e
	}
	c := d.Default
	if s, ok := v[d.Role]; ok {
		c.APIKey = s.APIKey
		c.BaseURL = s.BaseURL
		c.Model = s.Model
	}
	return New(ctx, c, d.Slots)
}
func (d *Dynamic) Generate(ctx context.Context, ms []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	m, e := d.client(ctx)
	if e != nil {
		return nil, e
	}
	return m.Generate(ctx, ms, opts...)
}
func (d *Dynamic) Stream(ctx context.Context, ms []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m, e := d.client(ctx)
	if e != nil {
		return nil, e
	}
	return m.Stream(ctx, ms, opts...)
}
