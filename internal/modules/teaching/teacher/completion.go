// Package teacher coordinates task contracts for one teacher agent.
package teacher

import (
	"context"
	"github.com/cloudwego/eino/schema"
	"github.com/musi-ai/education/internal/agent/completion"
	"github.com/musi-ai/education/internal/agent/runtime/state"
	"github.com/musi-ai/education/internal/query"
)

type Completion struct {
	Queries   query.Repository
	Solutions completion.ReceiptResolver
	Store     completion.TurnReader
}

func (p Completion) policy(ctx context.Context, in state.Input) (completion.Policy, error) {
	r, e := p.Queries.Get(ctx, in.Identity.SubjectID, in.InputRef)
	if e != nil {
		return nil, e
	}
	if r.GuideQuestionID != "" || r.Mode == query.ReviewMode {
		return completion.Dialogue{Store: p.Store}, nil
	}
	return completion.Receipts{Resolver: p.Solutions}, nil
}
func (p Completion) Preflight(ctx context.Context, in state.Input) (*state.Receipt, error) {
	v, e := p.policy(ctx, in)
	if e != nil {
		return nil, e
	}
	return v.Preflight(ctx, in)
}
func (p Completion) Resolve(ctx context.Context, in state.Input, ms []*schema.Message, runErr error) (*state.Receipt, error) {
	v, e := p.policy(ctx, in)
	if e != nil {
		return nil, e
	}
	return v.Resolve(ctx, in, ms, runErr)
}
