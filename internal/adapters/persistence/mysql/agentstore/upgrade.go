package agentstore

import "context"

// Additive, repeatable upgrade of existing three-table installations. It keeps
// all sealed messages and does not create any fourth table.
func (s *Store) upgradePendingQueries(ctx context.Context) error {
	for name, ddl := range map[string]string{
		"status":            "ALTER TABLE agent_message ADD COLUMN status VARCHAR(24) CHARACTER SET ascii NOT NULL DEFAULT 'SEALED'",
		"subject_scope":     "ALTER TABLE agent_message ADD COLUMN subject_scope CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL",
		"client_request_id": "ALTER TABLE agent_message ADD COLUMN client_request_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NULL",
	} {
		var found int
		if err := s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='agent_message' AND COLUMN_NAME=?", name).Scan(&found); err != nil {
			return err
		}
		if found == 0 {
			if _, err := s.DB.ExecContext(ctx, ddl); err != nil {
				return err
			}
		}
	}
	var nullable string
	if err := s.DB.QueryRowContext(ctx, "SELECT IS_NULLABLE FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='agent_message' AND COLUMN_NAME='position'").Scan(&nullable); err != nil {
		return err
	}
	if nullable == "NO" {
		if _, err := s.DB.ExecContext(ctx, "ALTER TABLE agent_message MODIFY COLUMN position BIGINT UNSIGNED NULL"); err != nil {
			return err
		}
	}
	for _, name := range []string{"subject_id", "conversation_id"} {
		var length int
		if err := s.DB.QueryRowContext(ctx, "SELECT CHARACTER_MAXIMUM_LENGTH FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='agent_conversation' AND COLUMN_NAME=?", name).Scan(&length); err != nil {
			return err
		}
		if length < 128 {
			if _, err := s.DB.ExecContext(ctx, "ALTER TABLE agent_conversation MODIFY COLUMN "+name+" VARCHAR(128) NOT NULL"); err != nil {
				return err
			}
		}
	}
	for name, ddl := range map[string]string{
		"uk_query_request": "ALTER TABLE agent_message ADD UNIQUE KEY uk_query_request(subject_scope,client_request_id)",
		"idx_query_recent": "ALTER TABLE agent_message ADD KEY idx_query_recent(subject_scope,created_at,message_id)",
	} {
		var found int
		if err := s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.STATISTICS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='agent_message' AND INDEX_NAME=?", name).Scan(&found); err != nil {
			return err
		}
		if found == 0 {
			if _, err := s.DB.ExecContext(ctx, ddl); err != nil {
				return err
			}
		}
	}
	return nil
}
