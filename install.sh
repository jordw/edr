#!/usr/bin/env bash
# Install edr from GitHub Releases.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/jordw/edr/main/install.sh | sh
#
# Installs the binary and automatically runs `edr setup` on the current
# directory if it's a git repo. Pass --skip-setup to install only.
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

# --- Auto-detect target if not specified ---
if [ -z "$TARGET" ] && [ "$SKIP_SETUP" = false ]; then
    if git rev-parse --show-toplevel >/dev/null 2>&1; then
        TARGET="$(git rev-parse --show-toplevel)"
    fi
fi

# --- Auto-detect agent if not specified ---
if [ -z "$AGENT_FLAG" ] && [ -n "$TARGET" ]; then
    if [ -f "$TARGET/CLAUDE.md" ] || [ -f "$TARGET/.claude" ]; then
        AGENT_FLAG="--claude"
    elif [ -f "$TARGET/.cursorrules" ]; then
        AGENT_FLAG="--cursor"
    elif [ -f "$TARGET/AGENTS.md" ]; then
        AGENT_FLAG="--codex"
    fi
fi

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
    VERSION="$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" 2>/dev/null | grep '"tag_name"' | sed -E 's/.*"v?([^"]+)".*/\1/')"
    if [ -z "$VERSION" ]; then
        echo "Error: no releases found at github.com/${REPO}/releases" >&2
        echo "       To build from source instead:" >&2
        echo "       git clone https://github.com/${REPO}.git && ./${REPO##*/}/setup.sh" >&2
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

# --- Verify checksum ---
if [ -n "${EDR_RELEASE_URL:-}" ]; then
    CHECKSUM_URL="${EDR_RELEASE_URL}/checksums.txt"
else
    CHECKSUM_URL="https://github.com/${REPO}/releases/download/v${VERSION_NUM}/checksums.txt"
fi
if curl -fsSL "$CHECKSUM_URL" -o "$TMP/checksums.txt" 2>/dev/null; then
    EXPECTED=$(grep "$ARCHIVE" "$TMP/checksums.txt" | awk '{print $1}')
    if [ -n "$EXPECTED" ]; then
        if command -v sha256sum &>/dev/null; then
            ACTUAL=$(sha256sum "$TMP/$ARCHIVE" | awk '{print $1}')
        elif command -v shasum &>/dev/null; then
            ACTUAL=$(shasum -a 256 "$TMP/$ARCHIVE" | awk '{print $1}')
        fi
        if [ -n "$ACTUAL" ] && [ "$ACTUAL" != "$EXPECTED" ]; then
            echo "Error: checksum mismatch (expected $EXPECTED, got $ACTUAL)" >&2
            exit 1
        fi
        echo "==> Checksum verified"
    fi
else
    echo "==> Warning: could not fetch checksums, skipping verification"
fi

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

# --- Run edr setup on target repo ---
if [ "$SKIP_SETUP" = false ] && [ -n "$TARGET" ]; then
    TARGET="$(cd "$TARGET" && pwd)"
    echo "==> Setting up ${TARGET}..."
    edr setup "$TARGET" $AGENT_FLAG
    echo ""
    echo "==> Done! edr is ready."
    echo "    cd $TARGET && edr map       # explore the codebase"
    echo "    cd $TARGET && edr read ...  # read files and symbols"
elif [ "$SKIP_SETUP" = false ]; then
    echo ""
    echo "==> No git repo detected in current directory."
    echo "    To set up a project: cd /your/project && edr setup ."
else
    echo ""
    echo "==> Done! edr is installed."
fi
