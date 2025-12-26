#!/bin/sh
set -e

# Create a dedicated system user if it does not exist.
if ! getent passwd cf-ddns >/dev/null 2>&1; then
    if command -v useradd >/dev/null 2>&1; then
        useradd --system --no-create-home --shell /usr/sbin/nologin \
            --comment "cf-ddns dynamic DNS client" cf-ddns || true
    elif command -v adduser >/dev/null 2>&1; then
        adduser --system --no-create-home --group cf-ddns || true
    fi
fi

if ! getent group cf-ddns >/dev/null 2>&1; then
    groupadd --system cf-ddns >/dev/null 2>&1 || true
fi

# The config and the token file hold a secret, so keep them off world-read.
if [ -d /etc/cf-ddns ]; then
    chgrp -R cf-ddns /etc/cf-ddns 2>/dev/null || true
    chmod 0750 /etc/cf-ddns 2>/dev/null || true
    chmod 0640 /etc/cf-ddns/config.json /etc/cf-ddns/cf-ddns.env 2>/dev/null || true
fi

# Reload systemd so the new unit is visible. The service is NOT auto-enabled:
# it cannot work until an API token and records are configured. Opt in with
#   systemctl enable --now cf-ddns
if command -v systemctl >/dev/null 2>&1; then
    systemctl daemon-reload >/dev/null 2>&1 || true
fi

exit 0
