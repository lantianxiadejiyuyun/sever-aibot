ALTER TABLE system_settings ADD COLUMN recording_retention_days INTEGER NOT NULL DEFAULT 30 CHECK(recording_retention_days BETWEEN 1 AND 3650);
ALTER TABLE sessions ADD COLUMN recording_purged_at TEXT NOT NULL DEFAULT '';
CREATE INDEX ix_sessions_recording_retention ON sessions(kind, ended_at) WHERE recording_purged_at='';
