#!/usr/bin/env bash
# herdr [[build]] step: download the prebuilt herdr-palette binary for this
# platform from the matching GitHub Release into the plugin's bin/ dir.
# Runs on `herdr plugin install`. `herdr plugin link` skips build steps —
# for a local checkout, run: go build -o bin/herdr-palette .
#
# The release tag matches the manifest version, so a checkout always pulls
# its own release. Build commands may not receive the runtime env, so the
# plugin root resolves from this script's location, not $HERDR_PLUGIN_ROOT.
set -euo pipefail

NAME="herdr-palette"
REPO="Binb1/herdr-palette"

ROOT="$(cd "$(dirname "$0")" && pwd)"
VERSION="$(grep -m1 '^version' "$ROOT/herdr-plugin.toml" | sed -E 's/.*"([^"]+)".*/\1/')"
TAG="v${VERSION}"

os="$(uname -s | tr '[:upper:]' '[:lower:]')" # darwin | linux
arch="$(uname -m)"
case "$arch" in
  x86_64) arch="amd64" ;;
  aarch64 | arm64) arch="arm64" ;;
  *)
    echo "$NAME: no prebuilt binary for $os-$arch — build from source with 'go build -o bin/$NAME .'" >&2
    exit 1
    ;;
esac

archive="${NAME}-${os}-${arch}.tar.gz"
base="https://github.com/${REPO}/releases/download/${TAG}"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

# Release-asset downloads are eventually-consistent: GitHub's CDN can 404
# for a few minutes after a release publishes. Retry, also on 404.
dl() { curl -fsSL --retry 5 --retry-delay 3 --retry-all-errors "$1" -o "$2"; }

echo "$NAME: downloading $archive ($TAG)"
dl "$base/$archive" "$tmp/$archive"
dl "$base/checksums.txt" "$tmp/checksums.txt"

echo "$NAME: verifying checksum"
expected="$(awk -v f="$archive" '$2 == f {print $1}' "$tmp/checksums.txt")"
if command -v sha256sum >/dev/null 2>&1; then
  actual="$(sha256sum "$tmp/$archive" | awk '{print $1}')"
else
  actual="$(shasum -a 256 "$tmp/$archive" | awk '{print $1}')"
fi
if [ -z "$expected" ] || [ "$expected" != "$actual" ]; then
  echo "$NAME: checksum mismatch (expected ${expected:-none}, got $actual)" >&2
  exit 1
fi

mkdir -p "$ROOT/bin"
tar -xzf "$tmp/$archive" -C "$tmp"
install -m 0755 "$tmp/$NAME" "$ROOT/bin/$NAME"
echo "$NAME: installed $ROOT/bin/$NAME"
