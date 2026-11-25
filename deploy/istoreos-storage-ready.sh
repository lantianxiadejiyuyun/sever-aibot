#!/bin/sh
# Ensure the configured data filesystem is mounted before Docker uses it.
set -eu
uuid="$(uci -q get server_aibot.storage.uuid)"
target="$(uci -q get server_aibot.storage.target)"
timeout="$(uci -q get server_aibot.storage.wait_seconds || printf 30)"
[ -n "$uuid" ] && [ "$target" = /mnt/sda4 ] || exit 1
case "$timeout" in ''|*[!0-9]*) exit 1 ;; esac
fail() {
    logger -t aibot-storage "$*"
    printf '%s\n' "$*" >&2
    exit 1
}
mounted_source() {
    while read -r dev dest rest; do
        if [ "$dest" = "$target" ]; then
            printf '%s\n' "$dev"
            return 0
        fi
    done < /proc/mounts
}
source="$(mounted_source)"
if [ -n "$source" ]; then
    [ "$(blkid -s UUID -o value "$source" 2>/dev/null)" = "$uuid" ] || fail "Unexpected filesystem at $target; refusing Docker startup"
    exit 0
fi
elapsed=0
device=''
while [ "$elapsed" -le "$timeout" ]; do
    device="$(blkid -U "$uuid" 2>/dev/null || true)"
    [ -n "$device" ] && [ -b "$device" ] && break
    [ "$elapsed" -lt "$timeout" ] || fail "Data device UUID=$uuid not available"
    sleep 1
    elapsed=$((elapsed + 1))
done
mkdir -p "$target"
mount -t ext4 "$device" "$target" || fail "Cannot mount $device at $target"
source="$(mounted_source)"
[ -n "$source" ] && [ "$(blkid -s UUID -o value "$source" 2>/dev/null)" = "$uuid" ] || fail "Data mount verification failed"
logger -t aibot-storage "Data filesystem ready at $target"
