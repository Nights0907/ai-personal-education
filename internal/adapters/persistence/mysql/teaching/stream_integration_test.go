package teaching

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"github.com/musi-ai/education/internal/query"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestSolveSSEFlushesEarlyCommitsAndReplays(t *testing.T) {
	db := dbForTest(t)
	gate := make(chan struct{})
	m := &testModel{wait: gate}
	_, server, q := setup(t, db, m, &testModel{review: true})
	c, e := q.CreateConversation(context.Background(), "alice", "sse")
	if e != nil {
		t.Fatal(e)
	}
	input := query.Input{Query: "解方程x+1=2。", ConversationID: c.ConversationID, ClientRequestID: "stream"}
	body, _ := json.Marshal(input)
	request := func() *http.Response {
		t.Helper()
		req, _ := http.NewRequest("POST", server.URL+"/api/v1/solve", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")
		client := &http.Client{Timeout: 15 * time.Second}
		response, e := client.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		if response.StatusCode != 200 || !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
			t.Fatal(response.Status)
		}
		return response
	}
	response := request()
	reader := bufio.NewReader(response.Body)
	first := ""
	for {
		line, e := reader.ReadString('\n')
		if e != nil {
			t.Fatal(e)
		}
		first += line
		if line == "\n" {
			break
		}
	}
	if !strings.Contains(first, "event: accepted") {
		t.Fatal(first)
	}
	var n int
	if e = db.QueryRow("SELECT COUNT(*) FROM teaching_question").Scan(&n); e != nil || n != 0 {
		t.Fatal("committed before model", n, e)
	}
	close(gate)
	rest, e := io.ReadAll(reader)
	response.Body.Close()
	if e != nil {
		t.Fatal(e)
	}
	for _, want := range []string{"event: progress", "event: draft", "event: validation", "event: solution", "event: done", "succeeded"} {
		if !strings.Contains(string(rest), want) {
			t.Fatalf("missing %s: %s", want, rest)
		}
	}
	calls := m.calls.Load()
	replay := request()
	raw, e := io.ReadAll(replay.Body)
	replay.Body.Close()
	if e != nil || m.calls.Load() != calls || !strings.Contains(string(raw), "event: solution") || strings.Contains(string(raw), "event: draft") {
		t.Fatal("replay reran model", e, string(raw))
	}
}
func TestSolveSSEFailureHasNoSuccessfulResult(t *testing.T) {
	db := dbForTest(t)
	_, server, q := setup(t, db, &testModel{}, &testModel{review: true, reject: true})
	c, e := q.CreateConversation(context.Background(), "alice", "sse-failure")
	if e != nil {
		t.Fatal(e)
	}
	body, _ := json.Marshal(query.Input{Query: "解方程x+1=2。", ConversationID: c.ConversationID, ClientRequestID: "stream-failure"})
	req, _ := http.NewRequest("POST", server.URL+"/api/v1/solve", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	response, e := http.DefaultClient.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	raw, e := io.ReadAll(response.Body)
	response.Body.Close()
	if e != nil || !strings.Contains(string(raw), "event: error") || !strings.Contains(string(raw), `"status":"failed"`) || strings.Contains(string(raw), "event: solution") {
		t.Fatal(string(raw), e)
	}
	var n int
	if e = db.QueryRow("SELECT COUNT(*) FROM teaching_question").Scan(&n); e != nil || n != 0 {
		t.Fatal("failed draft committed", n, e)
	}
}
