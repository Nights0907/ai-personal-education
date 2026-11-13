package runtime

import (
	"context"
	"github.com/cloudwego/eino/schema"
	"github.com/musi-ai/education/internal/agent/runtime/state"
	"sync"
)

type TurnEvent struct {
	Type      string          `json:"type"`
	Message   *schema.Message `json:"message,omitempty"`
	Result    *state.Result   `json:"result,omitempty"`
	ErrorCode string          `json:"error_code,omitempty"`
}
type liveKey struct{}
type liveSink func(TurnEvent)

func emitLive(ctx context.Context, e TurnEvent) {
	if emit, ok := ctx.Value(liveKey{}).(liveSink); ok {
		emit(e)
	}
}

type EventSource struct {
	events <-chan TurnEvent
	done   <-chan struct{}
	cancel context.CancelFunc
	once   sync.Once
	result state.Result
	err    error
}

// StreamTurn starts a bounded, message-level live source. Close cancels execution;
// consuming through EOF guarantees storage and lifecycle finalization completed.
func (r *Runtime) StreamTurn(ctx context.Context, in state.Input) *EventSource {
	ctx, cancel := context.WithCancel(ctx)
	events := make(chan TurnEvent, 16)
	done := make(chan struct{})
	s := &EventSource{events: events, done: done, cancel: cancel}
	emit := liveSink(func(e TurnEvent) {
		if e.Result != nil {
			v := *e.Result
			e.Result = &v
		}
		select {
		case events <- e:
		case <-ctx.Done():
		}
	})
	go func() {
		defer close(done)
		defer close(events)
		defer cancel()
		s.result, s.err = r.RunTurn(context.WithValue(ctx, liveKey{}, emit), in)
		if s.err != nil {
			emit(TurnEvent{Type: "error", ErrorCode: errorCode(s.err)})
		}
	}()
	return s
}
func (s *EventSource) Next(ctx context.Context) (TurnEvent, bool) {
	select {
	case e, ok := <-s.events:
		return e, ok
	case <-ctx.Done():
		s.Close()
		return TurnEvent{}, false
	}
}
func (s *EventSource) Close() { s.once.Do(s.cancel) }
func (s *EventSource) Result(ctx context.Context) (state.Result, error) {
	select {
	case <-s.done:
		return s.result, s.err
	case <-ctx.Done():
		return state.Result{}, ctx.Err()
	}
}
