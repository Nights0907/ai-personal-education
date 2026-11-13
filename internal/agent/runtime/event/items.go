package event

import (
	"github.com/cloudwego/eino/schema"
	"github.com/musi-ai/education/internal/agent/runtime/state"
)

func ItemsForTurn(scope state.Scope, t state.Turn) []Item {
	items := make([]Item, 0, len(t.Messages))
	for index, m := range state.CleanMessages(t.Messages) {
		kind := "assistant_text"
		role := "assistant"
		position := t.End.Position
		if m.Role == schema.User {
			kind = "user_message"
			role = "user"
			position--
		} else if m.Role == schema.Tool {
			kind = "tool_result"
		} else if len(m.ToolCalls) > 0 {
			kind = "tool_call"
		}
		id := state.MessageID(scope, t.TurnID, role)
		items = append(items, Item{Index: index, MessageID: id, Seq: position, Type: kind, Role: m.Role, Content: m.Content, ToolCallID: m.ToolCallID, ToolCalls: m.ToolCalls})
	}
	return items
}
