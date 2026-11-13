package state

import (
	"encoding/json"
	"fmt"
	"github.com/cloudwego/eino/schema"
)

// Positions are local to a branch. Never compare positions from different branches.
type MessageCursor struct {
	BranchID, MessageID, PrefixHash string
	Position                        int64
}
type ReadToken struct {
	ScopeKey, BranchID         string
	Tip                        MessageCursor
	WriteVersion, HistoryEpoch int64
}
type HistoryPage struct {
	Token ReadToken
	Turns []Turn
	Next  *MessageCursor
}

// TerminalObservation is frozen atomically with the business receipt. Result is
// the exact string returned by the tool, not a later re-serialization of it.
type TerminalObservation struct {
	CallID, ToolName, Arguments, ArgumentsHash, Result, SchemaVersion, Hash string
}

func (t TerminalObservation) Digest() string {
	t.Hash = ""
	return Hash(t)
}
func (t TerminalObservation) Validate() error {
	if t.CallID == "" || t.ToolName == "" || t.SchemaVersion == "" || !json.Valid([]byte(t.Result)) || !json.Valid([]byte(t.Arguments)) || t.ArgumentsHash != Hash(t.Arguments) || t.Hash != t.Digest() {
		return ErrProtocol
	}
	return nil
}

// RestoreTerminal fills only the original terminal call/result. Earlier tool
// gaps are rejected by ValidateProtocol; no call IDs or results are invented.
func RestoreTerminal(ms []*schema.Message, receipt *Receipt) ([]*schema.Message, error) {
	out := CleanMessages(ms)
	if receipt == nil || receipt.Terminal == nil {
		return out, nil
	}
	t := receipt.Terminal
	if err := t.Validate(); err != nil {
		return nil, err
	}
	call, result := false, false
	for _, m := range out {
		for _, c := range m.ToolCalls {
			if c.ID == t.CallID {
				if call || c.Function.Name != t.ToolName || c.Function.Arguments != t.Arguments {
					return nil, ErrProtocol
				}
				call = true
			}
		}
		if m.Role == schema.Tool && m.ToolCallID == t.CallID {
			if result || m.ToolName != t.ToolName || m.Content != t.Result {
				return nil, ErrProtocol
			}
			result = true
		}
	}
	if !call {
		if result || ValidateProtocol(out) != nil {
			return nil, fmt.Errorf("terminal prefix: %w", ErrProtocol)
		}
		out = append(out, schema.AssistantMessage("", []schema.ToolCall{{ID: t.CallID, Type: "function", Function: schema.FunctionCall{Name: t.ToolName, Arguments: t.Arguments}}}))
	}
	if !result {
		out = append(out, &schema.Message{Role: schema.Tool, ToolName: t.ToolName, ToolCallID: t.CallID, Content: t.Result})
	}
	return out, ValidateProtocol(out)
}
