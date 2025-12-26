#!/usr/bin/env bash
# Build cf-ddns. With no arguments it builds for the host into ./cf-ddns; pass
# --all to cross-compile every release target into ./dist.
set -euo pipefail

cd "$(dirname "$0")"

pkg="./cmd/cf-ddns"
version="$(git describe --tags --always 2>/dev/null || echo dev)"
commit="$(git rev-parse --short HEAD 2>/dev/null || echo none)"
date="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
ldflags="-s -w -X main.version=${version} -X main.commit=${commit} -X main.date=${date}"

build_one() {
  local goos="$1" goarch="$2" ext="${3:-}"
  local out="dist/cf-ddns-${goos}-${goarch}${ext}"
  echo "  ${out}"
  GOOS="$goos" GOARCH="$goarch" CGO_ENABLED=0 \
    go build -trimpath -ldflags "$ldflags" -o "$out" "$pkg"
}

case "${1:-}" in
  --all)
    echo "Building cf-ddns ${version} for all targets"
    mkdir -p dist
    build_one windows amd64 .exe
    build_one windows arm64 .exe
    build_one linux   amd64
    build_one linux   arm64
    build_one linux   arm
    build_one darwin  amd64
    build_one darwin  arm64
    build_one freebsd amd64
    if command -v sha256sum >/dev/null 2>&1; then
      (cd dist && sha256sum cf-ddns-* > SHA256SUMS)
      echo "Wrote dist/SHA256SUMS"
    fi
    ;;
  --test)
    gofmt -l .
    go vet ./...
    go test ./...
    # Run the same linter CI runs, when it is installed. Keeping it optional
    # means a contributor without it can still use --test.
    if command -v golangci-lint >/dev/null 2>&1; then
      golangci-lint run ./...
    else
      echo "(golangci-lint not installed, skipping lint)"
    fi
    ;;
  -h|--help)
    echo "Usage: ./build.sh [--all | --test | --help]"
    echo "  (no args)  build ./cf-ddns for this machine"
    echo "  --all      cross-compile every release target into ./dist"
    echo "  --test     gofmt, go vet and go test"
    exit 0
    ;;
  "")
    echo "Building cf-ddns ${version}"
    CGO_ENABLED=0 go build -trimpath -ldflags "$ldflags" -o cf-ddns "$pkg"
    echo "Wrote ./cf-ddns"
    ;;
  *)
    echo "unknown argument: $1 (try --help)" >&2
    exit 1
    ;;
esac
