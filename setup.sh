#!/usr/bin/env bash
# Build and install edr from source.
#
# Usage: ./setup.sh [target-repo-path] [--global] [--force]
#
# Steps:
#   1. Checks for Go and gcc (errors if missing)
#   2. Builds the edr binary
#   3. Installs to ~/.local/bin (or INSTALL_DIR)
#   4. Runs `edr setup` on the target repo
#
# For Homebrew users: the formula handles build+install.
# Run `edr setup <repo>` directly to configure a repo.

set -euo pipefail

REPO_DIR="$(cd "$(dirname "$0")" && pwd)"
INSTALL_DIR="${INSTALL_DIR:-$HOME/.local/bin}"

# --- Parse arguments ---
TARGET=""
SETUP_FLAGS=()
for arg in "$@"; do
    case "$arg" in
        --global|--force|--skip-index|--no-global|--generic)
            SETUP_FLAGS+=("$arg")
            ;;
        *)
            TARGET="$arg"
            ;;
    esac
done
TARGET="${TARGET:-$(pwd)}"
TARGET="$(cd "$TARGET" && pwd)"

echo "==> edr setup"
echo "    source:  $REPO_DIR"
echo "    target:  $TARGET"
echo "    install: $INSTALL_DIR"

# --- Check and install dependencies ---
missing=()
command -v go &>/dev/null  || missing+=("go")
command -v gcc &>/dev/null || missing+=("gcc")
command -v g++ &>/dev/null || missing+=("g++")

if [ ${#missing[@]} -gt 0 ]; then
    echo "==> Installing missing tools: ${missing[*]}"
    if command -v brew &>/dev/null; then
        brew install go gcc
    elif command -v apt-get &>/dev/null; then
        apt-get update -qq && apt-get install -y -qq golang gcc g++ >/dev/null
    elif command -v apk &>/dev/null; then
        apk add --quiet go gcc g++ musl-dev
    else
        echo "ERROR: missing tools (${missing[*]}) and no supported package manager found."
        echo "Install Go (https://go.dev/dl/) and a C/C++ compiler, then re-run."
        exit 1
    fi
fi

# --- Build ---
echo "==> Building edr..."
cd "$REPO_DIR"
export GOTOOLCHAIN=auto
BUILD_HASH=$(git -C "$REPO_DIR" rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_VERSION=$(git -C "$REPO_DIR" describe --tags --always 2>/dev/null || echo "dev")
if ! go build -ldflags "-X github.com/jordw/edr/cmd.Version=${BUILD_VERSION} -X github.com/jordw/edr/cmd.BuildHash=${BUILD_HASH}" -o edr . 2>&1; then
    echo "ERROR: build failed"
    exit 1
fi
echo "    built ${BUILD_VERSION}+${BUILD_HASH}"

# --- Install binary ---
mkdir -p "$INSTALL_DIR"
cp "$REPO_DIR/edr" "$INSTALL_DIR/edr"
echo "    installed to $INSTALL_DIR/edr"

if [[ ":$PATH:" != *":$INSTALL_DIR:"* ]]; then
    export PATH="$INSTALL_DIR:$PATH"
    echo ""
    echo "NOTE: $INSTALL_DIR is not in your PATH."
    echo "  Add it to your shell profile:"
    echo "    export PATH=\"$INSTALL_DIR:\$PATH\""
    echo ""
fi

# --- Configure target repo ---
echo "==> Configuring $TARGET..."
"$INSTALL_DIR/edr" setup "$TARGET" "${SETUP_FLAGS[@]+"${SETUP_FLAGS[@]}"}"

echo ""
echo "==> Done. edr is ready."
echo "    cd $TARGET && edr map       # explore the codebase"
echo "    cd $TARGET && edr read ...  # read files and symbols"
