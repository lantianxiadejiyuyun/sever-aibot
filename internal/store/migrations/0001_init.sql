-- 0001_init.sql — control-plane base schema.
-- SQLite must run with foreign_keys=ON (set in the connection DSN).

CREATE TABLE IF NOT EXISTS admins (
  id            TEXT PRIMARY KEY,
  username      TEXT NOT NULL UNIQUE,
  password_hash TEXT NOT NULL,
  created_at    TEXT NOT NULL,
  updated_at    TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS admin_sessions (
  id           TEXT PRIMARY KEY,           -- sha256(token)
  admin_id     TEXT NOT NULL REFERENCES admins(id) ON DELETE CASCADE,
  csrf         TEXT NOT NULL,
  ip           TEXT NOT NULL DEFAULT '',
  user_agent   TEXT NOT NULL DEFAULT '',
  created_at   TEXT NOT NULL,
  expires_at   TEXT NOT NULL,
  last_seen_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_admin_sessions_expiry ON admin_sessions(expires_at);

CREATE TABLE IF NOT EXISTS devices (
  id           TEXT PRIMARY KEY,
  name         TEXT NOT NULL,
  remark       TEXT NOT NULL DEFAULT '',
  tags         TEXT NOT NULL DEFAULT '',
  key_hash     TEXT NOT NULL,
  key_prefix   TEXT NOT NULL DEFAULT '',
  status       TEXT NOT NULL DEFAULT 'offline',
  version      TEXT NOT NULL DEFAULT '',
  os           TEXT NOT NULL DEFAULT '',
  arch         TEXT NOT NULL DEFAULT '',
  last_seen_at TEXT,
  created_at   TEXT NOT NULL,
  updated_at   TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_devices_status ON devices(status);

CREATE TABLE IF NOT EXISTS nodes (
  id                  TEXT PRIMARY KEY,
  name                TEXT NOT NULL,
  remark              TEXT NOT NULL DEFAULT '',
  tags                TEXT NOT NULL DEFAULT '',
  addr                TEXT NOT NULL DEFAULT '',
  protocol            TEXT NOT NULL DEFAULT 'tcp',
  http_port           INTEGER NOT NULL DEFAULT 0,
  replace_address     TEXT NOT NULL DEFAULT '',
  forward_ports       TEXT NOT NULL DEFAULT '',
  domain              TEXT NOT NULL DEFAULT '',
  url_template        TEXT NOT NULL DEFAULT '',
  deny_domain_prefix  TEXT NOT NULL DEFAULT '',
  allow_wildcard      INTEGER NOT NULL DEFAULT 0,
  feature_web         INTEGER NOT NULL DEFAULT 1,
  feature_forward     INTEGER NOT NULL DEFAULT 0,
  feature_tunnel      INTEGER NOT NULL DEFAULT 0,
  feature_p2p         INTEGER NOT NULL DEFAULT 0,
  feature_proxy       INTEGER NOT NULL DEFAULT 0,
  max_pool_count      INTEGER NOT NULL DEFAULT 0,
  p2p_disable_forward INTEGER NOT NULL DEFAULT 0,
  rules               TEXT NOT NULL DEFAULT '',
  limit_total_gb      REAL NOT NULL DEFAULT 0,
  limit_kind          TEXT NOT NULL DEFAULT '',
  limit_reset_day     INTEGER NOT NULL DEFAULT 0,
  limiter_mbps        INTEGER NOT NULL DEFAULT 0,
  concurrency         INTEGER NOT NULL DEFAULT 0,
  connections         INTEGER NOT NULL DEFAULT 0,
  status              TEXT NOT NULL DEFAULT 'offline',
  last_seen_at        TEXT,
  created_at          TEXT NOT NULL,
  updated_at          TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS forwards (
  id            TEXT PRIMARY KEY,
  name          TEXT NOT NULL,
  kind          TEXT NOT NULL DEFAULT 'port',
  device_id     TEXT REFERENCES devices(id) ON DELETE SET NULL,
  node_id       TEXT REFERENCES nodes(id) ON DELETE SET NULL,
  client_id     TEXT NOT NULL DEFAULT '',
  target_ip     TEXT NOT NULL DEFAULT '',
  target_port   INTEGER NOT NULL DEFAULT 0,
  remote_port   INTEGER NOT NULL DEFAULT 0,
  domain        TEXT NOT NULL DEFAULT '',
  state         TEXT NOT NULL DEFAULT 'saved',
  revision      INTEGER NOT NULL DEFAULT 0,
  config_hash   TEXT NOT NULL DEFAULT '',
  last_error    TEXT NOT NULL DEFAULT '',
  tags          TEXT NOT NULL DEFAULT '',
  options       TEXT NOT NULL DEFAULT '{}',
  traffic_bytes INTEGER NOT NULL DEFAULT 0,
  created_at    TEXT NOT NULL,
  updated_at    TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_forwards_device ON forwards(device_id);
CREATE INDEX IF NOT EXISTS ix_forwards_node ON forwards(node_id);
CREATE INDEX IF NOT EXISTS ix_forwards_state ON forwards(state);
CREATE UNIQUE INDEX IF NOT EXISTS ux_forwards_port
  ON forwards(node_id, remote_port)
  WHERE remote_port > 0 AND state <> 'error';

CREATE TABLE IF NOT EXISTS audit_logs (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  actor      TEXT NOT NULL,
  action     TEXT NOT NULL,
  target     TEXT NOT NULL DEFAULT '',
  detail     TEXT NOT NULL DEFAULT '',
  ip         TEXT NOT NULL DEFAULT '',
  user_agent TEXT NOT NULL DEFAULT '',
  result     TEXT NOT NULL DEFAULT 'ok',
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_audit_created ON audit_logs(created_at);

CREATE TABLE IF NOT EXISTS settings (
  key        TEXT PRIMARY KEY,
  value      TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS node_ports (
  node_id   TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
  port      INTEGER NOT NULL,
  forward_id TEXT REFERENCES forwards(id) ON DELETE SET NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (node_id, port)
);
