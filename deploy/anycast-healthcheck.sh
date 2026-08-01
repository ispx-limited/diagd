#!/bin/sh
# Announce or withdraw the anycast route based on diagd instance health.
# Run from cron or a systemd timer every few seconds alongside
# bird-anycast.conf.
set -u

OPS=${OPS:-http://127.0.0.1:9143/healthz}
PROTO=${PROTO:-anycast_diagd}

if curl -fsS -m 2 "$OPS" >/dev/null 2>&1; then
    birdc enable "$PROTO" >/dev/null
else
    birdc disable "$PROTO" >/dev/null
fi
