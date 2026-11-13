CREATE TABLE IF NOT EXISTS teaching_task (
 id VARCHAR(36) CHARACTER SET ascii NOT NULL PRIMARY KEY,
 student_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 conversation_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 client_request_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 input_json JSON NOT NULL,
 input_hash CHAR(64) CHARACTER SET ascii NOT NULL,
 parent_id VARCHAR(36) CHARACTER SET ascii NOT NULL DEFAULT '',
 status VARCHAR(16) NOT NULL DEFAULT 'QUEUED',
 attempts INT NOT NULL DEFAULT 0,
 lease_token VARCHAR(36) CHARACTER SET ascii NOT NULL DEFAULT '',
 lease_until DATETIME(6) NOT NULL DEFAULT '1970-01-01',
 opening_text TEXT NOT NULL,
 result_json JSON NULL,
 error_text TEXT NOT NULL,
 created_at DATETIME(6) NOT NULL,
 updated_at DATETIME(6) NOT NULL,
 UNIQUE KEY uk_task_request(student_id,client_request_id),
 KEY idx_task_work(status,lease_until,created_at),
 KEY idx_task_conversation(student_id,conversation_id,created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
CREATE TABLE IF NOT EXISTS teaching_task_event (
 id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
 task_id VARCHAR(36) CHARACTER SET ascii NOT NULL,
 event_key VARCHAR(36) CHARACTER SET ascii NOT NULL,
 kind VARCHAR(32) CHARACTER SET ascii NOT NULL,
 data_json JSON NOT NULL,
 created_at DATETIME(6) NOT NULL,
 UNIQUE KEY uk_task_event(task_id,event_key),
 KEY idx_task_replay(task_id,id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
