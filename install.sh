#!/bin/sh
# Install cf-ddns on Linux.
#
#   curl -fsSL https://raw.githubusercontent.com/chriswirz/cf-ddns/main/install.sh | sh
#
# Run as root to install system wide (/usr/local/bin, /etc/cf-ddns, a systemd
# system service). Run as an ordinary user to install into that user's home
# (~/.local/bin, ~/.config/cf-ddns, a systemd user service). Both work; the
# script picks the right paths for whoever runs it.
#
# Options, which go after "sh -s --" when piping:
#
#   --service          also install and start the background service
#   --no-service       never install the service (the default when piped)
#   --verbose          run the service with --verbose (every check is logged)
#   --version VERSION  install a specific tag instead of the latest release
#   --bin-dir DIR      override where the binary goes
#   --config-dir DIR   override where config.json goes
#   --uninstall        remove cf-ddns, its service and its unit file
#   --help             show this text
#
# Example:
#   curl -fsSL .../install.sh | sh -s -- --service

set -eu

REPO="chriswirz/cf-ddns"
BINARY="cf-ddns"
API="${CF_DDNS_API:-https://api.github.com}"
DOWNLOADS="${CF_DDNS_DOWNLOADS:-https://github.com}"

VERSION=""
BIN_DIR=""
CONFIG_DIR=""
WANT_SERVICE="ask"
DO_UNINSTALL="no"
SERVICE_VERBOSE=""

# ----------------------------------------------------------------- output ---

# Colour only when stderr is a terminal, so piped or logged output stays clean.
if [ -t 2 ]; then
    C_RED=$(printf '\033[31m')
    C_GRN=$(printf '\033[32m')
    C_YEL=$(printf '\033[33m')
    C_DIM=$(printf '\033[2m')
    C_OFF=$(printf '\033[0m')
else
    C_RED=''
    C_GRN=''
    C_YEL=''
    C_DIM=''
    C_OFF=''
fi

say()  { printf '%s==>%s %s\n' "$C_GRN" "$C_OFF" "$*" >&2; }
warn() { printf '%swarn:%s %s\n' "$C_YEL" "$C_OFF" "$*" >&2; }
note() { printf '%s     %s%s\n' "$C_DIM" "$*" "$C_OFF" >&2; }
die()  { printf '%serror:%s %s\n' "$C_RED" "$C_OFF" "$*" >&2; exit 1; }

usage() {
    sed -n '2,23p' "$0" | sed 's/^#\{1,\} \{0,1\}//'
    exit 0
}

# -------------------------------------------------------------- arguments ---

while [ $# -gt 0 ]; do
    case "$1" in
        --service)      WANT_SERVICE="yes" ;;
        --verbose)      SERVICE_VERBOSE=" --verbose" ;;
        --no-service)   WANT_SERVICE="no" ;;
        --uninstall)    DO_UNINSTALL="yes" ;;
        --version)      shift; VERSION="${1:-}"; [ -n "$VERSION" ] || die "--version needs a value" ;;
        --version=*)    VERSION="${1#*=}" ;;
        --bin-dir)      shift; BIN_DIR="${1:-}"; [ -n "$BIN_DIR" ] || die "--bin-dir needs a value" ;;
        --bin-dir=*)    BIN_DIR="${1#*=}" ;;
        --config-dir)   shift; CONFIG_DIR="${1:-}"; [ -n "$CONFIG_DIR" ] || die "--config-dir needs a value" ;;
        --config-dir=*) CONFIG_DIR="${1#*=}" ;;
        -h|--help)      usage ;;
        *)              die "unknown option: $1 (try --help)" ;;
    esac
    shift
done

# --------------------------------------------------------------- platform ---

require() {
    command -v "$1" >/dev/null 2>&1 || die "$1 is required but is not installed"
}

detect_platform() {
    os=$(uname -s 2>/dev/null || echo unknown)
    if [ "$os" != "Linux" ]; then
        die "this installer is for Linux, but this is $os.
For macOS, Windows or FreeBSD, download from:
  https://github.com/$REPO/releases"
    fi

    machine=$(uname -m 2>/dev/null || echo unknown)
    case "$machine" in
        x86_64|amd64)       ARCH="amd64" ;;
        aarch64|arm64)      ARCH="arm64" ;;
        armv7l|armv7|armhf) ARCH="arm" ;;
        armv6l|armv6)       ARCH="arm" ;;
        *) die "unsupported architecture: $machine
