#!/bin/sh
# cpt installer — downloads the latest release binary from GitHub.
# Usage: curl -fsSL https://raw.githubusercontent.com/burkeholland/cpt/main/install.sh | sh
set -e

REPO="burkeholland/cpt"
INSTALL_DIR="${CPT_INSTALL_DIR:-$HOME/.local/bin}"

detect_platform() {
  OS=$(uname -s | tr '[:upper:]' '[:lower:]')
  ARCH=$(uname -m)

  case "$OS" in
    darwin)  OS="darwin" ;;
    linux)   OS="linux" ;;
    mingw*|msys*|cygwin*) OS="windows" ;;
    *) echo "Unsupported OS: $OS" >&2; exit 1 ;;
  esac

  case "$ARCH" in
    x86_64|amd64) ARCH="amd64" ;;
    arm64|aarch64) ARCH="arm64" ;;
    *) echo "Unsupported architecture: $ARCH" >&2; exit 1 ;;
  esac
}

get_latest_version() {
  VERSION=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" | grep '"tag_name"' | sed -E 's/.*"v([^"]+)".*/\1/')
  if [ -z "$VERSION" ]; then
    echo "Failed to fetch latest version" >&2
    exit 1
  fi
}

download() {
  EXT="tar.gz"
  if [ "$OS" = "windows" ]; then
    EXT="zip"
  fi

  URL="https://github.com/${REPO}/releases/download/v${VERSION}/cpt_${OS}_${ARCH}.${EXT}"
  echo "Downloading cpt v${VERSION} for ${OS}/${ARCH}..."

  TMP=$(mktemp -d)
  trap 'rm -rf "$TMP"' EXIT

  curl -fsSL "$URL" -o "${TMP}/cpt.${EXT}"

  if [ "$EXT" = "zip" ]; then
    unzip -qo "${TMP}/cpt.${EXT}" -d "${TMP}"
  else
    tar -xzf "${TMP}/cpt.${EXT}" -C "${TMP}"
  fi

  mkdir -p "$INSTALL_DIR"
  cp "${TMP}/cpt" "${INSTALL_DIR}/cpt"
  chmod +x "${INSTALL_DIR}/cpt"
}

detect_platform
get_latest_version
download

echo ""
echo "✓ cpt v${VERSION} installed to ${INSTALL_DIR}/cpt"
echo ""

# Check if install dir is in PATH
case ":$PATH:" in
  *":${INSTALL_DIR}:"*) ;;
  *)
    echo "Add ${INSTALL_DIR} to your PATH:"
    echo "  export PATH=\"${INSTALL_DIR}:\$PATH\""
    echo ""
    ;;
esac

echo "Then run: cpt --install"
