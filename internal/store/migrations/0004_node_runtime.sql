-- 0004_node_runtime.sql — node control-plane credentials and online state.
-- Node keys follow the device model: only a hash is stored, the key is
-- returned once at creation or rotation.

ALTER TABLE nodes ADD COLUMN key_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE nodes ADD COLUMN key_prefix TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS ix_nodes_status ON nodes(status);
CREATE INDEX IF NOT EXISTS ix_forwards_device_state ON forwards(device_id, state);
CREATE INDEX IF NOT EXISTS ix_forwards_node_state ON forwards(node_id, state);
