#!/usr/bin/env bash
# Rarefy one-line installer.
#
#   curl -sL https://raw.githubusercontent.com/raghavraut/rarefy/main/install.sh | bash
#
# Downloads the matching release tarball, verifies its SHA-256 checksum,
# and installs the `rarefy` binary somewhere already on PATH
# (/usr/local/bin, else $HOME/.local/bin). Set RAREFY_VERSION=vX.Y.Z
# to pin a release instead of latest.
set -euo pipefail

REPO="raghavraut/rarefy"
VERSION="${RAREFY_VERSION:-latest}"

fail() { echo "install: $*" >&2; exit 1; }
have() { command -v "$1" >/dev/null 2>&1; }

have curl || have wget || fail "need curl or wget"

# --- platform -------------------------------------------------------------
os="$(uname -s | tr '[:upper:]' '[:lower:]')"
arch="$(uname -m)"
case "$os" in
  linux)  os="linux" ;;
  darwin) os="darwin" ;;
  mingw*|msys*|cygwin*|windowsnt)
    fail "Windows: download rarefy_<ver>_windows_amd64.zip from https://github.com/${REPO}/releases and unzip it into a PATH folder" ;;
  *) fail "unsupported OS: $(uname -s)" ;;
esac
case "$arch" in
  x86_64|amd64)   arch="amd64" ;;
  aarch64|arm64)  arch="arm64" ;;
  *) fail "unsupported arch: $arch" ;;
esac

# --- resolve version --------------------------------------------------------
# Asset filenames embed the tag (rarefy_v1.0.0_linux_amd64.tar.gz), so
# "latest" must be resolved to a concrete tag first.
if [ "$VERSION" = "latest" ]; then
  if have curl; then
    VERSION="$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
      | grep '"tag_name"' | cut -d'"' -f4)"
  else
    VERSION="$(wget -S --spider -O- "https://github.com/${REPO}/releases/latest" 2>&1 \
      | grep -i '  location:' | grep -o '[^/]*' | tail -1 | tr -d '\r')"
  fi
  [ -n "$VERSION" ] || fail "could not resolve latest release"
fi
echo "install: rarefy $VERSION ($os/$arch)"

# --- download -------------------------------------------------------------
base="https://github.com/${REPO}/releases/download/${VERSION}"
asset="rarefy_${VERSION}_${os}_${arch}.tar.gz"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

dl() { # $1=url $2=dest — retries ride out transient CDN 404s
  if have curl; then
    curl -fsSL --retry 3 --retry-all-errors --retry-delay 2 "$1" -o "$2" && return 0
    code="$(curl -s -o /dev/null -w '%{http_code} <- %{url_effective}' "$1")"
    fail "download failed [${code}]: $1"
  else
    wget --tries=3 -qO "$2" "$1" \
      || fail "download failed: $1 (install curl for a detailed error)"
  fi
}
echo "install: downloading $asset"
dl "${base}/${asset}" "$tmp/pkg.tgz" \
  || fail "download failed — check https://github.com/${REPO}/releases"

# --- checksum ---------------------------------------------------------------
if have sha256sum; then
  dl "${base}/checksums.txt" "$tmp/checksums.txt" \
    || fail "checksum file missing for $tag"
  ( cd "$tmp" && sha256sum -c --status <(grep "  ${asset}\$" checksums.txt) ) \
    || fail "checksum mismatch for $asset"
  echo "install: checksum ok"
elif have shasum; then
  dl "${base}/checksums.txt" "$tmp/checksums.txt" \
    || fail "checksum file missing for $tag"
  ( cd "$tmp" && shasum -a 256 -c --status <(grep "  ${asset}\$" checksums.txt) ) \
    || fail "checksum mismatch for $asset"
  echo "install: checksum ok"
else
  echo "install: warning: no sha256sum/shasum, skipping verification" >&2
fi

tar xzf "$tmp/pkg.tgz" -C "$tmp"
bin="$tmp/rarefy"
[ -x "$bin" ] || fail "archive did not contain a rarefy binary"

# --- install ----------------------------------------------------------------
dest="/usr/local/bin/rarefy"
if [ -w "/usr/local/bin" ]; then
  cp "$bin" "$dest"
elif have sudo; then
  sudo cp "$bin" "$dest"
else
  dest="$HOME/.local/bin/rarefy"
  mkdir -p "$HOME/.local/bin"
  cp "$bin" "$dest"
fi
chmod +x "$dest"
echo "install: installed to $dest"

# --- verify -----------------------------------------------------------------
if ! "$dest" --help >/dev/null 2>&1; then
  fail "installed binary does not run"
fi
case ":$PATH:" in
  *":$(dirname "$dest"):"*) echo "install: done — run 'rarefy --help'" ;;
  *)
    echo "install: done, but $(dirname "$dest") is not on your PATH." >&2
    if [ -n "${ZSH_VERSION:-}" ] || [ "${SHELL:-}" = "/usr/bin/zsh" ] || [ "${SHELL:-}" = "/bin/zsh" ]; then
      echo "install: run: echo 'export PATH=\"\$PATH:$HOME/.local/bin\"' >> ~/.zshrc && source ~/.zshrc" >&2
    else
      echo "install: run: echo 'export PATH=\"\$PATH:$HOME/.local/bin\"' >> ~/.bashrc && source ~/.bashrc" >&2
    fi ;;
esac
