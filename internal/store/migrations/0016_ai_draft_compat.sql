-- Conditional column additions run in migrateAIDraftCompat within this transaction.
CREATE TABLE IF NOT EXISTS ai_prompt_versions (
 prompt_id TEXT NOT NULL REFERENCES ai_configs(id) ON DELETE CASCADE,
 version INTEGER NOT NULL,
 body TEXT NOT NULL,
 created_at TEXT NOT NULL,
 PRIMARY KEY(prompt_id,version)
);
