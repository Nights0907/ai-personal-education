// Package modelstream carries request-scoped observers without coupling models
// to HTTP. Only public content is projected by the caller; reasoning is ignored.
package modelstream

import (
	"context"
	"github.com/cloudwego/eino/schema"
)

type Observer func() func(*schema.Message) error
type key struct{}

func WithObserver(ctx context.Context, o Observer) context.Context {
	return context.WithValue(ctx, key{}, o)
}
func ObserverFrom(ctx context.Context) Observer   { o, _ := ctx.Value(key{}).(Observer); return o }
func Without(ctx context.Context) context.Context { return WithObserver(ctx, nil) }
