CREATE TABLE IF NOT EXISTS student_profile_l0 (
 id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
 student_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL UNIQUE,
 revision BIGINT UNSIGNED NOT NULL DEFAULT 1,
 basic_json JSON NOT NULL,
 updated_at DATETIME(6) NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS student_memory_state (
 id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
 student_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL UNIQUE,
 handled_conversation_id BIGINT UNSIGNED NOT NULL DEFAULT 0,
 lease_token VARCHAR(36) CHARACTER SET ascii NOT NULL DEFAULT '',
 lease_until DATETIME(6) NOT NULL DEFAULT '1970-01-01',
 next_attempt_at DATETIME(6) NOT NULL DEFAULT '1970-01-01',
 last_reason VARCHAR(32) NOT NULL DEFAULT '',
 last_error VARCHAR(64) NOT NULL DEFAULT '',
 last_extracted_at DATETIME(6) NULL,
 updated_at DATETIME(6) NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

-- Durable source ledger/outbox: only pointers, never a second raw-message store.
CREATE TABLE IF NOT EXISTS student_memory_turn (
 id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
 student_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 source_message_id VARCHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL UNIQUE,
 scope_key CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 month CHAR(7) CHARACTER SET ascii NOT NULL,
 processed_at DATETIME(6) NULL,
 created_at DATETIME(6) NOT NULL,
 KEY idx_pending(student_id,processed_at,id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
CREATE TABLE IF NOT EXISTS student_memory_evidence (
 id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
 event_key CHAR(64) CHARACTER SET ascii NOT NULL UNIQUE,
 student_id VARCHAR(128) CHARACTER SET ascii NOT NULL,
 conversation_id VARCHAR(128) CHARACTER SET ascii NOT NULL,
 question_id VARCHAR(36) CHARACTER SET ascii NOT NULL,
 source_id VARCHAR(400) CHARACTER SET ascii NOT NULL,
 occurred_at DATETIME(6) NOT NULL,
 processed_at DATETIME(6) NULL,
 next_attempt_at DATETIME(6) NOT NULL DEFAULT '1970-01-01',
 KEY idx_evidence_due(processed_at,next_attempt_at,id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
