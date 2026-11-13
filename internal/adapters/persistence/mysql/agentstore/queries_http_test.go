package agentstore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/musi-ai/education/internal/adapters/httpapi"
	"github.com/musi-ai/education/internal/agent/runtime/state"
	"github.com/musi-ai/education/internal/query"
	"github.com/musi-ai/education/web"
)

func TestQueryHTTPMySQLRoundTrip(t *testing.T) {
	s := testStore(t)
	db := s.DB
	ctx := context.Background()
	repo := fixtureQueries(s)
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer((httpapi.Server{Repo: repo, StudentID: "alice", Assets: web.Files}).Handler())
	defer server.Close()
	post := func(in query.Input) (int, query.Record) {
		t.Helper()
		b, _ := json.Marshal(in)
		res, e := http.Post(server.URL+"/api/v1/messages", "application/json", bytes.NewReader(b))
		if e != nil {
			t.Fatal(e)
		}
		defer res.Body.Close()
		var r query.Record
		_ = json.NewDecoder(res.Body).Decode(&r)
		return res.StatusCode, r
	}
	original := "  已知 f(x) = x² − 4x + 3，求最值。\n\\frac{x−1}{x+2} > 0；aₙ、α、🚀\n  "
	in := query.Input{Query: original, ConversationID: "conversation-1", ClientRequestID: "request-1"}
	status, first := post(in)
	if status != 201 || first.Query != original || first.Status != "RECEIVED" || first.ID == "" {
		t.Fatal("original query did not round-trip", status, first)
	}
	var conversations, branches, messages, assistants int
	for table, dest := range map[string]*int{"agent_conversation": &conversations, "agent_branch": &branches, "agent_message": &messages} {
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(dest); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM agent_message WHERE role='assistant'").Scan(&assistants); err != nil || conversations != 1 || branches != 1 || messages != 1 || assistants != 0 {
		t.Fatal("query did not create exactly three-table records", err, conversations, branches, messages, assistants)
	}
	headScope := state.Scope{Namespace: "teacher", SubjectID: "alice", ConversationID: in.ConversationID}
	v, err := s.View(ctx, headScope)
	if err != nil || len(v.Turns) != 0 {
		t.Fatal("pending input entered complete model history", v, err)
	}
	status, retry := post(in)
	if status != 200 || retry.ID != first.ID {
		t.Fatal("retry duplicated submission", status, retry)
	}
	changed := in
	changed.Query += "different"
	if status, _ = post(changed); status != 409 {
		t.Fatal("same key changed content", status)
	}
	res, err := http.Get(server.URL + "/api/v1/messages/" + first.ID)
	if err != nil {
		t.Fatal(err)
	}
	var fetched query.Record
	err = json.NewDecoder(res.Body).Decode(&fetched)
	res.Body.Close()
	if err != nil || res.StatusCode != 200 || fetched.Query != original {
		t.Fatal("GET did not read stored original", err, fetched)
	}
	other := httptest.NewServer((httpapi.Server{Repo: repo, StudentID: "bob", Assets: web.Files}).Handler())
	defer other.Close()
	res, err = http.Get(other.URL + "/api/v1/messages/" + first.ID)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 404 {
		t.Fatal("cross-student read", res.StatusCode)
	}
	for _, query := range []string{" \n\t", strings.Repeat("数", query.MaxQueryRunes+1)} {
		invalid := in
		invalid.Query = query
		invalid.ClientRequestID = uuid.NewString()
		if status, _ = post(invalid); status != 400 {
			t.Fatal("invalid query saved", status)
		}
	}
	// Concurrent retries must converge to exactly one immutable row.
	parallel := in
	parallel.ClientRequestID = "parallel"
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, _, e := repo.Create(context.Background(), "alice", parallel)
			if e != nil {
				errs <- e
			} else if r.Query != original {
				errs <- fmt.Errorf("changed original query")
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
	var count int
	if err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM agent_message WHERE subject_scope=?", repo.subject("alice")).Scan(&count); err != nil || count != 2 {
		t.Fatal("unexpected duplicate/invalid rows", count, err)
	}
	boundary := in
	boundary.ClientRequestID = "maximum-unicode-query"
	boundary.Query = strings.Repeat("🚀", query.MaxQueryRunes)
	if status, r := post(boundary); status != 201 || r.Query != boundary.Query {
		t.Fatal("valid maximum Unicode query was truncated", status)
	}
	// Browse multiple pages, returning only this student's records.
	res, err = http.Get(server.URL + "/api/v1/messages?limit=1")
	if err != nil {
		t.Fatal(err)
	}
	var page query.Page
	err = json.NewDecoder(res.Body).Decode(&page)
	res.Body.Close()
	if err != nil || len(page.Items) != 1 || page.NextCursor == "" {
		t.Fatal(page, err)
	}
	res, err = http.Get(server.URL + "/api/v1/messages?limit=1&before=" + url.QueryEscape(page.NextCursor))
	if err != nil {
		t.Fatal(err)
	}
	var next query.Page
	err = json.NewDecoder(res.Body).Decode(&next)
	res.Body.Close()
	if err != nil || len(next.Items) != 1 || next.Items[0].ID == page.Items[0].ID {
		t.Fatal("keyset pagination lost/duplicated data", next, err)
	}
	b, _ := json.Marshal(in)
	req, _ := http.NewRequest("POST", server.URL+"/api/v1/messages", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://untrusted.example")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 403 {
		t.Fatal("cross-site write accepted", res.StatusCode)
	}
	res, err = http.Get(server.URL + "/app.js")
	if err != nil {
		t.Fatal(err)
	}
	asset, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !bytes.Contains(asset, []byte("/api/v1/messages")) {
		t.Fatal("real frontend not served")
	}
}
