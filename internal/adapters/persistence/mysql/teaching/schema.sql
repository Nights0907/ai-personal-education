CREATE TABLE IF NOT EXISTS teaching_question (
 id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
 question_id VARCHAR(36) CHARACTER SET ascii NOT NULL UNIQUE,
 student_id VARCHAR(128) NOT NULL,
 scope_key CHAR(64) CHARACTER SET ascii NOT NULL,
 turn_id VARCHAR(128) CHARACTER SET ascii NOT NULL,
 source_message_id VARCHAR(36) CHARACTER SET ascii NOT NULL,
 original_text MEDIUMTEXT NOT NULL,
 source_hash CHAR(64) CHARACTER SET ascii NOT NULL,
 input_type VARCHAR(16) NOT NULL,
 source_image_object_key VARCHAR(1024) NULL,
 ocr_result_object_key VARCHAR(1024) NULL,
 question_json JSON NOT NULL,
 created_at DATETIME(6) NOT NULL,
 UNIQUE KEY uk_question_turn(scope_key,turn_id),
 KEY idx_student(student_id,created_at,question_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
CREATE TABLE IF NOT EXISTS teaching_solution (
 id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
 solution_id VARCHAR(36) CHARACTER SET ascii NOT NULL UNIQUE,
 question_id VARCHAR(36) CHARACTER SET ascii NOT NULL UNIQUE,
 answer JSON NOT NULL,
 analysis MEDIUMTEXT NOT NULL,
 knowledge_points JSON NOT NULL,
 model_ref VARCHAR(255) NOT NULL,
 review_status VARCHAR(64) NOT NULL,
 schema_version VARCHAR(64) NOT NULL,
 created_at DATETIME(6) NOT NULL,
 CONSTRAINT fk_teaching_solution_question FOREIGN KEY(question_id) REFERENCES teaching_question(question_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
CREATE TABLE IF NOT EXISTS teaching_solution_step (
 id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
 step_id VARCHAR(36) CHARACTER SET ascii NOT NULL UNIQUE,
 solution_id VARCHAR(36) CHARACTER SET ascii NOT NULL,
 ordinal INT UNSIGNED NOT NULL,
 subquestion_id VARCHAR(64) NULL,
 title VARCHAR(800) NOT NULL,
 content MEDIUMTEXT NOT NULL,
 UNIQUE KEY uk_solution_step(solution_id,ordinal),
 CONSTRAINT fk_teaching_step_solution FOREIGN KEY(solution_id) REFERENCES teaching_solution(solution_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
CREATE TABLE IF NOT EXISTS teaching_solution_draft (
 id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
 draft_id VARCHAR(36) CHARACTER SET ascii NOT NULL UNIQUE,
 scope_key CHAR(64) CHARACTER SET ascii NOT NULL,
 turn_id VARCHAR(128) CHARACTER SET ascii NOT NULL,
 run_id VARCHAR(128) CHARACTER SET ascii NOT NULL,
 lease_token BIGINT NOT NULL,
 source_hash CHAR(64) CHARACTER SET ascii NOT NULL,
 payload JSON NOT NULL,
 payload_hash CHAR(64) CHARACTER SET ascii NOT NULL,
 review_json JSON NOT NULL,
 expires_at DATETIME(6) NOT NULL,
 created_at DATETIME(6) NOT NULL,
 KEY idx_draft_run(scope_key,turn_id,run_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
CREATE TABLE IF NOT EXISTS teaching_solution_receipt (
 id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
 result_id VARCHAR(36) CHARACTER SET ascii NOT NULL UNIQUE,
 scope_key CHAR(64) CHARACTER SET ascii NOT NULL,
 turn_id VARCHAR(128) CHARACTER SET ascii NOT NULL,
 student_id VARCHAR(128) NOT NULL,
 question_id VARCHAR(36) CHARACTER SET ascii NOT NULL,
 solution_id VARCHAR(36) CHARACTER SET ascii NOT NULL,
 input_json JSON NOT NULL,
 receipt_json JSON NOT NULL,
 view_json JSON NOT NULL,
 history_status VARCHAR(32) NOT NULL DEFAULT 'PENDING',
 repair_attempts INT UNSIGNED NOT NULL DEFAULT 0,
 repair_due_at DATETIME(6) NOT NULL,
 created_at DATETIME(6) NOT NULL,
 UNIQUE KEY uk_receipt_turn(scope_key,turn_id),
 KEY idx_repair(history_status,repair_due_at),
 KEY idx_receipt_student(student_id,created_at,question_id),
 CONSTRAINT fk_teaching_receipt_question FOREIGN KEY(question_id) REFERENCES teaching_question(question_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
