#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
BASE_DIR="${BASE_DIR:-/tmp}"
RESULTS_DIR="${RESULTS_DIR:-$BASE_DIR/edr-bench-results}"
EDR_BIN="${EDR:-$REPO_ROOT/edr}"
ITERS="${ITERS:-5}"

mkdir -p "$RESULTS_DIR"

clone_if_missing() {
    local url="$1" dir="$2"
    if [ -d "$dir/.git" ]; then
        echo "Reusing existing checkout: $dir"
        return
    fi
    rm -rf "$dir"
    git clone --depth 1 "$url" "$dir"
}

run_profile() {
    local name="$1" profile="$2"
    local outfile="$RESULTS_DIR/${name}.out"
    echo ""
    echo "=================================================================="
    echo "Running benchmark: $name"
    echo "Profile: $profile"
    echo "Output:  $outfile"
    echo "=================================================================="
    BASE_DIR="$BASE_DIR" EDR="$EDR_BIN" ITERS="$ITERS" bash "$SCRIPT_DIR/native_comparison.sh" "$profile" | tee "$outfile"
}

if [ ! -x "$EDR_BIN" ]; then
    (cd "$REPO_ROOT" && go build -o "$EDR_BIN" .)
fi

clone_if_missing "https://github.com/urfave/cli.git" "$BASE_DIR/edr-bench-urfave-cli"
clone_if_missing "https://github.com/vitessio/vitess.git" "$BASE_DIR/edr-bench-vitess"
clone_if_missing "https://github.com/pallets/click.git" "$BASE_DIR/edr-bench-click"
clone_if_missing "https://github.com/rails/thor.git" "$BASE_DIR/edr-bench-thor"
clone_if_missing "https://github.com/reduxjs/redux-toolkit.git" "$BASE_DIR/edr-bench-redux-toolkit"

run_profile "urfave-cli" "$SCRIPT_DIR/profiles/real/urfave_cli.sh"
run_profile "vitess-sqlparser" "$SCRIPT_DIR/profiles/real/vitess_sqlparser.sh"
run_profile "vitess-vtgate" "$SCRIPT_DIR/profiles/real/vitess_vtgate.sh"
run_profile "click" "$SCRIPT_DIR/profiles/real/click.sh"
run_profile "thor" "$SCRIPT_DIR/profiles/real/thor.sh"
run_profile "redux-toolkit" "$SCRIPT_DIR/profiles/real/redux_toolkit.sh"

echo ""
echo "Results written to $RESULTS_DIR"
