#!/usr/bin/env bash
# rename_dogfood.sh — at-scale rename correctness oracle.
#
# For each (repo, sample_dir, build_cmd) tuple:
#   1. Sample N functions/methods via `edr orient`.
#   2. For each pick:
#      a. Reset the worktree to HEAD (one shared per repo).
#      b. Apply edr rename --cross-file --force, mangling the name.
#      c. Run the build/typecheck command.
#      d. Record PASS / FAIL / NOOP / ERR.
#   3. Aggregate per-repo and per-language stats.
#
# Goal: surface real-world false positives that hand-curated oracle
# tuples miss. Intended to run nightly / pre-release, not per-commit.
#
# Tunable knobs:
#   N=10 bash rename_dogfood.sh        # sample size per (repo, dir)
#   REPO=tokio bash rename_dogfood.sh  # restrict to one repo
#   SEED=42 bash rename_dogfood.sh     # deterministic sampling

set -o pipefail

: "${REPO_BASE:=$(cd "$(dirname "$0")/../../.." && pwd)}"
: "${EDR_BIN:=edr}"
: "${N:=10}"
: "${SEED:=$(date +%s)}"
: "${REPO:=}"
export EDR_EVAL_FORCE_MODE=scope

# Tuples: "label|repo|sample_dir|build_cmd". sample_dir is a
# subdirectory of the repo from which to draw symbols. build_cmd
# runs from the worktree root and must be fast — typecheck or
# narrowly-scoped build, not full repo build.
TUPLES=(
  "rust:tokio|tokio|tokio/src/sync|cargo check --quiet --package tokio"
  "go:k8s-apimachinery|kubernetes|staging/src/k8s.io/apimachinery/pkg/util|go build ./staging/src/k8s.io/apimachinery/pkg/util/..."
  # vscode tsc setup is fragile; left out of the default tuple list
  # until we have a reliable typecheck command. Re-enable with a
  # pinned tsconfig-with-types path when needed.
  # "ts:vscode|vscode|src/vs/base/common|cd src && tsc --noEmit -p tsconfig.json"
  # linux mm: build oracle disabled — kernel build needs config.
  # The samples come through but build_cmd=true means PASSes are
  # vacuous. Re-enable with `make M=mm modules` once we want it.
  # "c:linux-mm|linux|mm|true"
)

check_tool() { command -v "$1" >/dev/null 2>&1; }

WT_DIR="${TMPDIR:-/tmp}/edr-dogfood-wt-$$"
mkdir -p "$WT_DIR"

