CREATE TABLE ai_task_routes (
 owner_id TEXT NOT NULL REFERENCES admins(id) ON DELETE CASCADE,
 task TEXT NOT NULL CHECK(task IN ('chat','inspection','log_summary','change')),
 model_id TEXT NOT NULL REFERENCES ai_configs(id) ON DELETE RESTRICT,
 PRIMARY KEY(owner_id,task)
);
CREATE TABLE ai_conversation_summaries (
 conversation_id TEXT PRIMARY KEY REFERENCES ai_conversations(id) ON DELETE CASCADE,
 content TEXT NOT NULL,
 through_message INTEGER NOT NULL,
 updated_at TEXT NOT NULL
);
CREATE TABLE ai_conversation_facts (
 conversation_id TEXT PRIMARY KEY REFERENCES ai_conversations(id) ON DELETE CASCADE,
 content TEXT NOT NULL,
 revision INTEGER NOT NULL DEFAULT 1,
 updated_at TEXT NOT NULL
);
