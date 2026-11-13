package httpapi

import (
	"context"
	"github.com/cloudwego/eino/schema"
	"github.com/musi-ai/education/internal/application/agentic"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSolveStreamDeduplicatesDraftSnapshotsAndProgress(t *testing.T) {
	w := httptest.NewRecorder()
	s := &solveStream{w: w, cancel: func() {}}
	ctx := context.Background()
	draft := agentic.Packet{Type: "draft", Data: map[string]any{"analysis": "解题内容"}}
	for i := 0; i < 2; i++ {
		if e := s.packet(ctx, draft); e != nil {
			t.Fatal(e)
		}
	}
	// The final tool hook adds identity fields to the same streamed snapshot.
	draft.BizName = "math_solve"
	draft.TurnID = "turn"
	if e := s.packet(ctx, draft); e != nil {
		t.Fatal(e)
	}
	progress := agentic.Packet{Type: "progress", Stage: "generating", Text: "正在生成"}
	s.packet(ctx, progress)
	s.packet(ctx, progress)
	s.send("draft_reset", map[string]bool{"provisional": true})
	s.packet(ctx, draft)
	body := w.Body.String()
	if strings.Count(body, "event: draft\n") != 2 || strings.Count(body, "event: progress\n") != 1 {
		t.Fatal(body)
	}
}
func TestObserverProjectsOnlyPublicDraftAndText(t *testing.T) {
	w := httptest.NewRecorder()
	s := &solveStream{w: w, cancel: func() {}}
	consume := s.observer()()
	index := 0
	consume(&schema.Message{Role: schema.Assistant, Content: "公开", ReasoningContent: "private-reasoning"})
	consume(&schema.Message{Role: schema.Assistant, Content: "说明"})
	consume(&schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{Index: &index, Function: schema.FunctionCall{Name: "validate_solution", Arguments: `{"candidate":{"question":{"stem":"private-stem"},"solution":{"analysis":"公开解析`}}}})
	body := w.Body.String()
	if strings.Contains(body, "private-") || !strings.Contains(body, "公开解析") || strings.Count(body, "event: text_delta\n") != 2 {
		t.Fatal(body)
	}
}

func TestTeachingNarrativeStreamsOnlyPublicFieldBeforeJSONCompletes(t *testing.T) {
	var events []string
	emit := func(kind string, data map[string]string) (string, error) {
		events = append(events, kind+":"+data["text"])
		return "", nil
	}
	chunk := &schema.Message{ToolCalls: []schema.ToolCall{{Function: schema.FunctionCall{Name: "submit_solution", Arguments: `{"candidate":{"solution":{"teacher_response":"先看等式两边`}}}}
	consume := teachingNarrativeObserver(true, func() bool { return false }, emit)()
	if err := consume(chunk); err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || !strings.Contains(events[0], "先看等式两边") || strings.Contains(events[0], "candidate") {
		t.Fatal("no safe incremental narrative", events)
	}
	events = nil
	hidden := teachingNarrativeObserver(false, func() bool { return false }, emit)()
	chunk.Content = "private draft"
	hidden(chunk)
	if len(events) != 0 {
		t.Fatal("hidden guide reference leaked", events)
	}
	public := teachingNarrativeObserver(false, func() bool { return true }, emit)()
	public(&schema.Message{Content: "你可以先解释为什么移项。"})
	if len(events) != 1 {
		t.Fatal("verified teaching feedback did not stream")
	}
}
