ALTER TABLE inspection_runs ADD COLUMN detail TEXT NOT NULL DEFAULT '{}';
CREATE TABLE inspection_batches (
 id TEXT PRIMARY KEY,
 name TEXT NOT NULL,
 state TEXT NOT NULL,
 created_at INTEGER NOT NULL,
 completed_at INTEGER NOT NULL DEFAULT 0,
 report TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX inspection_batches_created ON inspection_batches(created_at DESC);
