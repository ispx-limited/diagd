#!/usr/bin/env bash
# Interoperability test: run the OB-UDPST reference client against the diagd
# TR-471 server. Requires go, git, cmake, and a C toolchain.
set -euo pipefail

PORT=${PORT:-24699}
AUTH_PORT=$((PORT - 1))
UDPST_REPO=${UDPST_REPO:-https://github.com/BroadbandForum/obudpst.git}
WORK=$(mktemp -d)
SERVER_PIDS=()

cleanup() {
    for pid in "${SERVER_PIDS[@]:-}"; do
        kill "$pid" 2>/dev/null || true
    done
    rm -rf "$WORK"
}
trap cleanup EXIT

fail() {
    echo "FAIL: $1" >&2
    exit 1
}

if [ -n "${UDPST:-}" ]; then
    :
elif command -v udpst >/dev/null; then
    UDPST=$(command -v udpst)
else
    echo "building OB-UDPST reference client"
    git clone --depth 1 "$UDPST_REPO" "$WORK/obudpst" >/dev/null 2>&1
    cmake -S "$WORK/obudpst" -B "$WORK/obudpst/build" >/dev/null
    make -C "$WORK/obudpst/build" >/dev/null
    UDPST="$WORK/obudpst/build/udpst"
fi

echo "building diagd"
go build -o "$WORK/diagd" ./cmd/diagd

"$WORK/diagd" serve -http "" -echo "" -tr471 "127.0.0.1:$PORT" \
    -tr471-bandwidth 2000 >"$WORK/server.log" 2>&1 &
SERVER_PIDS+=($!)
"$WORK/diagd" serve -http "" -echo "" -tr471 "127.0.0.1:$AUTH_PORT" \
    -tr471-key interoptest >"$WORK/server-auth.log" 2>&1 &
SERVER_PIDS+=($!)
sleep 1

run_udpst() {
    timeout 60 "$UDPST" "$@" 2>&1
}

check_summary() {
    local label=$1 out=$2
    echo "$out" | grep -q "Summary Delivered" || fail "$label: no summary ($out)"
    local delivered
    delivered=$(echo "$out" | grep "Summary Delivered" | sed 's/.*Delivered(%): *//; s/,.*//')
    awk -v d="$delivered" 'BEGIN { exit (d >= 99.0) ? 0 : 1 }' \
        || fail "$label: delivered $delivered% is below 99%"
    echo "ok: $label (delivered $delivered%)"
}

check_summary "downstream" "$(run_udpst -d -t 5 -B 1000 -p "$PORT" 127.0.0.1)"
check_summary "upstream" "$(run_udpst -u -t 5 -B 1000 -p "$PORT" 127.0.0.1)"
check_summary "downstream two connections" \
    "$(run_udpst -d -t 5 -B 1000 -p "$PORT" 127.0.0.1 127.0.0.1)"
check_summary "downstream authenticated" \
    "$(run_udpst -d -t 5 -a interoptest -p "$AUTH_PORT" 127.0.0.1)"

out=$(run_udpst -d -t 5 -B 5000 -p "$PORT" 127.0.0.1 || true)
echo "$out" | grep -q "exceeds available capacity" \
    || fail "capacity rejection not enforced ($out)"
echo "ok: capacity rejection"

out=$(run_udpst -d -t 5 -p "$PORT" 127.0.0.1 || true)
echo "$out" | grep -q "Max bandwidth option required" \
    || fail "bandwidth requirement not enforced ($out)"
echo "ok: bandwidth requirement"

out=$(run_udpst -d -t 5 -a wrongkey -p "$AUTH_PORT" 127.0.0.1 || true)
echo "$out" | grep -q "Authentication failure" \
    || fail "wrong key was not rejected ($out)"
echo "ok: authentication rejection"

echo "PASS: all interoperability tests"