Supported: x86_64, aarch64, armv6 and armv7." ;;
    esac
}

# is_root selects between a system install and a per-user one. Everything the
# script writes follows from this one answer.
is_root() { [ "$(id -u)" -eq 0 ]; }

set_paths() {
    if is_root; then
        MODE="system"
        : "${BIN_DIR:=/usr/local/bin}"
        : "${CONFIG_DIR:=/etc/cf-ddns}"
        UNIT_DIR="/etc/systemd/system"
        SYSTEMCTL="systemctl"
        JOURNAL="journalctl"
    else
        MODE="user"
        : "${BIN_DIR:=$HOME/.local/bin}"
        : "${CONFIG_DIR:=${XDG_CONFIG_HOME:-$HOME/.config}/cf-ddns}"
        UNIT_DIR="${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user"
        SYSTEMCTL="systemctl --user"
        JOURNAL="journalctl --user"
    fi
}

# -------------------------------------------------------------- downloads ---

# api_get fetches JSON from the public API. GITHUB_TOKEN is used if it happens
# to be set, purely to lift the unauthenticated rate limit of 60 requests an
# hour per IP, which realistically only bites in CI.
api_get() {
    if [ -n "${GITHUB_TOKEN:-}" ]; then
        curl -fsSL \
            -H "Authorization: Bearer $GITHUB_TOKEN" \
            -H "Accept: application/vnd.github+json" \
            -H "X-GitHub-Api-Version: 2022-11-28" \
            "$1"
    else
        curl -fsSL -H "Accept: application/vnd.github+json" "$1"
    fi
}

# json_field reads one string field out of a JSON document. jq is not installed
# on a minimal system, and this only ever needs a tag name.
json_field() {
    sed -n 's/.*"'"$1"'"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1
}

# latest_from_redirect reads the tag out of the URL that /releases/latest
# redirects to, which needs no API call and so has no rate limit.
latest_from_redirect() {
    url=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "$DOWNLOADS/$REPO/releases/latest" 2>/dev/null) || return 0
    case "$url" in
        */tag/*) printf '%s' "${url##*/tag/}" ;;
        *)       : ;;
    esac
}

resolve_version() {
    if [ -n "$VERSION" ]; then
        case "$VERSION" in
            v*) : ;;
            *)  VERSION="v$VERSION" ;;
        esac
        say "Installing $BINARY $VERSION"
        return
    fi
    say "Looking up the latest release"
    body=$(api_get "$API/repos/$REPO/releases/latest" 2>/dev/null) || body=""
    if [ -n "$body" ]; then
        VERSION=$(printf '%s' "$body" | json_field "tag_name")
    fi
    if [ -z "$VERSION" ]; then
        # The API is rate limited to 60 requests an hour per IP. Fall back to
        # the /releases/latest redirect, which is not, and whose target ends in
        # the tag.
        VERSION=$(latest_from_redirect)
    fi
    if [ -z "$VERSION" ]; then
        die "could not work out the latest release of $REPO.
Check https://github.com/$REPO/releases and pass one with --version."
    fi
    say "Latest release is $VERSION"
}

# asset_url is the public download URL for one file of a release. Release
# assets are served straight from github.com, so no API call and no token.
asset_url() {
    printf '%s/%s/releases/download/%s/%s' "$DOWNLOADS" "$REPO" "$VERSION" "$1"
}

download() {
    curl -fsSL "$1" -o "$2"
}

# -------------------------------------------------------------- uninstall ---

uninstall() {
    say "Removing $BINARY ($MODE install)"
    if command -v systemctl >/dev/null 2>&1; then
        $SYSTEMCTL stop "$BINARY" 2>/dev/null || true
        $SYSTEMCTL disable "$BINARY" 2>/dev/null || true
    fi
    rm -f "$UNIT_DIR/$BINARY.service"
    if command -v systemctl >/dev/null 2>&1; then
        $SYSTEMCTL daemon-reload 2>/dev/null || true
    fi
    rm -f "$BIN_DIR/$BINARY"
    say "Removed the binary and the service"
    # The config holds an API token the operator chose; deleting it silently
    # would be the wrong call, so it is left behind and reported.
    if [ -d "$CONFIG_DIR" ]; then
        note "Your config was left in place: $CONFIG_DIR"
        note "Remove it with: rm -rf $CONFIG_DIR"
    fi
    exit 0
}

