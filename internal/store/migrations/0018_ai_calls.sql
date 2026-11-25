CREATE TABLE ai_calls (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 message_id INTEGER NOT NULL REFERENCES ai_messages(id) ON DELETE CASCADE,
 attempt INTEGER NOT NULL,
 tool_round INTEGER NOT NULL,
 provider_id TEXT NOT NULL,
 served_model_id TEXT NOT NULL,
 provider_model TEXT NOT NULL,
 status TEXT NOT NULL,
 finish_reason TEXT NOT NULL DEFAULT '',
 tokens_in INTEGER,
 tokens_out INTEGER,
 cost REAL,
 price_snapshot TEXT NOT NULL,
 estimated_input INTEGER NOT NULL,
 created_at TEXT NOT NULL,
 finished_at TEXT,
 UNIQUE(message_id, attempt)
);
