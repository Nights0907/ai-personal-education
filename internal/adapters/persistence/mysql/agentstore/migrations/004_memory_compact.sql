-- Append-only compaction versions; raw messages are never updated by compression.
CREATE TABLE IF NOT EXISTS agent_memory_compact (
 compact_id CHAR(64) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
 scope_key CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 branch_id CHAR(36) CHARACTER SET ascii NOT NULL,
 history_epoch BIGINT UNSIGNED NOT NULL,
 revision BIGINT UNSIGNED NOT NULL,
 through_turn_cursor BIGINT UNSIGNED NOT NULL,
 through_message_id CHAR(36) CHARACTER SET ascii NOT NULL,
 through_position BIGINT UNSIGNED NOT NULL,
 through_prefix_hash CHAR(64) CHARACTER SET ascii NOT NULL,
 content JSON NOT NULL,
 overview MEDIUMTEXT NOT NULL,
 summary_json JSON NOT NULL,
 source_token JSON NOT NULL,
 policy_json JSON NOT NULL,
 model_ref VARCHAR(255) NOT NULL,
 source_hash CHAR(64) CHARACTER SET ascii NOT NULL,
 created_at DATETIME(6) NOT NULL,
 UNIQUE KEY uk_compact_revision(scope_key,branch_id,history_epoch,revision),
 KEY idx_compact_history(scope_key,created_at,compact_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
