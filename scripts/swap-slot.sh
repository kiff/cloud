#!/usr/bin/env bash
#
# swap-slot.sh — replace one kiff service on this box without dropping
# requests. Runs on the KIFF host as root, uploaded by scripts/deploy.sh.
#
# Each service runs in one of two slots: a base port and an alternate
# port, as the template units kiff-<name>@<port>. Caddy lists both slots
# for every site (scripts/caddy/Caddyfile) and sends each request to the
# first that's up, so a swap never touches Caddy:
#   1. starts the new binary in the idle slot and waits for /healthz;
#   2. stops the old slot. Each binary stops accepting and drains its
#      in-flight requests on SIGTERM; a request Caddy dials to it then is
#      refused, sends nothing, and goes to the new slot.
# If the new slot is not healthy, the old slot keeps serving and the new
# one is stopped.
#
# The active slot is kept in /etc/kiff/slot-<name>, which the health probe
# reads. Before the first swap there is no slot file and the service still
# runs as its legacy unit on the base port; that unit is stopped instead.
#
# usage: swap-slot.sh <name> <base-port> <alt-port> <legacy-unit>
#   name         api | gateway | web
set -euo pipefail

name=$1
base=$2
alt=$3
legacy=$4
unit="kiff-$name@"
slot_file="/etc/kiff/slot-$name"

log() { printf '[swap %s] %s\n' "$name" "$*"; }

if [[ -f "$slot_file" ]]; then
  active=$(cat "$slot_file")
  old_unit="$unit$active"
else
  active=$base
  old_unit=$legacy
fi
if [[ "$active" == "$base" ]]; then
  target=$alt
else
  target=$base
fi
log "active=$active target=$target"

# 1. Start the new process in the idle slot and wait until it answers.
systemctl start "$unit$target"
healthy=0
for _ in $(seq 1 20); do
  if curl -sf --max-time 3 "http://127.0.0.1:$target/healthz" >/dev/null; then
    healthy=1
    break
  fi
  sleep 2
done
if [[ $healthy -ne 1 ]]; then
  log "slot $target never became healthy; still serving on $active"
  systemctl stop "$unit$target" || true
  exit 1
fi
log "slot $target healthy"
# Caddy sends traffic to a slot only once its own check of /healthz passes
# (every second), and skips one for 2s after a refused dial: wait 3s, so it
# has seen the new slot healthy, and any mark on it has expired, before the
# old one stops.
sleep 3

# 2. Retire the old slot. Stopping sends SIGTERM and the binary drains;
# Caddy's next dial to it is refused and goes to the new slot.
echo "$target" > "$slot_file"
systemctl enable "$unit$target" >/dev/null 2>&1 || true
systemctl stop "$old_unit" || true
systemctl disable "$old_unit" >/dev/null 2>&1 || true
log "stopped $old_unit; serving on $target"
