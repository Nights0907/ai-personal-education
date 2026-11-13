package state

import (
	"errors"
	"github.com/cloudwego/eino/schema"
	"testing"
)

func TestRestoreExactTerminal(t *testing.T) {
	o := &TerminalObservation{CallID: "original-id", ToolName: "commit", Arguments: `{"x":1}`, Result: `{ "result": "already committed" }`, SchemaVersion: "v1"}
	o.ArgumentsHash = Hash(o.Arguments)
	o.Hash = o.Digest()
	r := &Receipt{Terminal: o}
	ms, err := RestoreTerminal([]*schema.Message{schema.UserMessage("problem")}, r)
	if err != nil || len(ms) != 3 || ms[1].ToolCalls[0].ID != o.CallID || ms[2].Content != o.Result {
		t.Fatal(ms, err)
	}
	again, err := RestoreTerminal(ms, r)
	if err != nil || Hash(ms) != Hash(again) {
		t.Fatal("not idempotent", err)
	}
	ms[2].Content = "recreated result"
	if _, err = RestoreTerminal(ms, r); !errors.Is(err, ErrProtocol) {
		t.Fatal("changed result accepted", err)
	}
	gap := []*schema.Message{schema.UserMessage("problem"), schema.AssistantMessage("", []schema.ToolCall{{ID: "earlier", Function: schema.FunctionCall{Name: "read"}}})}
	if _, err = RestoreTerminal(gap, r); !errors.Is(err, ErrProtocol) {
		t.Fatal("earlier gap fabricated", err)
	}
}
