#!/bin/sh
# Install diagd from an unpacked release directory: the binary, and the
# systemd service when systemd runs this machine. Run as root.
set -eu

BINDIR=${BINDIR:-/usr/local/bin}
UNITDIR=${UNITDIR:-/etc/systemd/system}
HERE=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)

if [ "$(id -u)" -ne 0 ]; then
    echo "run as root: sudo $0" >&2
    exit 1
fi
if [ ! -f "$HERE/diagd" ]; then
    echo "diagd binary not found next to this script" >&2
    exit 1
fi

install -m 0755 "$HERE/diagd" "$BINDIR/diagd"
echo "installed $BINDIR/diagd"

if [ ! -d /run/systemd/system ] || ! command -v systemctl >/dev/null 2>&1; then
    echo
    echo "systemd not detected, so only the binary was installed."
    echo "diagd is a plain foreground process that logs to stderr and stops"
    echo "cleanly on SIGTERM; run it under any supervisor (OpenRC, runit,"
    echo "s6, supervisord, a container) with: diagd serve"
    exit 0
fi

if [ -f "$UNITDIR/diagd.service" ]; then
    install -m 0644 "$HERE/diagd.service" "$UNITDIR/diagd.service.new"
    echo "kept existing $UNITDIR/diagd.service"
    echo "this release's unit was written to diagd.service.new for comparison"
else
    install -m 0644 "$HERE/diagd.service" "$UNITDIR/diagd.service"
    echo "installed $UNITDIR/diagd.service"
fi
systemctl daemon-reload

if systemctl is-active --quiet diagd; then
    echo "diagd is running; restart it to pick up the new binary:"
    echo "  systemctl restart diagd"
else
    echo
    echo "review the service arguments, then start:"
    echo "  systemctl edit diagd"
    echo "  systemctl enable --now diagd"
fi
