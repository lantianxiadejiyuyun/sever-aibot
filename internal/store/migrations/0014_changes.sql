CREATE TABLE IF NOT EXISTS change_proposals (
 id TEXT PRIMARY KEY,
 asset_id TEXT NOT NULL REFERENCES assets(id),
 kind TEXT NOT NULL,
 service TEXT NOT NULL,
 summary TEXT NOT NULL,
 actor TEXT NOT NULL,
 status TEXT NOT NULL DEFAULT 'draft',
 revision TEXT NOT NULL,
 plan TEXT NOT NULL,
 diff TEXT NOT NULL,
 impact TEXT NOT NULL,
 risk TEXT NOT NULL,
 rollback TEXT NOT NULL,
 verification TEXT NOT NULL,
 approved_by TEXT NOT NULL DEFAULT '',
 approved_until TEXT,
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_change_proposals_created ON change_proposals(created_at);
CREATE TABLE IF NOT EXISTS change_executions (
 id TEXT PRIMARY KEY,
 proposal_id TEXT NOT NULL REFERENCES change_proposals(id),
 asset_id TEXT NOT NULL REFERENCES assets(id),
 actor TEXT NOT NULL,
 status TEXT NOT NULL DEFAULT 'running',
 output TEXT NOT NULL DEFAULT '',
 backup_path TEXT NOT NULL DEFAULT '',
 started_at TEXT NOT NULL,
 ended_at TEXT
);
CREATE UNIQUE INDEX IF NOT EXISTS ix_change_active_asset ON change_executions(asset_id) WHERE ended_at IS NULL;
CREATE TABLE IF NOT EXISTS deployment_archives (
 id TEXT PRIMARY KEY,
 name TEXT NOT NULL,
 version TEXT NOT NULL,
 proposal_id TEXT NOT NULL REFERENCES change_proposals(id),
 asset_id TEXT NOT NULL REFERENCES assets(id),
 plan TEXT NOT NULL,
 digest TEXT NOT NULL,
 created_by TEXT NOT NULL,
 created_at TEXT NOT NULL
);
