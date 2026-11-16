-- 0002_vault_assets.sql — credential vault, asset inventory and access sessions.
-- SQLite must run with foreign_keys=ON (set in the connection DSN).
-- Timestamps are RFC3339 TEXT, matching 0001_init.sql.

-- ---------- credentials ----------
CREATE TABLE IF NOT EXISTS credentials (
  id           TEXT PRIMARY KEY,
  name         TEXT NOT NULL,
  kind         TEXT NOT NULL,              -- ssh_password | ssh_key | baota_api
                                           -- db_password | api_token | generic
  username     TEXT NOT NULL DEFAULT '',
  secret_ct    BLOB NOT NULL,              -- nonce(24) || AEAD ciphertext+tag
  key_id       TEXT NOT NULL,              -- master-key fingerprint for rotation detection
  public_meta  TEXT NOT NULL DEFAULT '{}', -- non-sensitive metadata as a JSON object
  created_at   TEXT NOT NULL,
  updated_at   TEXT NOT NULL,
  last_used_at TEXT
);
CREATE INDEX IF NOT EXISTS ix_credentials_kind ON credentials(kind);
CREATE INDEX IF NOT EXISTS ix_credentials_last_used ON credentials(last_used_at);

-- ---------- assets / jump hosts ----------
CREATE TABLE IF NOT EXISTS assets (
  id            TEXT PRIMARY KEY,
  name          TEXT NOT NULL,
  address       TEXT NOT NULL,              -- IP or hostname
  port          INTEGER NOT NULL DEFAULT 22,
  protocol      TEXT NOT NULL DEFAULT 'ssh', -- ssh|rdp|vnc|http|mysql|postgres
  via_device_id TEXT REFERENCES devices(id), -- jump agent; empty = control-plane direct
  via_asset_id  TEXT REFERENCES assets(id),  -- secondary jump host; v1 chain length <= 3
  credential_id TEXT REFERENCES credentials(id),
  host_key      TEXT NOT NULL DEFAULT '',    -- pinned SSH host public key fingerprint
  tags          TEXT NOT NULL DEFAULT '',
  enabled       INTEGER NOT NULL DEFAULT 1,
  notes         TEXT NOT NULL DEFAULT '',
  created_at    TEXT NOT NULL,
  updated_at    TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_assets_device ON assets(via_device_id);
CREATE INDEX IF NOT EXISTS ix_assets_via_asset ON assets(via_asset_id);
CREATE INDEX IF NOT EXISTS ix_assets_credential ON assets(credential_id);
CREATE INDEX IF NOT EXISTS ix_assets_protocol ON assets(protocol);
CREATE INDEX IF NOT EXISTS ix_assets_name ON assets(name);

CREATE TABLE IF NOT EXISTS asset_checks (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  asset_id   TEXT NOT NULL REFERENCES assets(id) ON DELETE CASCADE,
  device_id  TEXT NOT NULL,
  ok         INTEGER NOT NULL,
  latency_ms INTEGER,
  error      TEXT,
  checked_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_asset_checks_asset ON asset_checks(asset_id, checked_at);

-- ---------- access sessions (terminal / db / http) ----------
CREATE TABLE IF NOT EXISTS sessions (
  id         TEXT PRIMARY KEY,
  asset_id   TEXT REFERENCES assets(id),
  device_id  TEXT NOT NULL DEFAULT '',
  actor      TEXT NOT NULL,
  kind       TEXT NOT NULL,                 -- terminal | db | http
  started_at TEXT NOT NULL,
  ended_at   TEXT,
  bytes_in   INTEGER NOT NULL DEFAULT 0,
  bytes_out  INTEGER NOT NULL DEFAULT 0,
  recording  TEXT NOT NULL DEFAULT '',      -- relative path, asciinema v2
  result     TEXT NOT NULL DEFAULT '',      -- ok | error | timeout | killed
  error      TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS ix_sessions_asset ON sessions(asset_id);
CREATE INDEX IF NOT EXISTS ix_sessions_actor ON sessions(actor);
CREATE INDEX IF NOT EXISTS ix_sessions_started ON sessions(started_at);
