package agentstore

import (
	"context"
	"errors"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/musi-ai/education/internal/adapters/httpapi"
	"github.com/musi-ai/education/internal/agent/runtime/state"
	"github.com/musi-ai/education/internal/application/agentic"
	"github.com/musi-ai/education/web"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type dialogueModel struct{ calls int }

func (m *dialogueModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	m.calls++
	return schema.AssistantMessage("测试答案：x=1", nil), nil
}
func (*dialogueModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("not used")
}
func TestNewGraphHTTPToThreeTablesAndReplay(t *testing.T) {
	s := testStore(t)
	q := fixtureQueries(s)
	m := &dialogueModel{}
	spec := state.Spec{Name: "test", Version: "v1", Instruction: "test", ModelRef: "fake", MaxIterations: 3, MaxToolCalls: 3, Timeout: time.Minute, History: state.DefaultPolicy()}
	pipeline, e := agentic.New(context.Background(), agentic.Dependencies{Store: s, Models: func(context.Context, string) (model.BaseChatModel, error) { return m, nil }, Spec: spec}, agentic.NewRegistry())
	if e != nil {
		t.Fatal(e)
	}
	server := httptest.NewServer((httpapi.Server{Repo: q, StudentID: "alice", Assets: web.Files, Agentic: pipeline}).Handler())
	defer server.Close()
	body := `{"query":"  x+1=2\n","conversation_id":"new-graph","client_request_id":"request-1"}`
	for i := 0; i < 2; i++ {
		response, e := http.Post(server.URL+"/api/v1/agentic/chat", "application/json", strings.NewReader(body))
		if e != nil {
			t.Fatal(e)
		}
		output, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != 200 || !strings.Contains(string(output), "event: text") || !strings.Contains(string(output), "测试答案") || !strings.Contains(string(output), `"status":"succeeded"`) {
			t.Fatalf("bad SSE status %d: %s", response.StatusCode, output)
		}
	}
	if m.calls != 1 {
		t.Fatal("HTTP retry executed the model again", m.calls)
	}
	records, _, e := q.List(context.Background(), "alice", "new-graph", nil, 10)
	if e != nil || len(records) != 1 || records[0].Query != "  x+1=2\n" || records[0].Status != "ANSWERED" {
		t.Fatal(records, e)
	}
	var count int
	if e = s.DB.QueryRow("SELECT COUNT(*) FROM agent_message").Scan(&count); e != nil || count != 2 {
		t.Fatal("expected one sealed user and assistant", count, e)
	}
}
