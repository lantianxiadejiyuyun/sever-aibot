CREATE TABLE ai_configs (
 id TEXT PRIMARY KEY,
 kind TEXT NOT NULL CHECK(kind IN ('providers','models','prompts')),
 body TEXT NOT NULL,
 credential_id TEXT REFERENCES credentials(id) ON DELETE RESTRICT,
 parent_id TEXT REFERENCES ai_configs(id) ON DELETE RESTRICT
);
CREATE TABLE ai_prompt_versions (
 prompt_id TEXT NOT NULL REFERENCES ai_configs(id) ON DELETE CASCADE,
 version INTEGER NOT NULL,
 body TEXT NOT NULL,
 created_at TEXT NOT NULL,
 PRIMARY KEY(prompt_id,version)
);
CREATE TABLE ai_conversations (
 id TEXT PRIMARY KEY,
 owner_id TEXT NOT NULL REFERENCES admins(id),
 title TEXT NOT NULL,
 model_id TEXT NOT NULL REFERENCES ai_configs(id) ON DELETE RESTRICT,
 prompt_id TEXT REFERENCES ai_configs(id) ON DELETE RESTRICT,
 prompt_version INTEGER NOT NULL DEFAULT 0,
 prompt_snapshot TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL
);
CREATE TABLE ai_messages (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 conversation_id TEXT NOT NULL REFERENCES ai_conversations(id) ON DELETE CASCADE,
 role TEXT NOT NULL,
 content TEXT NOT NULL,
 status TEXT NOT NULL,
 tokens_in INTEGER,
 tokens_out INTEGER,
 estimated_input INTEGER NOT NULL DEFAULT 0,
 dropped_messages INTEGER NOT NULL DEFAULT 0,
 served_model_id TEXT NOT NULL DEFAULT '',
 cost REAL,
 price_snapshot TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL
);
CREATE INDEX ai_messages_conversation ON ai_messages(conversation_id,id);
CREATE TABLE ai_proposals (
 id TEXT PRIMARY KEY,
 conversation_id TEXT NOT NULL REFERENCES ai_conversations(id) ON DELETE CASCADE,
 body TEXT NOT NULL,
 status TEXT NOT NULL DEFAULT 'proposed',
 approved_by TEXT,
 result TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL
);
