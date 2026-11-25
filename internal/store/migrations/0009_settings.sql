CREATE TABLE system_settings (
  id INTEGER PRIMARY KEY CHECK(id=1),
  site_name TEXT NOT NULL DEFAULT 'AI 运维控制台',
  announcement TEXT NOT NULL DEFAULT '',
  audit_retention_days INTEGER NOT NULL DEFAULT 180,
  updated_at TEXT NOT NULL
);
INSERT INTO system_settings(id,updated_at) VALUES(1,strftime('%Y-%m-%dT%H:%M:%SZ','now'));
