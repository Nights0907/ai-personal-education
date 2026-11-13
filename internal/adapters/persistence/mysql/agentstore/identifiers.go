package agentstore

import (
	"context"
	"fmt"
	"strings"
)

// MySQL DDL commits implicitly. Inspect each table so interrupted migrations can
// resume without renumbering IDs or dropping legacy uniqueness constraints.
func (s *Store) migrateIdentifiers(ctx context.Context) error {
	b, err := migrations.ReadFile("migrations/005_identifiers.sql")
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(b), "\n") {
		stmt := strings.TrimSpace(line)
		if !strings.HasPrefix(stmt, "ALTER TABLE ") {
			continue
		}
		table := strings.Fields(stmt)[2]
		var count int
		if err = s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=? AND COLUMN_NAME='id'", table).Scan(&count); err != nil {
			return err
		}
		if count == 0 {
			if _, err = s.DB.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("identifier migration %s: %w", table, err)
			}
		}
		var extra, typ, key string
		if err = s.DB.QueryRowContext(ctx, "SELECT EXTRA,COLUMN_TYPE,COLUMN_KEY FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=? AND COLUMN_NAME='id'", table).Scan(&extra, &typ, &key); err != nil {
			return err
		}
		if !strings.Contains(extra, "auto_increment") || typ != "bigint unsigned" || key != "PRI" {
			return fmt.Errorf("%s.id must be an unsigned BIGINT AUTO_INCREMENT primary key", table)
		}
	}
	return nil
}
