CREATE TABLE operation_checks (
 id TEXT PRIMARY KEY, definition TEXT NOT NULL, enabled INTEGER NOT NULL,
 interval_seconds INTEGER NOT NULL, next_run INTEGER NOT NULL DEFAULT 0,
 lease_until INTEGER NOT NULL DEFAULT 0, state TEXT NOT NULL DEFAULT 'stale',
 checked_at INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE inspection_runs (
 id TEXT PRIMARY KEY, check_id TEXT NOT NULL, check_name TEXT NOT NULL,
 state TEXT NOT NULL, message TEXT NOT NULL, latency_ms INTEGER NOT NULL,
 created_at INTEGER NOT NULL
);
CREATE INDEX inspection_runs_created ON inspection_runs(created_at DESC);
CREATE INDEX inspection_runs_check ON inspection_runs(check_id);
CREATE TABLE operation_alerts (
 id TEXT PRIMARY KEY, check_id TEXT NOT NULL, state TEXT NOT NULL,
 message TEXT NOT NULL, acknowledged INTEGER NOT NULL DEFAULT 0,
 silence_until INTEGER NOT NULL DEFAULT 0, resolved INTEGER NOT NULL DEFAULT 0,
 created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
);
CREATE UNIQUE INDEX operation_alert_active ON operation_alerts(check_id) WHERE resolved=0;
CREATE TABLE notification_channels (
 id TEXT PRIMARY KEY, name TEXT NOT NULL, kind TEXT NOT NULL,
 enabled INTEGER NOT NULL, secret_ct BLOB NOT NULL, key_id TEXT NOT NULL
);
CREATE TABLE operation_notifications (
 id TEXT PRIMARY KEY, message TEXT NOT NULL, is_read INTEGER NOT NULL DEFAULT 0,
 created_at INTEGER NOT NULL
);
CREATE TABLE notification_outbox (
 id TEXT PRIMARY KEY, event_id TEXT NOT NULL, channel_id TEXT NOT NULL,
 payload TEXT NOT NULL, state TEXT NOT NULL DEFAULT 'pending',
 attempts INTEGER NOT NULL DEFAULT 0, next_attempt INTEGER NOT NULL DEFAULT 0,
 lease_until INTEGER NOT NULL DEFAULT 0, last_error TEXT NOT NULL DEFAULT '',
 created_at INTEGER NOT NULL, UNIQUE(event_id,channel_id)
);
CREATE INDEX notification_outbox_due ON notification_outbox(state,next_attempt,lease_until);
CREATE TABLE notification_logs (
 id INTEGER PRIMARY KEY AUTOINCREMENT, outbox_id TEXT NOT NULL,
 attempt INTEGER NOT NULL, state TEXT NOT NULL, message TEXT NOT NULL,
 created_at INTEGER NOT NULL
);
