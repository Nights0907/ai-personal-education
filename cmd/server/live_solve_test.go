package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/go-sql-driver/mysql"
	"github.com/musi-ai/education/internal/agent/runtime/state"
	"github.com/musi-ai/education/internal/application/agentic"
	"github.com/musi-ai/education/internal/platform/localenv"
	"github.com/musi-ai/education/internal/query"
	"os"
	"strings"
	"testing"
)

// Explicitly opt in: this calls the configured provider using a local synthetic
// fixture. It is never run by the ordinary test suite.
func TestLiveSolve(t *testing.T) {
	if os.Getenv("RUNTIME_LIVE_SOLVE_INPUT") == "" {
		t.Skip("explicit live fixture required")
	}
	t.Chdir("../..")
	if e := localenv.Load(".env"); e != nil {
		t.Fatal(e)
	}
	raw, e := os.ReadFile(os.Getenv("RUNTIME_LIVE_SOLVE_INPUT"))
	if e != nil {
		t.Fatal(e)
	}
	var input query.Input
	if json.Unmarshal(raw, &input) != nil {
		t.Fatal("invalid fixture")
	}
	cfg, e := mysql.ParseDSN(os.Getenv("RUNTIME_MYSQL_DSN"))
	if e != nil {
		t.Fatal(e)
	}
	cfg.ParseTime = true
	db, e := sql.Open("mysql", cfg.FormatDSN())
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pipeline, service, _, e := buildTeacher(ctx, db)
	if e != nil {
		t.Fatal(e)
	}
	record, _, e := service.Queries.Create(ctx, "local-pilot", input)
	if e != nil {
		t.Fatal(e)
	}
	turn, e := pipeline.Run(ctx, agentic.Request{Query: record.Query, Input: state.Input{Identity: state.Identity{Scope: state.Scope{Namespace: "teacher", SubjectID: "local-pilot", ConversationID: record.ConversationID}, TurnID: record.TurnID}, InputRef: record.ID, BindingRef: record.ID}}, agentic.SinkFunc(func(_ context.Context, p agentic.Packet) error {
		if p.Type == "validation" {
			t.Logf("validation feedback: %+v", p.Data)
		}
		return nil
	}))
	if e != nil {
		message := e.Error()
		if key := os.Getenv("BAILIAN_API_KEY"); key != "" {
			message = strings.ReplaceAll(message, key, "[redacted]")
		}
		t.Fatal(message)
	}
	if turn.Result.Status != "succeeded" {
		t.Fatal(turn.Result)
	}
	t.Log("live solve committed", turn.Result.CommittedResult)
}
