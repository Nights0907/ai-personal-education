// student-memory-reindex deterministically rebuilds L1 from each owned L2.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	documents "github.com/musi-ai/education/internal/adapters/persistence/redis/studentmemory"
	mem "github.com/musi-ai/education/internal/modules/student/memory"
	"github.com/musi-ai/education/internal/platform/localenv"
	"github.com/redis/go-redis/v9"
)

func main() {
	student := flag.String("student-id", "", "server-owned student ID to reindex")
	month := flag.String("month", "", "one YYYY-MM month; omit to rebuild all indexed months")
	backup := flag.String("backup-dir", "var/memory-reindex-backups", "private backup directory before rebuilding documents")
	flag.Parse()
	if _, err := mem.MonthlyPaths(*student, "2026-01"); err != nil {
		log.Fatal("valid -student-id required")
	}
	if err := localenv.Load(".env"); err != nil {
		log.Fatal("cannot load private configuration")
	}
	opts, err := redis.ParseURL(os.Getenv("MUSI_REDIS_URL"))
	if err != nil {
		log.Fatal("valid MUSI_REDIS_URL required")
	}
	opts.Protocol = 2
	opts.DisableIdentity = true
	client := redis.NewClient(opts)
	defer client.Close()
	s := mem.Service{Documents: &documents.Documents{Client: client}}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	months := []string{*month}
	if *month == "" {
		indexes, e := s.Documents.Indexes(ctx, *student, 120)
		if e != nil {
			log.Fatal(e)
		}
		months = nil
		for _, index := range indexes {
			months = append(months, index.Month)
		}
	}
	for _, m := range months {
		old, e := s.Documents.Load(ctx, *student, m)
		if e != nil {
			log.Fatal(e)
		}
		dir := filepath.Join(*backup, *student, fmt.Sprintf("%s-r%d-%d", m, old.Raw.Revision, time.Now().UnixNano()))
		if e = os.MkdirAll(dir, 0700); e != nil {
			log.Fatal("cannot create private backup")
		}
		for i, name := range []string{"l1.yaml", "l2.yaml", "l3.yaml"} {
			if e = os.WriteFile(filepath.Join(dir, name), []byte(old.Encoded[i]), 0600); e != nil {
				log.Fatal("cannot back up memory document")
			}
		}
		next, e := s.RebuildIndex(ctx, *student, m, time.Now().UTC())
		if e != nil {
			log.Fatal(e)
		}
		log.Printf("L1 rebuilt from L2: student=%s month=%s revision=%d keywords=%d", *student, m, next.Index.Revision, len(next.Index.Content.Keywords))
	}
}
