package noop

import (
	"context"
	"github.com/musi-ai/education/internal/agent/runtime/event"
)

type Producer struct{}

func (Producer) Emit(context.Context, event.Event) error { return nil }
