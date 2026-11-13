package context

import (
	"context"
	"errors"
	"github.com/cloudwego/eino/schema"
	"github.com/musi-ai/education/internal/agent/runtime/state"
	"strings"
	"testing"
)

func TestBuildUsesSummaryThenOnlyUncoveredFullTurns(t *testing.T) {
	spec := state.Spec{Instruction: "stable", Version: "v1", History: state.DefaultPolicy()}
	binding := state.Binding{SourceVersion: "v1", Input: schema.UserMessage("current {raw}")}
	turn := func(n int64, text string) state.Turn {
		return state.Turn{Cursor: n, Messages: []*schema.Message{schema.UserMessage(text), schema.AssistantMessage("feedback", nil)}}
	}
	view := state.View{Summary: state.Summary{Revision: 2, ThroughCursor: 1, Content: "historical summary"}, Turns: []state.Turn{turn(1, "covered-original"), turn(2, "recent-original")}}
	snap, e := (Builder{Counter: ConservativeCounter{}}).Build(context.Background(), state.Input{}, spec, binding, view)
	if e != nil {
		t.Fatal(e)
	}
	var all strings.Builder
	for _, m := range snap.Messages {
		all.WriteString(m.Content)
	}
	if strings.Contains(all.String(), "covered-original") || !strings.Contains(all.String(), "recent-original") || snap.HistoryCursors[0] != 2 {
		t.Fatal("coverage boundary incorrect")
	}
	if snap.Messages[len(snap.Messages)-1].Content != TextBlock("student-input", "current {raw}") {
		t.Fatal("input not last/raw")
	}
}
func TestBuildNeverTruncatesOverBudgetInput(t *testing.T) {
	spec := state.Spec{Instruction: "fixed", History: state.DefaultPolicy()}
	spec.History.MaxInputTokens = 200
	spec.History.OutputReserve = 100
	input := strings.Repeat("math条件", 500)
	snap, e := (Builder{Counter: ConservativeCounter{}}).Build(context.Background(), state.Input{}, spec, state.Binding{SourceVersion: "v", Input: schema.UserMessage(input)}, state.View{})
	if !errors.Is(e, state.ErrBudget) || snap.OriginalInput.Content != input {
		t.Fatal("silently dropped input", e)
	}
}
func TestBuildRejectsOrphanToolHistory(t *testing.T) {
	_, e := (Builder{Counter: ConservativeCounter{}}).Build(context.Background(), state.Input{}, state.Spec{Instruction: "s", History: state.DefaultPolicy()}, state.Binding{SourceVersion: "v", Input: schema.UserMessage("x")}, state.View{Turns: []state.Turn{{Cursor: 1, Messages: []*schema.Message{{Role: schema.Tool, ToolCallID: "orphan"}}}}})
	if !errors.Is(e, state.ErrProtocol) {
		t.Fatal(e)
	}
}
