#!/usr/bin/env bash
# Bootstrap edr for any environment (local, cloud, CI, containers).
# Usage: ./setup.sh [target-repo-path]
#
# What it does:
#   1. Checks/installs Go and gcc (if missing)
#   2. Builds the edr binary
#   3. Installs to PATH (~/.local/bin)
#   4. Indexes the target repo
#
# After running: the `edr` command is available globally and the target
# repo is indexed (or cwd if not specified).

set -euo pipefail

# Use sudo only when not already root (containers, CI often run as root)
SUDO=""
if [ "$(id -u)" -ne 0 ] && command -v sudo &>/dev/null; then
    SUDO="sudo"
fi

REPO_DIR="$(cd "$(dirname "$0")" && pwd)"
TARGET="${1:-$(pwd)}"
TARGET="$(cd "$TARGET" && pwd)"
INSTALL_DIR="$HOME/.local/bin"

echo "==> edr setup"
echo "    edr source: $REPO_DIR"
echo "    target repo: $TARGET"

# --- Check Go ---
if ! command -v go &>/dev/null; then
    echo "==> Go not found. Attempting install..."
    if command -v apt-get &>/dev/null; then
        $SUDO apt-get update -qq && $SUDO apt-get install -y -qq golang gcc g++ >/dev/null
    elif command -v brew &>/dev/null; then
        brew install go
    elif command -v apk &>/dev/null; then
        apk add --no-cache go gcc g++ musl-dev
    else
        echo "ERROR: Go not found and no known package manager. Install Go manually."
        exit 1
    fi
fi

# --- Check gcc/g++ (needed for tree-sitter CGO and HCL grammar) ---
if ! command -v gcc &>/dev/null || ! command -v g++ &>/dev/null; then
    echo "==> C/C++ compiler not found. Attempting install..."
    if command -v apt-get &>/dev/null; then
        $SUDO apt-get update -qq && $SUDO apt-get install -y -qq gcc g++ >/dev/null
    elif command -v brew &>/dev/null; then
        brew install gcc
    elif command -v apk &>/dev/null; then
        apk add --no-cache gcc g++ musl-dev
    else
        echo "ERROR: gcc/g++ not found. Install C and C++ compilers for tree-sitter."
        exit 1
    fi
fi

# --- Build ---
echo "==> Building edr..."
cd "$REPO_DIR"
# Distro Go may be older than go.mod requires; let Go download the right version
export GOTOOLCHAIN=auto
BUILD_HASH=$(git -C "$REPO_DIR" rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_VERSION=$(git -C "$REPO_DIR" describe --tags --always 2>/dev/null || echo "dev")
go build -ldflags "-X github.com/jordw/edr/cmd.Version=${BUILD_VERSION} -X github.com/jordw/edr/cmd.BuildHash=${BUILD_HASH}" -o edr .
echo "    built: $REPO_DIR/edr (${BUILD_VERSION}+${BUILD_HASH})"

# --- Install to PATH ---
mkdir -p "$INSTALL_DIR"
cp "$REPO_DIR/edr" "$INSTALL_DIR/edr"
echo "    installed: $INSTALL_DIR/edr"

# Ensure ~/.local/bin is in PATH
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
            echo "    added $INSTALL_DIR to PATH in $SHELL_RC"
        fi
    fi
    export PATH="$INSTALL_DIR:$PATH"
fi

# --- Index target repo ---
echo "==> Indexing $TARGET..."
"$INSTALL_DIR/edr" init -r "$TARGET" 2>/dev/null | head -1
echo ""
echo "==> Done. edr is ready."
echo "    CLI:    edr -r $TARGET <command>"
echo "    Server: edr -r $TARGET serve --stdio"
