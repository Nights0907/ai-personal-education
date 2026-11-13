// server is the local web + query-persistence entry point. No AI run is started.
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/musi-ai/education/internal/adapters/httpapi"
	"github.com/musi-ai/education/internal/adapters/persistence/mysql/agentstore"
	"github.com/musi-ai/education/internal/adapters/persistence/mysql/teachingtasks"
	"github.com/musi-ai/education/internal/modules/teaching/tasks"
	"github.com/musi-ai/education/internal/platform/localenv"
	"github.com/musi-ai/education/internal/query"
	"github.com/musi-ai/education/web"
)

func run() error {
	if e := localenv.Load(".env"); e != nil {
		return errors.New("cannot load local configuration")
	}
	addr := flag.String("addr", "127.0.0.1:8080", "local HTTP listen address")
	student := flag.String("student-id", "local-pilot", "server-owned local development student identity")
	flag.Parse()
	host, _, err := net.SplitHostPort(*addr)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return errors.New("local query server must listen on a loopback address")
	}
	if !query.ValidID(*student) {
		return errors.New("invalid local student identity")
	}
	cfg, err := mysql.ParseDSN(os.Getenv("RUNTIME_MYSQL_DSN"))
	if err != nil || cfg.DBName == "" {
		return errors.New("set RUNTIME_MYSQL_DSN with an existing database name")
	}
	cfg.ParseTime = true
	cfg.Loc = time.UTC
	cfg.ClientFoundRows = false
	cfg.Timeout = 5 * time.Second
	cfg.ReadTimeout = 15 * time.Second
	cfg.WriteTimeout = 15 * time.Second
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	probe, cancel := context.WithTimeout(ctx, 10*time.Second)
	err = db.PingContext(probe)
	if err == nil {
		err = agentstore.New(db).ValidateSchema(probe)
	}
	cancel()
	if err != nil {
		return errors.New("database unavailable or Agent Store schema missing; run cmd/runtime-db first")
	}
	studentMemory, closeStudentMemory, err := buildStudentMemory(ctx, db)
	if err != nil {
		return err
	}
	defer closeStudentMemory()
	pipeline, teachingService, recallService, err := buildTeacher(ctx, db, studentMemory)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS radar_candidate(student_id VARCHAR(128) NOT NULL,source_id VARCHAR(128) NOT NULL,seed INT NOT NULL,candidate LONGTEXT NOT NULL,verified BOOLEAN NOT NULL DEFAULT false,verification LONGTEXT,PRIMARY KEY(student_id,source_id,seed))`); err != nil {
		return err
	}
	taskRepo := &teachingtasks.Repository{DB: db}
	if err := taskRepo.Migrate(ctx); err != nil {
		return err
	}
	taskService := &tasks.Service{Repo: taskRepo, Wake: make(chan struct{}, 1)}
	accounts := &httpapi.Accounts{DB: db}
	if err := accounts.Migrate(); err != nil {
		return err
	}
	api := httpapi.Server{Accounts: accounts, StudentMemory: studentMemory, Photos: teachingService.Photos, Agentic: pipeline, Teaching: teachingService, RecallStatus: recallService, Repo: agentstore.NewQueries(agentstore.New(db)), StudentID: *student, Assets: web.Files, Tasks: taskService}
	taskService.Execute = api.ExecuteTeachingTask
	if pipeline != nil {
		for i := 0; i < 2; i++ {
			go taskService.Run(ctx)
		}
	}
	server := &http.Server{Handler: api.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 20 * time.Second, IdleTimeout: 60 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	log.Printf("query web ready: http://%s (local student: %s)", listener.Addr(), *student)
	err = server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
