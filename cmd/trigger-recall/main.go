// Command trigger-recall triggers the forgetting-prevention plan for a student:
// EnsurePlan (auto-enroll) then Step (daily delivery), using the real scheduler
// with a fallback (no model) opening.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"time"

	"github.com/go-sql-driver/mysql"
	store "github.com/musi-ai/education/internal/adapters/persistence/mysql/agentstore"
	recallplanmysql "github.com/musi-ai/education/internal/adapters/persistence/mysql/recallplan"
	"github.com/musi-ai/education/internal/adapters/persistence/mysql/teaching"
	redisdocs "github.com/musi-ai/education/internal/adapters/persistence/redis/studentmemory"
	sqlmemory "github.com/musi-ai/education/internal/adapters/persistence/mysql/studentmemory"
	mem "github.com/musi-ai/education/internal/modules/student/memory"
	rp "github.com/musi-ai/education/internal/modules/teaching/recallplan"
	"github.com/redis/go-redis/v9"
)

func main() {
	student := os.Args[1]
	dsn := os.Getenv("RUNTIME_MYSQL_DSN")
	url := os.Getenv("MUSI_REDIS_URL")
	cfg, _ := mysql.ParseDSN(dsn)
	cfg.ParseTime = true
	cfg.Loc = time.UTC
	db, _ := sql.Open("mysql", cfg.FormatDSN())
	ctx := context.Background()
	opts, _ := redis.ParseURL(url)
	opts.Protocol = 2
	opts.DisableIdentity = true
	client := redis.NewClient(opts)

	memRepo := &sqlmemory.Repository{DB: db}
	docs := &redisdocs.Documents{Client: client}
	memSvc := &mem.Service{Repo: memRepo, Documents: docs}
	repo := teaching.New(db)
	queries := store.NewQueries(store.New(db))
	rpRepo := &recallplanmysql.Repository{DB: db}
	if err := rpRepo.Migrate(ctx); err != nil {
		panic(err)
	}
	svc := &rp.Service{
		Repo:          rpRepo,
		Recall:        rp.MemoryRecall{Service: memSvc},
		Questions:     rp.SolveQuestions{Repo: repo},
		Conversations: rp.AgentStoreSink{Queries: queries},
		Model:         nil, // fallback opening, no LLM
		SendHour:      0,   // ensure DuePlans matches now.Hour() for the demo
	}

	now := time.Now()
	// Auto-enroll
	if err := svc.EnsurePlan(ctx, student, now); err != nil {
		fmt.Println("EnsurePlan:", err)
		return
	}
	// Deliver today's question (force send_hour to be satisfied: use current hour)
	if _, err := svc.Step(ctx, now); err != nil {
		fmt.Println("Step:", err)
		return
	}
	st := svc.Status(ctx, student)
	fmt.Printf("plan status: has_plan=%v quota=%d send_hour=%d last_run=%s pending=%d sent=%d reviewed=%d\n",
		st.HasPlan, st.DailyQuota, st.SendHour, st.LastRunDate, st.Pending, st.Sent, st.Reviewed)
}
