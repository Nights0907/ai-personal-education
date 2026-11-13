// runtime-db explicitly bootstraps message storage and versioned compaction.
package main

import (
	"context"
	"database/sql"
	"flag"
	"github.com/go-sql-driver/mysql"
	store "github.com/musi-ai/education/internal/adapters/persistence/mysql/agentstore"
	"github.com/musi-ai/education/internal/adapters/persistence/mysql/photos"
	studentmemory "github.com/musi-ai/education/internal/adapters/persistence/mysql/studentmemory"
	"github.com/musi-ai/education/internal/adapters/persistence/mysql/teaching"
	"github.com/musi-ai/education/internal/platform/localenv"
	"log"
	"os"
	"time"
)

func main() {
	if e := localenv.Load(".env"); e != nil {
		log.Fatal("cannot load local configuration")
	}
	timeout := flag.Duration("timeout", 5*time.Minute, "migration timeout")
	retire := flag.Bool("retire-empty-v2", false, "after stopping old writers, drop ten empty V2 tables; refuse if any has rows")
	flag.Parse()
	cfg, e := mysql.ParseDSN(os.Getenv("RUNTIME_MYSQL_DSN"))
	if e != nil || cfg.DBName == "" {
		log.Fatal("set RUNTIME_MYSQL_DSN with an existing database name")
	}
	cfg.ParseTime = true
	cfg.Loc = time.UTC
	db, e := sql.Open("mysql", cfg.FormatDSN())
	if e != nil {
		log.Fatal(e)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	if e = store.New(db).Migrate(ctx); e != nil {
		log.Fatal(e)
	}
	if e = teaching.New(db).Migrate(ctx); e != nil {
		log.Fatal(e)
	}
	if *retire {
		if e = store.New(db).RetireEmptyV2(ctx); e != nil {
			log.Fatal(e)
		}
	}
	if e = photos.New(db).Migrate(ctx); e != nil {
		log.Fatal(e)
	}
	if e = (&studentmemory.Repository{DB: db}).Migrate(ctx); e != nil {
		log.Fatal(e)
	}
	log.Printf("Database migration complete: database=%s; four Agent Store tables + seven teaching tables + three student memory tables", cfg.DBName)
}
