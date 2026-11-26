#!/bin/sh

set -eu

if [ "$(id -u)" -ne 0 ]; then
    echo "run this installer as root (for example: sudo $0 /path/to/sing-box-daemon)" >&2
    exit 1
fi

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
SOURCE=${1:-./sing-box-daemon}
DAEMON_GROUP=sing-box-daemon
INSTALL_BIN=/usr/local/bin/sing-box-daemon
INSTALL_DATA=/var/lib/sing-box-daemon
INSTALL_UNIT=/etc/systemd/system/sing-box-daemon.service

if [ ! -f "$SOURCE" ]; then
    echo "daemon binary not found: $SOURCE" >&2
    exit 1
fi
SOURCE=$(CDPATH= cd -- "$(dirname -- "$SOURCE")" && pwd)/$(basename -- "$SOURCE")

if ! getent group "$DAEMON_GROUP" >/dev/null; then
    groupadd --system "$DAEMON_GROUP"
fi

# sudo preserves the installing account in SUDO_USER. Only that account is
# added; every member of this group may connect to the root daemon socket and
# claim it while no owner has been recorded yet.
INSTALL_USER=${SUDO_USER:-}
if [ -n "$INSTALL_USER" ] && [ "$INSTALL_USER" != root ] && id "$INSTALL_USER" >/dev/null 2>&1; then
    usermod -a -G "$DAEMON_GROUP" "$INSTALL_USER"
fi

install -o root -g root -m 0755 "$SOURCE" "$INSTALL_BIN"
install -d -o root -g "$DAEMON_GROUP" -m 0750 "$INSTALL_DATA"
install -o root -g root -m 0644 "$SCRIPT_DIR/sing-box-daemon.service" "$INSTALL_UNIT"

systemctl daemon-reload
systemctl enable --now sing-box-daemon.service

if [ -n "$INSTALL_USER" ] && [ "$INSTALL_USER" != root ]; then
    echo "$INSTALL_USER may need to log out and back in for the $DAEMON_GROUP membership to take effect."
fi
