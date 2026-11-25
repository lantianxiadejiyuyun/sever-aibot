CREATE TABLE admin_security (
  admin_id TEXT PRIMARY KEY REFERENCES admins(id) ON DELETE CASCADE,
  totp_enabled INTEGER NOT NULL DEFAULT 0 CHECK (totp_enabled IN (0,1)),
  totp_seed_ct BLOB,
  totp_key_id TEXT NOT NULL DEFAULT '',
  pending_seed_ct BLOB,
  pending_key_id TEXT NOT NULL DEFAULT '',
  pending_expires_at TEXT NOT NULL DEFAULT '',
  last_totp_step INTEGER NOT NULL DEFAULT -1
);
CREATE TABLE admin_recovery_codes (
  admin_id TEXT NOT NULL REFERENCES admins(id) ON DELETE CASCADE,
  code_hash TEXT NOT NULL,
  PRIMARY KEY (admin_id, code_hash)
);
