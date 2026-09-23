#!/usr/bin/env bash
# Cross-compile budgit for every platform friends might run it on.
# Output lands in dist/. No cgo, no external deps — every target is pure Go.
set -euo pipefail

VERSION="${1:-dev}"
OUT=dist
rm -rf "$OUT"; mkdir -p "$OUT"

targets=(
  darwin/arm64    # Apple Silicon Macs
  darwin/amd64    # Intel Macs
  linux/amd64
  linux/arm64     # Raspberry Pi 4/5, ARM servers
  windows/amd64
  windows/arm64   # Surface / Snapdragon ARM laptops
)

for t in "${targets[@]}"; do
  os="${t%%/*}"; arch="${t##*/}"
  name="budgit-${VERSION}-${os}-${arch}"
  bin="$OUT/$name/budgit"
  [ "$os" = windows ] && bin="$bin.exe"
  mkdir -p "$OUT/$name"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" \
    go build -trimpath -ldflags="-s -w" -o "$bin" .
  cp README.md LICENSE "$OUT/$name/" 2>/dev/null || true
  (cd "$OUT" && if [ "$os" = windows ]; then zip -qr "$name.zip" "$name"; \
                else tar czf "$name.tar.gz" "$name"; fi && rm -rf "$name")
  echo "  built $name"
done

echo
echo "Archives in $OUT/ — upload these to a GitHub release."
