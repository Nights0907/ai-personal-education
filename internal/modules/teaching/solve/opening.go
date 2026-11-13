package solve

import "context"

type openingKey struct{}
type OpeningPublisher func(context.Context, string) (string, error)

func WithOpeningPublisher(ctx context.Context, publish OpeningPublisher) context.Context {
	return context.WithValue(ctx, openingKey{}, publish)
}
func openingPublisher(ctx context.Context) OpeningPublisher {
	p, _ := ctx.Value(openingKey{}).(OpeningPublisher)
	return p
}
