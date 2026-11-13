package observability

import (
	"context"
	"errors"
	"github.com/musi-ai/education/internal/agent/adk/middleware"
	"testing"
)

func TestErrorClosesModelAndTurnWithSharedTrace(t *testing.T) {
	recorder := &Recorder{}
	tracer := NewTracer(recorder)
	ctx, turn := tracer.Start(context.Background(), "runtime.turn", nil)
	hooks := ModelHooks(tracer)
	in := &middleware.HookInput{Context: ctx}
	_ = hooks.BeforeModel(ctx, in)
	in.Err = errors.New("failed")
	_ = hooks.OnError(ctx, in)
	turn.End(in.Err)
	records := recorder.Records()
	if len(records) != 2 || records[0].Status != "error" || records[1].Status != "error" || records[0].ParentSpanID != records[1].SpanID || records[0].TraceID != records[1].TraceID {
		t.Fatal(records)
	}
}