# ---------------------------------------------------------------- install ---

install_binary() {
    asset="${BINARY}_${VERSION#v}_linux_${ARCH}.tar.gz"
    say "Downloading $asset"

    download "$(asset_url "$asset")" "$TMP/$asset" || die "could not download $asset.
Check that release $VERSION has a Linux $ARCH build:
  https://github.com/$REPO/releases/tag/$VERSION"

    verify_checksum "$asset"

    # -o is no-same-owner in both GNU and busybox tar. Without it, extracting
    # as root tries to apply the uid and gid recorded in the archive, which
    # fails outright when that uid does not exist on this machine.
    tar -xzof "$TMP/$asset" -C "$TMP" || die "could not unpack $asset"
    [ -f "$TMP/$BINARY" ] || die "the archive did not contain a $BINARY binary"

    mkdir -p "$BIN_DIR" || die "could not create $BIN_DIR"
    # Write to a temporary name and rename, so a running service is swapped
    # atomically and never sees a half-written binary.
    cp "$TMP/$BINARY" "$BIN_DIR/.$BINARY.new" || die "could not write to $BIN_DIR"
    chmod 0755 "$BIN_DIR/.$BINARY.new"
    mv -f "$BIN_DIR/.$BINARY.new" "$BIN_DIR/$BINARY" || die "could not install into $BIN_DIR"
    say "Installed $BIN_DIR/$BINARY"
}

verify_checksum() {
    asset="$1"
    download "$(asset_url "checksums.txt")" "$TMP/checksums.txt" || {
        warn "could not download checksums.txt, skipping verification"
        return 0
    }
    if ! command -v sha256sum >/dev/null 2>&1; then
        warn "sha256sum is not installed, skipping verification"
        return 0
    fi
    # Match the whole name, and tolerate the "*name" that sha256sum writes in
    # binary mode as well as the plain "name" of text mode.
    want=$(awk -v a="$asset" '$NF == a || $NF == "*" a { print $1; exit }' "$TMP/checksums.txt")
    if [ -z "$want" ]; then
        warn "$asset is not listed in checksums.txt, skipping verification"
        return 0
    fi
    got=$(sha256sum "$TMP/$asset" | awk '{print $1}')
    if [ "$want" != "$got" ]; then
        die "checksum mismatch for $asset
  expected $want
  got      $got
Refusing to install. Try again, and if it persists, report it."
    fi
    say "Checksum verified"
}

install_config() {
    mkdir -p "$CONFIG_DIR" || die "could not create $CONFIG_DIR"
    chmod 0750 "$CONFIG_DIR" 2>/dev/null || true

    if [ -f "$CONFIG_DIR/config.json" ]; then
        say "Keeping the existing $CONFIG_DIR/config.json"
        return
    fi
    # The binary carries the example, so there is one copy of it, not two that
    # can drift apart.
    "$BIN_DIR/$BINARY" example-config > "$CONFIG_DIR/config.json" ||
        die "could not write $CONFIG_DIR/config.json"
    # It holds an API token, so it is never world readable.
    chmod 0600 "$CONFIG_DIR/config.json"
    say "Wrote a starter config to $CONFIG_DIR/config.json"
}

# ---------------------------------------------------------------- service ---

# have_systemd checks for a systemd this user can actually drive. A user bus is
# absent over a plain SSH session unless lingering is enabled, and a container
# often has no systemd at all.
have_systemd() {
    command -v systemctl >/dev/null 2>&1 || return 1
    if is_root; then
        [ -d /run/systemd/system ]
    else
        $SYSTEMCTL show-environment >/dev/null 2>&1
    fi
}

