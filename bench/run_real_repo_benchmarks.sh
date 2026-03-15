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
    local url="$1" dir="$2" commit="${3:-}"
    if [ -d "$dir/.git" ]; then
        echo "Reusing existing checkout: $dir"
        return
    fi
    rm -rf "$dir"
    if [ -n "$commit" ]; then
        git init "$dir"
        git -C "$dir" remote add origin "$url"
        git -C "$dir" fetch --depth 1 origin "$commit"
        git -C "$dir" checkout FETCH_HEAD
    else
        git clone --depth 1 "$url" "$dir"
    fi
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

# Pinned commits for reproducible benchmarks (update these when re-running).
clone_if_missing "https://github.com/urfave/cli.git" "$BASE_DIR/edr-bench-urfave-cli" "f035ffaa3749afda2cd26fb824aa940747297ef1"
clone_if_missing "https://github.com/vitessio/vitess.git" "$BASE_DIR/edr-bench-vitess" "33d20817882abb3b8761289052bcfdd6903f743c"
clone_if_missing "https://github.com/pallets/click.git" "$BASE_DIR/edr-bench-click" "cdab890e57a30a9f437b88ce9652f7bfce980c1f"
clone_if_missing "https://github.com/rails/thor.git" "$BASE_DIR/edr-bench-thor" "6a680f2f929cc24d61b81197e113066aa18c8fbb"
clone_if_missing "https://github.com/reduxjs/redux-toolkit.git" "$BASE_DIR/edr-bench-redux-toolkit" "2ebb40a7363c5cec826493eabac53fe7b1b6d5d6"
clone_if_missing "https://github.com/django/django.git" "$BASE_DIR/edr-bench-django" "373cb3037fe4e67adbac9ac43340391e859aa957"

run_profile "urfave-cli" "$SCRIPT_DIR/profiles/real/urfave_cli.sh"
run_profile "vitess-sqlparser" "$SCRIPT_DIR/profiles/real/vitess_sqlparser.sh"
run_profile "vitess-vtgate" "$SCRIPT_DIR/profiles/real/vitess_vtgate.sh"
run_profile "click" "$SCRIPT_DIR/profiles/real/click.sh"
run_profile "thor" "$SCRIPT_DIR/profiles/real/thor.sh"
run_profile "redux-toolkit" "$SCRIPT_DIR/profiles/real/redux_toolkit.sh"
run_profile "django" "$SCRIPT_DIR/profiles/real/django.sh"

echo ""
echo "Results written to $RESULTS_DIR"
