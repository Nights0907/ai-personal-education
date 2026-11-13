package context

import (
	"context"
	"github.com/cloudwego/eino/schema"
	"strings"
	"testing"
)

func TestSlotPriorityAndAttributeDeterminism(t *testing.T) {
	provider := func(name string, slot Slot, p int) TurnProvider {
		return TurnProvider{Name: name, Slot: slot, Priority: p, Provide: func(context.Context, BuildInput) ([]Block, error) {
			return []Block{{Tag: name, Content: "value", Attrs: map[string]string{"z": "2", "a": "1"}}}, nil
		}}
	}
	a, e := Assemble(context.Background(), BuildInput{}, []TurnProvider{provider("tail", SlotPreamble, 900), provider("dynamic", SlotUserPrefix, 1), provider("head", SlotPreamble, 100), provider("memory", SlotMemory, 100)}, nil, schema.UserMessage("student"))
	if e != nil {
		t.Fatal(e)
	}
	if len(a.Messages) != 3 || !strings.HasPrefix(a.Preamble, `<head a="1" z="2">`) || !strings.HasPrefix(a.Messages[2].Content, "<dynamic") || !strings.HasSuffix(a.Messages[2].Content, "student") {
		t.Fatal("slot ordering or stable rendering broken", a)
	}
	for _, p := range []TurnProvider{{Name: "bad", Slot: 99, Provide: provider("x", 0, 1).Provide}, {Name: "missing"}} {
		if _, e = Assemble(context.Background(), BuildInput{}, []TurnProvider{p}, nil, schema.UserMessage("x")); e == nil {
			t.Fatal("invalid provider accepted")
		}
	}
}
