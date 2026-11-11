#!/usr/bin/env bash
#
# Cross-compiles the release binaries and packs them for a GitHub release.
#
#   scripts/build-release.sh <version> [out-dir]
#
# Expects the web UI to be built already (web/dist, with web/node_modules
# present for the notices). Produces, in out-dir (default: dist), one
# hoard_<version>_<os>_<arch>.tar.gz per target plus checksums.txt. Each
# archive holds the binary, README.md, LICENSE and THIRD_PARTY_NOTICES.md.

set -euo pipefail

version="${1:?usage: build-release.sh <version> [out-dir]}"
out="${2:-dist}"
targets="linux/amd64 linux/arm64 darwin/amd64 darwin/arm64"

if [ ! -f web/dist/index.html ]; then
  echo "web/dist is not built: run 'npm --prefix web run build' first" >&2
  exit 1
fi

mkdir -p "$out"
stage="$(mktemp -d)"
trap 'rm -rf "$stage"' EXIT

scripts/third-party-notices.sh all >"$stage/THIRD_PARTY_NOTICES.md"

for target in $targets; do
  os="${target%/*}"
  arch="${target#*/}"
  name="hoard_${version}_${os}_${arch}"
  mkdir -p "$stage/$name"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" \
    go build -trimpath -ldflags "-s -w -X main.version=${version}" -o "$stage/$name/hoard" ./cmd/hoard
  cp README.md LICENSE "$stage/THIRD_PARTY_NOTICES.md" "$stage/$name/"
  tar -C "$stage" -czf "$out/$name.tar.gz" "$name"
  echo "built $out/$name.tar.gz"
done

cd "$out"
if command -v sha256sum >/dev/null 2>&1; then
  sha256sum hoard_"${version}"_*.tar.gz >checksums.txt
else
  shasum -a 256 hoard_"${version}"_*.tar.gz >checksums.txt
fi
