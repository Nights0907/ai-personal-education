-- Add surrogate keys; preserve every existing business identifier and scope.
ALTER TABLE agent_conversation ADD COLUMN id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT FIRST, DROP PRIMARY KEY, ADD PRIMARY KEY(id), ADD UNIQUE KEY uk_scope_key(scope_key), ADD COLUMN creation_request_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NULL, ADD UNIQUE KEY uk_conversation_create(namespace,subject_id,creation_request_id);
ALTER TABLE agent_message ADD COLUMN id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT FIRST, DROP PRIMARY KEY, ADD PRIMARY KEY(id), ADD UNIQUE KEY uk_scope_message(scope_key,message_id);
ALTER TABLE agent_branch ADD COLUMN id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT FIRST, DROP PRIMARY KEY, ADD PRIMARY KEY(id), ADD UNIQUE KEY uk_scope_branch(scope_key,branch_id);
ALTER TABLE agent_memory_compact ADD COLUMN id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT FIRST, DROP PRIMARY KEY, ADD PRIMARY KEY(id), ADD UNIQUE KEY uk_compact_id(compact_id);
