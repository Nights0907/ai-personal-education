package agentstore

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/go-sql-driver/mysql"
	"github.com/musi-ai/education/internal/agent/runtime/state"
	"github.com/musi-ai/education/internal/agent/runtime/storage"
	"github.com/musi-ai/education/internal/agent/runtime/storage/contracttest"
	"github.com/musi-ai/education/internal/query"
	"os"
	"sync"
	"testing"
	"time"
)

var testDatabase struct {
	once      sync.Once
	mu        sync.Mutex
	db, admin *sql.DB
	name      string
	err       error
}

func TestMain(m *testing.M) {
	code := m.Run()
	if testDatabase.db != nil {
		testDatabase.db.Close()
	}
	if testDatabase.admin != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		_, err := testDatabase.admin.ExecContext(ctx, "DROP DATABASE "+testDatabase.name)
		cancel()
		if err != nil {
			fmt.Fprintln(os.Stderr, "disposable integration database cleanup failed")
			code = 1
		}
		testDatabase.admin.Close()
	}
	os.Exit(code)
}
func testStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("RUNTIME_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("set RUNTIME_TEST_MYSQL_DSN for isolated integration tests")
	}
	testDatabase.once.Do(func() {
		cfg, err := mysql.ParseDSN(dsn)
		if err != nil {
			testDatabase.err = err
			return
		}
		cfg.ParseTime = true
		cfg.Loc = time.UTC
		cfg.DBName = ""
		testDatabase.admin, err = sql.Open("mysql", cfg.FormatDSN())
		if err != nil {
			testDatabase.err = err
			return
		}
		testDatabase.name = "runtime_test_" + state.Hash(time.Now().String())[:20]
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		_, err = testDatabase.admin.ExecContext(ctx, "CREATE DATABASE "+testDatabase.name)
		if err != nil {
			testDatabase.err = err
			return
		}
		cfg.DBName = testDatabase.name
		testDatabase.db, err = sql.Open("mysql", cfg.FormatDSN())
		if err == nil {
			err = New(testDatabase.db).Migrate(ctx)
		}
		testDatabase.err = err
	})
	if testDatabase.err != nil {
		t.Fatal(testDatabase.err)
	}
	// Tests share only the disposable schema, never state. Serial cleanup avoids
	// creating/dropping three InnoDB tables for every contract subtest.
	testDatabase.mu.Lock()
	t.Cleanup(func() {
		defer testDatabase.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		for _, table := range []string{"agent_memory_compact", "agent_message", "agent_branch", "agent_conversation"} {
			if _, err := testDatabase.db.ExecContext(ctx, "DELETE FROM "+table); err != nil {
				t.Error(err)
			}
		}
	})
	return New(testDatabase.db)
}
func TestMySQLContract(t *testing.T) {
	contracttest.Run(t, func(t *testing.T) storage.Store { return testStore(t) })
}

// Existing storage contract fixtures deliberately use historical conversation
// IDs. Seed through Import; production Create requires a server-owned session.
type legacyQueryFixture struct{ *Queries }

func fixtureQueries(s *Store) *legacyQueryFixture { return &legacyQueryFixture{Queries: NewQueries(s)} }
func (q *legacyQueryFixture) Create(ctx context.Context, student string, in query.Input) (query.Record, bool, error) {
	return q.Import(ctx, student, in, time.Time{})
}
