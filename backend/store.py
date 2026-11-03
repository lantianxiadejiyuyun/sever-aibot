"""Single-process SQLite document store. Secrets are separate encrypted records."""
import hashlib
import json
import os
from pathlib import Path
import sqlite3
import threading
import uuid
from contextlib import contextmanager
from datetime import datetime, timezone

from cryptography.fernet import Fernet
from fastapi import HTTPException


def now():
    return datetime.now(timezone.utc).isoformat(timespec="milliseconds")


def uid():
    return uuid.uuid4().hex


def error(message, code=400):
    raise HTTPException(code, message)


def digest(value):
    return hashlib.sha256(json.dumps(value, sort_keys=True, ensure_ascii=False).encode()).hexdigest()


class Store:
    def __init__(self):
        path = Path(os.environ.get("AIBOT_DB", "var/aibot.sqlite3")).resolve()
        path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
        self.cipher = Fernet(os.environ["AIBOT_MASTER_KEY"].encode())
        password = os.environ["AIBOT_ADMIN_PASSWORD"]
        if len(password) < 12:
            raise ValueError("AIBOT_ADMIN_PASSWORD must be at least 12 characters")
        self.salt = os.urandom(24)
        self.password = self.hashpass(password)
        self.lock = threading.RLock()
        self.conn = sqlite3.connect(path, check_same_thread=False, isolation_level=None, timeout=10)
        self.conn.execute("PRAGMA journal_mode=WAL")
        self.conn.execute("CREATE TABLE IF NOT EXISTS records (kind TEXT, id TEXT, body TEXT NOT NULL, PRIMARY KEY(kind,id))")
        self.conn.execute("CREATE INDEX IF NOT EXISTS byhost ON records(kind,json_extract(body,'$.host_id'))")
        if os.name == "posix":
            path.chmod(0o600)

    def hashpass(self, password):
        return hashlib.scrypt(password.encode(), salt=self.salt, n=2**14, r=8, p=1, dklen=32)

    @contextmanager
    def atomic(self):
        with self.lock:
            self.conn.execute("BEGIN IMMEDIATE")
            try:
                yield
            except BaseException:
                self.conn.rollback()
                raise
            else:
                self.conn.commit()

    def get(self, kind, key, required=True):
        with self.lock:
            row = self.conn.execute("SELECT body FROM records WHERE kind=? AND id=?", (kind, key)).fetchone()
        if not row and required:
            error("记录不存在：" + kind, 404)
        return json.loads(row[0]) if row else None

    def all(self, collection, **filters):
        with self.lock:
            rows = self.conn.execute("SELECT body FROM records WHERE kind=? ORDER BY rowid DESC", (collection,)).fetchall()
        return [r for (body,) in rows for r in [json.loads(body)]
                                            if all(r.get(k) == v for k,v in filters.items() if v is not None)]

    def put(self, kind, record):
        with self.lock:
            self.conn.execute("INSERT INTO records VALUES (?,?,?) ON CONFLICT(kind,id) DO UPDATE SET body=excluded.body",
                              (kind, record["id"], json.dumps(record, ensure_ascii=False)))
        return record

    def new(self, collection, **fields):
        return self.put(collection, {**fields, "id": uid(), "created_at": now()})

    def delete(self, kind, key):
        with self.lock:
            self.conn.execute("DELETE FROM records WHERE kind=? AND id=?", (kind,key))

    def secret(self, key, value=None):
        if value is not None:
            self.put("vault", {"id":key, "blob":self.cipher.encrypt(json.dumps(value).encode()).decode()})
        else:
            return json.loads(self.cipher.decrypt(self.get("vault", key)["blob"].encode()))
