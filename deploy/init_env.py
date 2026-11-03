"""Create a private, non-overwriting environment file without printing secrets."""
import argparse
import base64
import os
from pathlib import Path
import secrets


def main():
    p = argparse.ArgumentParser()
    p.add_argument("path")
    p.add_argument("--db", default="/var/lib/server-aibot/platform.sqlite3")
    a = p.parse_args()
    path = Path(a.path)
    path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    if any(c.isspace() or c in '#"\'$' for c in a.db):
        p.error("use a database path without whitespace or shell metacharacters")
    with os.fdopen(os.open(path, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600), "w", newline="\n") as f:
        for k, v in {
            "AIBOT_ADMIN_PASSWORD": secrets.token_urlsafe(32),
            "AIBOT_MASTER_KEY": base64.urlsafe_b64encode(os.urandom(32)).decode(),
            "GOSTC_CONTROL_SECRET": secrets.token_urlsafe(48),
            "AIBOT_DB": a.db, "GOSTC_CONTROL_URL": "http://127.0.0.1:18081",
            "GOSTC_ADMIN_ADDR": "127.0.0.1:18081", "AIBOT_COOKIE_SECURE": "1",
            "AIBOT_EXTERNAL_PROBE": "0"
        }.items():
            f.write(f"{k}={v}\n")


if __name__ == "__main__":
    main()
