// Package proxy is the boundary for a cloud model proxy. Runtime always submits
// full logical history. Provider-specific response IDs/caches belong to Client.
package proxy

import (
	"context"
	"errors"
	"github.com/cloudwego/eino/components/model"
	"github.com/musi-ai/education/internal/agent/adk"
)

type Client interface {
	Resolve(context.Context, string) (model.BaseChatModel, error)
}

func Resolver(client Client) adk.ModelResolver {
	return func(ctx context.Context, ref string) (model.BaseChatModel, error) {
		if client == nil {
			return nil, errors.New("proxy: model client required")
		}
		return client.Resolve(ctx, ref)
	}
}
