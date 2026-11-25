CREATE TABLE IF NOT EXISTS access_recordings (
 session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
 seq INTEGER PRIMARY KEY AUTOINCREMENT,
 elapsed REAL NOT NULL,
 data BLOB NOT NULL,
 event_type TEXT NOT NULL DEFAULT 'o'
);
CREATE INDEX IF NOT EXISTS ix_access_recordings_session ON access_recordings(session_id,seq);
ALTER TABLE sessions ADD COLUMN owner_session TEXT NOT NULL DEFAULT '';
UPDATE sessions SET ended_at=strftime('%Y-%m-%dT%H:%M:%SZ','now'),result='interrupted' WHERE ended_at IS NULL;