cleanup() {
  for wt in "$WT_DIR"/*; do
    [ -d "$wt" ] || continue
    repo=$(basename "$wt")
    src="$REPO_BASE/$repo"
    if [ -d "$src/.git" ] || [ -f "$src/.git" ]; then
      git -C "$src" worktree remove --force "$wt" >/dev/null 2>&1 || rm -rf "$wt"
    else
      rm -rf "$wt"
    fi
  done
  rmdir "$WT_DIR" 2>/dev/null || true
}
trap cleanup EXIT INT TERM

# Pseudo-random pick of N lines from stdin, seeded.
sample_n() {
  awk -v n="$N" -v seed="$SEED" '
    BEGIN { srand(seed); }
    { lines[NR] = $0 }
    END {
      total = NR
      if (total <= n) {
        for (i = 1; i <= total; i++) print lines[i]
        exit
      }
      for (i = 1; i <= total; i++) idx[i] = i
      for (i = 1; i <= n; i++) {
        j = i + int(rand() * (total - i + 1))
        tmp = idx[i]; idx[i] = idx[j]; idx[j] = tmp
        print lines[idx[i]]
      }
    }'
}

# Setup worktrees for unique repos. macOS bash 3.2 has no
# associative arrays — track seen repos in a space-separated string.
repo_done=" "
unique_repos=""
for tuple in "${TUPLES[@]}"; do
  rest=${tuple#*|}
  repo=${rest%%|*}
  if [ -n "$REPO" ] && [ "$REPO" != "$repo" ]; then continue; fi
  case "$repo_done" in *" $repo "*) continue ;; esac
  repo_done="$repo_done$repo "
  unique_repos="$unique_repos $repo"
  src="$REPO_BASE/$repo"
  [ ! -d "$src" ] && continue
  wt="$WT_DIR/$repo"
  if ! git -C "$src" worktree add --detach "$wt" HEAD >/dev/null 2>&1; then
    echo "ERROR: worktree setup failed for $repo" >&2
    exit 1
  fi
done

echo "Dogfood: N=$N SEED=$SEED REPO=${REPO:-all}"
echo
printf "%-22s %-50s %-6s %s\n" "label" "target" "result" "notes"
printf "%s\n" "------------------------------------------------------------------------------------"

total=0; pass=0; fail=0; skip=0
# Per-repo tallies as separate vars (macOS bash 3.2 compat).
per_repo_log=""

for tuple in "${TUPLES[@]}"; do
  label=${tuple%%|*}
  rest=${tuple#*|}
  repo=${rest%%|*}
  rest=${rest#*|}
  sample_dir=${rest%%|*}
  build_cmd=${rest#*|}
  if [ -n "$REPO" ] && [ "$REPO" != "$repo" ]; then continue; fi
  src="$REPO_BASE/$repo"
  wt="$WT_DIR/$repo"
  if [ ! -d "$wt" ]; then
    printf "%-22s %-50s %-6s %s\n" "$label" "-" "SKIP" "no worktree"
    continue
  fi

  # Sample N function lines + N method lines from sample_dir.
  funcs=$(cd "$wt" && "$EDR_BIN" orient "$sample_dir" --full --type function 2>/dev/null \
      | grep ': function ' | sample_n)
  meths=$(cd "$wt" && "$EDR_BIN" orient "$sample_dir" --full --type method 2>/dev/null \
      | grep ': method ' | sample_n)
  picks=$(printf "%s\n%s\n" "$funcs" "$meths")

  if [ -z "$(printf "%s" "$picks" | tr -d '[:space:]')" ]; then
    printf "%-22s %-50s %-6s %s\n" "$label" "-" "SKIP" "no symbols sampled"
    continue
  fi

  while IFS= read -r line; do
    [ -z "$line" ] && continue
    # Format: "path/to/file.ext:start-end: TYPE name"
    file_part=${line%%:*}
    rest_line=${line#*:}            # "start-end: TYPE name"
    after_range=${rest_line#*: }    # "TYPE name"
    sym_name=${after_range##* }     # "name"
    target="$file_part:$sym_name"

    # Sanity guards: skip empty, very short names, generic stdlib
    # overloads that the index might not see as renameable.
    if [ -z "$sym_name" ] || [ "${#sym_name}" -lt 3 ]; then
      continue
    fi
    case "$sym_name" in
      _*|test_*|*_test|*_generated*|operator*|new|drop|fmt|main|init) continue ;;
    esac

    total=$((total + 1))
    new_name="${sym_name}_renamed"

    # Reset worktree before each rename.
    git -C "$wt" reset --hard HEAD >/dev/null 2>&1
    git -C "$wt" clean -fdq >/dev/null 2>&1

    set +o pipefail
    rename_json=$(cd "$wt" && "$EDR_BIN" rename "$target" --to "$new_name" --cross-file --force 2>&1 | head -1)
    set -o pipefail
    status=$(printf "%s" "$rename_json" | sed -n 's/.*"status":"\([^"]*\)".*/\1/p')
    mode=$(printf "%s" "$rename_json" | sed -n 's/.*"mode":"\([^"]*\)".*/\1/p')
    n_edits=$(printf "%s" "$rename_json" | sed -n 's/.*"n":\([0-9]*\).*/\1/p')

    short_target=$(printf "%s" "$target" | tail -c 50)
    if [ "$status" = "noop" ]; then
      printf "%-22s %-50s %-6s %s\n" "$label" "$short_target" "NOOP" "(no occurrences)"
      skip=$((skip + 1))
      per_repo_log="$per_repo_log $repo:skip"
      continue
    fi
    if [ "$status" != "applied" ]; then
      err=$(printf "%s" "$rename_json" | cut -c1-50)
      printf "%-22s %-50s %-6s %s\n" "$label" "$short_target" "ERR" "$err"
      skip=$((skip + 1))
      per_repo_log="$per_repo_log $repo:skip"
      continue
    fi

    log="/tmp/rename_dogfood_${label//[^a-zA-Z0-9_]/_}_${total}.log"
    (cd "$wt" && eval "$build_cmd") >"$log" 2>&1
    rc=$?
    if [ $rc -eq 0 ]; then
      printf "%-22s %-50s %-6s %s\n" "$label" "$short_target" "PASS" "n=$n_edits mode=$mode"
      pass=$((pass + 1))
      per_repo_log="$per_repo_log $repo:pass"
    else
      err=$(grep -Ei "error|Error" "$log" | head -1 | cut -c1-50)
      printf "%-22s %-50s %-6s %s\n" "$label" "$short_target" "FAIL" "n=$n_edits $err (see $log)"
      fail=$((fail + 1))
      per_repo_log="$per_repo_log $repo:fail"
    fi
  done <<< "$picks"
done

echo
echo "Summary: total=$total PASS=$pass FAIL=$fail SKIP=$skip"
for repo in $unique_repos; do
  p=$(printf "%s" "$per_repo_log" | tr ' ' '\n' | grep -c "^$repo:pass$" || true)
  f=$(printf "%s" "$per_repo_log" | tr ' ' '\n' | grep -c "^$repo:fail$" || true)
  s=$(printf "%s" "$per_repo_log" | tr ' ' '\n' | grep -c "^$repo:skip$" || true)
  printf "  %-20s pass=%-3d fail=%-3d skip=%-3d\n" "$repo" "$p" "$f" "$s"
done
