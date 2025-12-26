#!/bin/sh
set -e

# Stop and disable the service before the files are removed.
if command -v systemctl >/dev/null 2>&1; then
    systemctl stop cf-ddns >/dev/null 2>&1 || true
    systemctl disable cf-ddns >/dev/null 2>&1 || true
fi

exit 0
