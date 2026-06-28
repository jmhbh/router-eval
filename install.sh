#!/bin/sh
# router-eval installer.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/jmhbh/router-eval/main/install.sh | sh
#
# Options (environment variables):
#   VERSION=v0.0.1   install a specific tag (default: latest published release)
#   BIN_DIR=~/.local/bin   install location (default: /usr/local/bin if writable, else ~/.local/bin)
set -eu

REPO="jmhbh/router-eval"
VERSION="${VERSION:-latest}"

fail() { echo "install: $*" >&2; exit 1; }

command -v curl >/dev/null 2>&1 || fail "curl is required"
command -v tar  >/dev/null 2>&1 || fail "tar is required"

# Detect platform; asset names match the release workflow's GOOS/GOARCH.
os="$(uname -s)"
case "$os" in
  Linux)  goos="linux" ;;
  Darwin) goos="darwin" ;;
  *) fail "unsupported OS: $os" ;;
esac

arch="$(uname -m)"
case "$arch" in
  x86_64|amd64)  goarch="amd64" ;;
  arm64|aarch64) goarch="arm64" ;;
  *) fail "unsupported architecture: $arch" ;;
esac

# Resolve the latest release tag if one was not pinned.
if [ "$VERSION" = "latest" ]; then
  VERSION="$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
    | grep '"tag_name"' | head -1 | sed -E 's/.*"tag_name": *"([^"]+)".*/\1/')"
  [ -n "$VERSION" ] || fail "could not resolve latest release for ${REPO}; pin one with VERSION=vX.Y.Z"
fi

asset="router-eval-${VERSION}-${goos}-${goarch}.tar.gz"
url="https://github.com/${REPO}/releases/download/${VERSION}/${asset}"

# Choose an install directory.
if [ -n "${BIN_DIR:-}" ]; then
  bin_dir="$BIN_DIR"
elif [ -d /usr/local/bin ] && [ -w /usr/local/bin ]; then
  bin_dir="/usr/local/bin"
else
  bin_dir="$HOME/.local/bin"
fi
mkdir -p "$bin_dir"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

echo "Downloading router-eval ${VERSION} (${goos}/${goarch})..."
curl -fSL "$url" -o "$tmp/$asset" || fail "download failed: $url"
tar -C "$tmp" -xzf "$tmp/$asset" || fail "extract failed"
[ -f "$tmp/router-eval" ] || fail "archive did not contain a router-eval binary"
chmod +x "$tmp/router-eval"
mv "$tmp/router-eval" "$bin_dir/router-eval"

echo "Installed router-eval ${VERSION} to ${bin_dir}/router-eval"
case ":$PATH:" in
  *":$bin_dir:"*) echo "Run: router-eval --help" ;;
  *) echo "NOTE: ${bin_dir} is not on your PATH. Add it, e.g.:"; echo "  export PATH=\"${bin_dir}:\$PATH\"" ;;
esac
