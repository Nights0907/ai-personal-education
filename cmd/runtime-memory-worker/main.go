// runtime-memory-worker consumes only durable summary jobs, never teaching jobs.
package main

import (
	"context"
	"database/sql"
	"errors"
	"github.com/go-sql-driver/mysql"
	"github.com/musi-ai/education/internal/adapters/model/bailian"
	store "github.com/musi-ai/education/internal/adapters/persistence/mysql/agentstore"
	"github.com/musi-ai/education/internal/agent/memory"
	ctxbuild "github.com/musi-ai/education/internal/agent/runtime/context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func run() error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	cfg, err := mysql.ParseDSN(os.Getenv("RUNTIME_MYSQL_DSN"))
	if err != nil {
		return err
	}
	if cfg.DBName == "" {
		return errors.New("RUNTIME_MYSQL_DSN must name the runtime database")
	}
	cfg.ParseTime = true
	cfg.Loc = time.UTC
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)
	probe, stop := context.WithTimeout(ctx, 10*time.Second)
	err = db.PingContext(probe)
	stop()
	if err != nil {
		return err
	}
	storeProbe, stopProbe := context.WithTimeout(ctx, 10*time.Second)
	err = store.New(db).ValidateSchema(storeProbe)
	stopProbe()
	if err != nil {
		return err
	}
	slots, _ := bailian.NewLimiter(2)
	m, err := bailian.New(ctx, bailian.Config{APIKey: os.Getenv("BAILIAN_API_KEY"), BaseURL: os.Getenv("BAILIAN_BASE_URL"), Model: os.Getenv("BAILIAN_CHAT_MODEL"), MaxOutputTokens: 2048, Timeout: 60 * time.Second}, slots)
	if err != nil {
		return err
	}
	manager := memory.Manager{ModelRef: os.Getenv("BAILIAN_CHAT_MODEL"), Store: store.New(db), Counter: ctxbuild.ConservativeCounter{}, Summarizer: bailian.Summarizer{Model: m}}
	host, _ := os.Hostname()
	worker := host + "-summary"
	timer := time.NewTicker(time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
			if _, err = manager.DispatchOne(ctx); err != nil {
				log.Printf("summary scheduling event pending retry (%T)", err)
			}
			if _, err = manager.ProcessOne(ctx, worker); err != nil {
				log.Printf("summary task failed; durable retry policy applied (%T)", err)
			}
		}
	}
}
func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