write_unit() {
    mkdir -p "$UNIT_DIR" || die "could not create $UNIT_DIR"

    user_line=""
    if is_root; then
        install_target="multi-user.target"
        # Drop to the unprivileged cf-ddns account when the packages created
        # one; nothing here needs root beyond reading its own config.
        if id cf-ddns >/dev/null 2>&1; then
            user_line="User=cf-ddns
Group=cf-ddns"
        fi
        # ReadWritePaths carves the config directory back out of
        # ProtectSystem=strict. The tool writes to it: a discovery run adds a
        # possible_records section, and log_file lives there too.
        hardening="NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ReadWritePaths=$CONFIG_DIR
ProtectHome=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
RestrictAddressFamilies=AF_INET AF_INET6
RestrictNamespaces=true
LockPersonality=true
MemoryDenyWriteExecute=true"
    else
        install_target="default.target"
        # ProtectHome would hide the config, which lives under $HOME here.
        hardening="NoNewPrivileges=true
PrivateTmp=true"
    fi

    cat > "$UNIT_DIR/$BINARY.service" <<UNIT
# Written by the cf-ddns installer. Edit it freely, but note that re-running
# the installer with --service overwrites it.
[Unit]
Description=cf-ddns - keep Cloudflare DNS records on the current public IP
Documentation=https://github.com/$REPO
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
${user_line:+$user_line
}ExecStart=$BIN_DIR/$BINARY --config $CONFIG_DIR/config.json$SERVICE_VERBOSE
Restart=always
RestartSec=30

$hardening

[Install]
WantedBy=$install_target
UNIT
    say "Wrote $UNIT_DIR/$BINARY.service"
}

# configured reports whether the config is ready to run. Starting the service
# against the untouched example would just log the same error every 30 seconds.
configured() {
    if [ -n "${CF_API_TOKEN:-}" ]; then
        return 0
    fi
    if grep -q '"api_token"[[:space:]]*:[[:space:]]*""' "$CONFIG_DIR/config.json" 2>/dev/null; then
        return 1
    fi
    return 0
}

install_service() {
    if ! have_systemd; then
        warn "no usable systemd here, so the service was not installed"
        if ! is_root; then
            note "A user service also needs lingering: loginctl enable-linger $(id -un)"
        fi
        note "You can still run it yourself:"
        note "  $BIN_DIR/$BINARY --config $CONFIG_DIR/config.json"
        return
    fi
    write_unit
    $SYSTEMCTL daemon-reload || die "systemctl daemon-reload failed"

    if ! configured; then
        $SYSTEMCTL enable "$BINARY" >/dev/null 2>&1 || true
        warn "Service enabled but not started: config.json has no API token yet."
        note "Add your token and records to $CONFIG_DIR/config.json, then run:"
        note "  $SYSTEMCTL start $BINARY"
        return
    fi
    $SYSTEMCTL enable --now "$BINARY" || die "could not enable and start the service"
    say "Service enabled and started"
    note "Follow it with: $JOURNAL -u $BINARY -f"
}

# ------------------------------------------------------------------- main ---

main() {
    require curl
    require tar
    detect_platform
    set_paths

    if [ "$DO_UNINSTALL" = "yes" ]; then
        uninstall
    fi

    TMP=$(mktemp -d) || die "could not create a temporary directory"
    trap 'rm -rf "$TMP"' EXIT INT TERM

    say "Installing for $(id -un) ($MODE), linux/$ARCH"
    resolve_version
    install_binary
    install_config

    # Piped into sh there is no terminal to ask on, so the safe default is to
    # install the binary and let the operator opt in to the service.
    if [ "$WANT_SERVICE" = "ask" ]; then
        if [ -t 0 ] || [ -e /dev/tty ]; then
            printf 'Install and start the background service? [y/N] ' >&2
            if read -r reply </dev/tty 2>/dev/null; then
                case "$reply" in
                    [yY]*) WANT_SERVICE="yes" ;;
                    *)     WANT_SERVICE="no" ;;
                esac
            else
                printf '\n' >&2
                WANT_SERVICE="no"
            fi
        else
            WANT_SERVICE="no"
        fi
    fi
    if [ "$WANT_SERVICE" = "yes" ]; then
        install_service
    fi

    printf '\n' >&2
    say "Done: $("$BIN_DIR/$BINARY" version 2>/dev/null || echo "$BINARY $VERSION")"

    case ":$PATH:" in
        *":$BIN_DIR:"*) : ;;
        *)
            warn "$BIN_DIR is not on your PATH"
            note "Add it with: export PATH=\"\$PATH:$BIN_DIR\""
            ;;
    esac

    printf '\n' >&2
    note "Next steps:"
    note "  1. Put your Cloudflare API token and records in $CONFIG_DIR/config.json"
    note "     (or leave records empty and run: $BINARY discover)"
    note "  2. Test it: $BINARY once --verbose --config $CONFIG_DIR/config.json"
    if [ "$WANT_SERVICE" != "yes" ]; then
        note "  3. Install the background service by re-running this with --service"
    fi
}

main
