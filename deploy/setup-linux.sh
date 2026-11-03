#!/usr/bin/env bash
# After copying a built release to /opt/server-aibot (or AIBOT_ROOT for staging).
set -euo pipefail
umask 077
root=${AIBOT_ROOT:-/opt/server-aibot}
cd "$root"
[[ $EUID == 0 && "$root" =~ ^/opt/[a-zA-Z0-9._-]+$ ]] || { echo "root and an /opt installation required" >&2; exit 1; }
for f in frontend/dist/index.html bin/tunnel-control app.py; do test -f "$f"; done
for u in aibot aibot-tunnel; do id "$u" >/dev/null 2>&1 || useradd --system --no-create-home --shell /usr/sbin/nologin "$u"; done
install -d -m 0750 -o root -g aibot /etc/server-aibot
install -d -m 0700 -o aibot -g aibot /var/lib/server-aibot
install -d -m 0700 -o aibot-tunnel -g aibot-tunnel /var/lib/aibot-tunnel
if [[ ! -e /etc/server-aibot/platform.env ]]; then
    python3 deploy/init_env.py /etc/server-aibot/platform.env
fi
grep -E '^GOSTC_(CONTROL_SECRET|ADMIN_ADDR)=' /etc/server-aibot/platform.env > /etc/server-aibot/tunnel.env
if [[ ! -e /etc/server-aibot/tls.key && ! -e /etc/server-aibot/tls.crt ]]; then
    openssl req -x509 -newkey rsa:3072 -sha256 -nodes -days 180 -subj '/CN=localhost' \
      -addext 'subjectAltName=DNS:localhost,IP:127.0.0.1' -keyout /etc/server-aibot/tls.key -out /etc/server-aibot/tls.crt >/dev/null 2>&1
fi
chgrp aibot /etc/server-aibot/tls.key /etc/server-aibot/tls.crt
chmod 640 /etc/server-aibot/tls.key /etc/server-aibot/tls.crt
[[ -x venv/bin/python ]] || python3 -m venv venv
venv/bin/python -m pip install --disable-pip-version-check -r backend/requirements.txt
# Application files and venv must be readable by the unprivileged services.
chmod 755 "$root"
for d in app.py backend frontend/dist tunnel bin venv; do chmod -R a+rX "$d"; done
chmod a+rx frontend
chmod 755 bin/tunnel-control
if [[ -f bin/gostc ]]; then chmod 755 bin/gostc; fi
for unit in aibot-web aibot-tunnels; do
    sed "s|/opt/server-aibot|$root|g" "deploy/$unit.service" > "/etc/systemd/system/$unit.service"
    chmod 644 "/etc/systemd/system/$unit.service"
done
systemctl daemon-reload
systemctl enable --now aibot-tunnels aibot-web
systemctl restart aibot-tunnels aibot-web
systemctl is-active aibot-tunnels aibot-web
