#!/usr/bin/env bash
# Install edr from GitHub Releases.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/jordw/edr/main/install.sh | sh
#   curl -fsSL https://raw.githubusercontent.com/jordw/edr/main/install.sh | sh -s -- /path/to/project
#   curl -fsSL https://raw.githubusercontent.com/jordw/edr/main/install.sh | sh -s -- --claude /path/to/project
#
# Options:
#   --claude, --cursor, --codex   Agent target for edr setup (default: auto-detect)
#   --skip-setup                  Skip running edr setup after install
#   --version VERSION             Install a specific version (default: latest)

set -euo pipefail

REPO="${EDR_REPO:-jordw/edr}"
INSTALL_DIR="${EDR_INSTALL_DIR:-$HOME/.local/bin}"
VERSION=""
SKIP_SETUP=false
AGENT_FLAG=""
TARGET=""

# --- Parse arguments ---
while [ $# -gt 0 ]; do
    case "$1" in
        --version)
            VERSION="$2"
            shift 2
            ;;
        --skip-setup)
            SKIP_SETUP=true
            shift
            ;;
        --claude|--cursor|--codex)
            AGENT_FLAG="$1"
            shift
            ;;
        *)
            TARGET="$1"
            shift
            ;;
    esac
done

# --- Detect platform ---
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"

case "$ARCH" in
    x86_64|amd64)  ARCH="amd64" ;;
    aarch64|arm64) ARCH="arm64" ;;
    *)
        echo "Error: unsupported architecture: $ARCH" >&2
        exit 1
        ;;
esac

case "$OS" in
    linux|darwin) ;;
    *)
        echo "Error: unsupported OS: $OS" >&2
        exit 1
        ;;
esac

# --- Resolve version ---
if [ -z "$VERSION" ]; then
    echo "==> Finding latest release..."
    VERSION="$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" | grep '"tag_name"' | sed -E 's/.*"v?([^"]+)".*/\1/')"
    if [ -z "$VERSION" ]; then
        echo "Error: could not determine latest version" >&2
        exit 1
    fi
fi
# Strip leading 'v' if present for the archive name
VERSION_NUM="${VERSION#v}"

echo "==> Installing edr v${VERSION_NUM} (${OS}/${ARCH})"

# --- Download and extract ---
ARCHIVE="edr_${VERSION_NUM}_${OS}_${ARCH}.tar.gz"
# EDR_RELEASE_URL overrides the download base URL (used by CI tests).
if [ -n "${EDR_RELEASE_URL:-}" ]; then
    URL="${EDR_RELEASE_URL}/${ARCHIVE}"
else
    URL="https://github.com/${REPO}/releases/download/v${VERSION_NUM}/${ARCHIVE}"
fi

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

echo "==> Downloading ${URL}..."
curl -fsSL "$URL" -o "$TMP/$ARCHIVE"

echo "==> Extracting..."
tar -xzf "$TMP/$ARCHIVE" -C "$TMP"

# --- Install binary ---
mkdir -p "$INSTALL_DIR"
mv "$TMP/edr" "$INSTALL_DIR/edr"
chmod +x "$INSTALL_DIR/edr"
echo "==> Installed to ${INSTALL_DIR}/edr"

# --- Ensure PATH ---
if [[ ":$PATH:" != *":$INSTALL_DIR:"* ]]; then
    SHELL_RC=""
    if [ -f "$HOME/.zshrc" ]; then
        SHELL_RC="$HOME/.zshrc"
    elif [ -f "$HOME/.bashrc" ]; then
        SHELL_RC="$HOME/.bashrc"
    fi
    if [ -n "$SHELL_RC" ]; then
        if ! grep -q "$INSTALL_DIR" "$SHELL_RC" 2>/dev/null; then
            echo "export PATH=\"$INSTALL_DIR:\$PATH\"" >> "$SHELL_RC"
            echo "==> Added $INSTALL_DIR to PATH in $SHELL_RC"
        fi
    fi
    export PATH="$INSTALL_DIR:$PATH"
fi

echo "==> $(edr --version)"

# --- Optional: run edr setup on target repo ---
if [ "$SKIP_SETUP" = false ] && [ -n "$TARGET" ]; then
    TARGET="$(cd "$TARGET" && pwd)"
    echo "==> Running edr setup on ${TARGET}..."
    edr setup --root "$TARGET" $AGENT_FLAG
fi

echo ""
echo "==> Done! edr is ready."
if [ -n "$TARGET" ]; then
    echo "    Start: edr serve --root $TARGET"
else
    echo "    Usage: edr setup /path/to/your/project"
fi
