#!/bin/sh
# Install the latest candyland release (Linux / macOS / WSL). Detects OS + arch,
# downloads the matching standalone binary from GitHub releases, installs it to
# ~/.local/bin. Windows: use install.ps1.
set -e

REPO="benitogf/candyland"
BINARY="candyland"

OS=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$OS" in
  linux) OS="linux" ;;     # also covers WSL (reports linux)
  darwin) OS="darwin" ;;
  *) echo "Unsupported OS: $OS (use install.ps1 on Windows)" >&2; exit 1 ;;
esac

ARCH=$(uname -m)
case "$ARCH" in
  x86_64|amd64) ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *) echo "Unsupported architecture: $ARCH" >&2; exit 1 ;;
esac

VERSION=$(curl -sL "https://api.github.com/repos/${REPO}/releases/latest" | grep '"tag_name"' | sed -E 's/.*"([^"]+)".*/\1/')
if [ -z "$VERSION" ]; then
  echo "No release found yet for ${REPO}." >&2
  exit 1
fi

ASSET="candyland-${OS}-${ARCH}"
URL="https://github.com/${REPO}/releases/download/${VERSION}/${ASSET}"
INSTALL_DIR="${HOME}/.local/bin"
mkdir -p "$INSTALL_DIR"

echo "Installing candyland ${VERSION} (${OS}/${ARCH})..."
curl -fSL "$URL" -o "${INSTALL_DIR}/${BINARY}"
chmod +x "${INSTALL_DIR}/${BINARY}"

echo "Installed to ${INSTALL_DIR}/${BINARY}"
case ":$PATH:" in
  *":${INSTALL_DIR}:"*) ;;
  *) echo "Add it to PATH: export PATH=\"${INSTALL_DIR}:\$PATH\"" ;;
esac
echo "Run: candyland   (UI on http://localhost:8080)"
