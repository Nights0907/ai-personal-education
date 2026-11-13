// Package completion defines replaceable turn completion policies. Business
// transactions may require receipts; ordinary dialogue completes on final text.
package completion

import (
	"context"
	"errors"
	"github.com/cloudwego/eino/schema"
	"github.com/musi-ai/education/internal/agent/runtime/state"
)

var ErrUnknown = errors.New("completion: outcome cannot be determined")

type Policy interface {
	Preflight(context.Context, state.Input) (*state.Receipt, error)
	Resolve(context.Context, state.Input, []*schema.Message, error) (*state.Receipt, error)
}
type ReceiptResolver interface {
	FindCommittedResult(context.Context, state.Input) (*state.Receipt, error)
}
type Receipts struct{ Resolver ReceiptResolver }

func (p Receipts) Preflight(ctx context.Context, in state.Input) (*state.Receipt, error) {
	return p.Resolver.FindCommittedResult(ctx, in)
}
func (p Receipts) Resolve(ctx context.Context, in state.Input, _ []*schema.Message, _ error) (*state.Receipt, error) {
	r, e := p.Resolver.FindCommittedResult(ctx, in)
	if e != nil {
		return nil, errors.Join(ErrUnknown, e)
	}
	return r, nil
}

type TurnReader interface {
	GetTurn(context.Context, state.Scope, string) (state.Turn, error)
}
type Dialogue struct{ Store TurnReader }

func receipt(in state.Input, run string) *state.Receipt {
	return &state.Receipt{Kind: "engine", ResultID: "turn-" + state.Hash([]string{in.Identity.Scope.Key(), in.Identity.TurnID})[:32], TurnID: in.Identity.TurnID, RunID: run}
}
func (p Dialogue) Preflight(ctx context.Context, in state.Input) (*state.Receipt, error) {
	t, e := p.Store.GetTurn(ctx, in.Identity.Scope, in.Identity.TurnID)
	if errors.Is(e, state.ErrNotFound) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	return receipt(in, t.RunID), nil
}
func (p Dialogue) Resolve(_ context.Context, in state.Input, ms []*schema.Message, runErr error) (*state.Receipt, error) {
	if runErr != nil {
		return nil, nil
	}
	if len(ms) < 2 || state.ValidateProtocol(ms) != nil {
		return nil, state.ErrIncomplete
	}
	last := ms[len(ms)-1]
	if last.Role != schema.Assistant || len(last.ToolCalls) > 0 || last.Content == "" {
		return nil, state.ErrIncomplete
	}
	return receipt(in, in.Identity.RunID), nil
}
