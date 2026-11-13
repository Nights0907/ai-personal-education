package agentic_test

import (
	"context"
	"errors"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	mem "github.com/musi-ai/education/internal/adapters/persistence/inmemory/agentstore"
	"github.com/musi-ai/education/internal/agent/runtime/state"
	"github.com/musi-ai/education/internal/agent/runtime/storage"
	"github.com/musi-ai/education/internal/application/agentic"
	"testing"
)

type failingSeal struct{ storage.Store }

func (s failingSeal) Finish(ctx context.Context, in state.Input, status, code string, r *state.Receipt, ms []*schema.Message) error {
	if status == "succeeded" {
		return errors.New("seal unavailable")
	}
	return s.Store.Finish(ctx, in, status, code, r, ms)
}
func TestOrdinaryDialogueDoesNotReportSuccessWhenSealFails(t *testing.T) {
	ctx := context.Background()
	store := failingSeal{mem.New()}
	m := &textModel{}
	p, e := agentic.New(ctx, agentic.Dependencies{Store: store, Models: func(context.Context, string) (model.BaseChatModel, error) { return m, nil }, Spec: spec()}, agentic.NewRegistry())
	if e != nil {
		t.Fatal(e)
	}
	scope := state.Scope{Namespace: "test", SubjectID: "alice", ConversationID: "seal-failure"}
	turn, e := p.Run(ctx, agentic.Request{Input: state.Input{Identity: state.Identity{Scope: scope, TurnID: "t"}}, Query: "q"}, agentic.SinkFunc(func(context.Context, agentic.Packet) error { return nil }))
	if e == nil || turn.Result.Status == "succeeded" || turn.Result.ErrorCode != "STORE_SEAL_FAILED" {
		t.Fatal("lost history reported as success", turn.Result, e)
	}
	if _, e = store.GetTurn(ctx, scope, "t"); !errors.Is(e, state.ErrNotFound) {
		t.Fatal("failed seal exposed history", e)
	}
}
func TestProjectionFailureCancelsSourceAndSkipsPostRuntime(t *testing.T) {
	ctx := context.Background()
	m := &textModel{}
	post := false
	p, e := agentic.New(ctx, agentic.Dependencies{Store: mem.New(), Models: func(context.Context, string) (model.BaseChatModel, error) { return m, nil }, Spec: spec()}, agentic.NewRegistry(agentic.RegistrationFunc(func(context.Context, *agentic.Turn) (agentic.Contribution, error) {
		return agentic.Contribution{PostRuntimeHooks: []func(context.Context, *agentic.Turn) error{func(context.Context, *agentic.Turn) error { post = true; return nil }}}, nil
	})))
	if e != nil {
		t.Fatal(e)
	}
	_, e = p.Run(ctx, agentic.Request{Input: state.Input{Identity: state.Identity{Scope: state.Scope{Namespace: "t", SubjectID: "a", ConversationID: "c"}, TurnID: "t"}}, Query: "q"}, agentic.SinkFunc(func(context.Context, agentic.Packet) error { return errors.New("client disconnected") }))
	if e == nil || post {
		t.Fatal("failed projection ran post-runtime hook", e, post)
	}
}
