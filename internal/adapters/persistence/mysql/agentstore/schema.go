package agentstore

import (
	"context"
	"fmt"
)

func (s *Store) ValidateSchema(ctx context.Context) error {
	required := map[string][]string{
		"agent_memory_compact": {"id", "compact_id", "scope_key", "branch_id", "history_epoch", "revision", "content", "summary_json", "through_turn_cursor"},
		"agent_conversation":   {"id", "creation_request_id", "scope_key", "current_branch_id", "current_message_id", "current_position", "version", "history_epoch", "ext", "memory_due_at", "event_due_at"},
		"agent_message":        {"id", "scope_key", "message_id", "branch_id", "parent_message_id", "position", "version", "role", "content", "ext", "turn_cursor", "prefix_hash", "status", "subject_scope", "client_request_id"},
		"agent_branch":         {"id", "scope_key", "branch_id", "parent_branch_id", "fork_parent_position", "last_position"},
	}
	rows, err := s.DB.QueryContext(ctx, "SELECT TABLE_NAME,COLUMN_NAME FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME IN ('agent_conversation','agent_message','agent_branch','agent_memory_compact')")
	if err != nil {
		return err
	}
	defer rows.Close()
	seen := map[string]map[string]bool{}
	for rows.Next() {
		var table, column string
		if err = rows.Scan(&table, &column); err != nil {
			return err
		}
		if seen[table] == nil {
			seen[table] = map[string]bool{}
		}
		seen[table][column] = true
	}
	if err = rows.Err(); err != nil {
		return err
	}
	for table, columns := range required {
		for _, column := range columns {
			if !seen[table][column] {
				return fmt.Errorf("agent store: incompatible schema, missing %s.%s", table, column)
			}
		}
	}
	return nil
}

// RetireEmptyV2 is explicit maintenance after stopping old writers. It refuses
// all cleanup when any old table has rows. No business table is included.
func (s *Store) RetireEmptyV2(ctx context.Context) error {
	if err := s.ValidateSchema(ctx); err != nil {
		return err
	}
	known := []string{"agent_store_events", "agent_store_summary_jobs", "agent_store_compactions", "agent_store_messages", "agent_store_run_items", "agent_store_context_snapshots", "agent_store_runs", "agent_store_turns", "agent_store_branches", "agent_store_conversations"}
	var existing []string
	for _, table := range known {
		var found int
		if err := s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=?", table).Scan(&found); err != nil {
			return err
		}
		if found == 0 {
			continue
		}
		var count int64
		if err := s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
			return err
		}
		if count > 0 {
			return fmt.Errorf("agent store: refusing to retire nonempty table %s", table)
		}
		existing = append(existing, table)
	}
	for _, table := range existing {
		if _, err := s.DB.ExecContext(ctx, "DROP TABLE "+table); err != nil {
			return err
		}
	}
	return nil
}
