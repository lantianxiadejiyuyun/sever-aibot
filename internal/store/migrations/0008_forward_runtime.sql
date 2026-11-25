CREATE TABLE forward_traffic_daily (
 forward_id TEXT NOT NULL,
 day TEXT NOT NULL,
 upload_bytes INTEGER NOT NULL DEFAULT 0,
 download_bytes INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY(forward_id,day)
);
CREATE TABLE forward_traffic_reports (
 node_id TEXT NOT NULL,
 report_id TEXT NOT NULL,
 received_at TEXT NOT NULL,
 PRIMARY KEY(node_id,report_id)
);
CREATE TABLE node_config_revisions (
 node_id TEXT PRIMARY KEY REFERENCES nodes(id) ON DELETE CASCADE,
 revision INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE device_tunnel_revocations(device_id TEXT PRIMARY KEY, revoked_before INTEGER NOT NULL);
DROP INDEX IF EXISTS ux_forwards_port;
CREATE UNIQUE INDEX ux_forwards_port ON forwards(node_id,remote_port) WHERE remote_port>0 AND state<>'error' AND kind='port';

CREATE TABLE forward_paths(forward_id TEXT PRIMARY KEY REFERENCES forwards(id) ON DELETE CASCADE, revision INTEGER NOT NULL, path TEXT NOT NULL, updated_at TEXT NOT NULL);
