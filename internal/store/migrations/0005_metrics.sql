CREATE TABLE device_metrics (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    device_id TEXT NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    received_at INTEGER NOT NULL,
    sampled_at INTEGER NOT NULL,
    sample_id TEXT,
    sample_minute INTEGER,
    payload TEXT NOT NULL,
    UNIQUE (device_id, sample_id),
    UNIQUE (device_id, sample_minute)
);
CREATE INDEX device_metrics_received ON device_metrics(device_id, received_at);
CREATE INDEX device_metrics_sampled ON device_metrics(device_id, sampled_at DESC, id DESC);
