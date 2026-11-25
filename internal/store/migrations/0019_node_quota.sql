CREATE TABLE node_quota_daily (
    node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    day TEXT NOT NULL,
    reserved_bytes INTEGER NOT NULL CHECK(reserved_bytes >= 0),
    PRIMARY KEY(node_id, day)
);
CREATE TABLE node_quota_reservations (
    node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    request_id TEXT NOT NULL,
    requested_bytes INTEGER NOT NULL,
    granted_bytes INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    PRIMARY KEY(node_id, request_id)
);
