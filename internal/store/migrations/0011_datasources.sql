CREATE TABLE IF NOT EXISTS datasources (
 id TEXT PRIMARY KEY,
 name TEXT NOT NULL,
 asset_id TEXT NOT NULL REFERENCES assets(id),
 credential_id TEXT NOT NULL REFERENCES credentials(id),
 database_name TEXT NOT NULL,
 tls_mode TEXT NOT NULL DEFAULT 'verify-full',
 enabled INTEGER NOT NULL DEFAULT 1,
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_datasources_asset ON datasources(asset_id);
