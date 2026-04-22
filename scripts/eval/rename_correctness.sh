#!/usr/bin/env bash
# rename_correctness.sh — compiler-oracle correctness eval for rename.
#
# For each tuple, git-stash the repo, apply edr rename (NOT dry-run),
# run the build, capture PASS/FAIL, restore state. A compile failure
# is a definitive correctness signal (something about the rename
# broke references). A compile pass is a strong but not conclusive
# positive.

set -o pipefail

: "${REPO_BASE:=$(cd "$(dirname "$0")/../../.." && pwd)}"
: "${EDR_BIN:=edr}"
: "${EDR_EVAL_FORCE_MODE:=scope}"
export EDR_EVAL_FORCE_MODE

# Tuples: "label|repo|target|new_name|build_cmd"
TUPLES=(
  # Go: edr as self-host.
  "go:edr-dispatch|edr|internal/dispatch/dispatch.go:Dispatch|Dispatch2|go build ./..."
  "go:edr-smallfn|edr|internal/dispatch/dispatch_rename.go:expandToDocComment|expandToDocComment2|go build ./..."
  "go:edr-scopeHelper|edr|internal/dispatch/dispatch_rename_scope.go:scopeSupported|scopeSupported2|go build ./..."
  "go:edr-outputRel|edr|internal/output/output.go:Rel|Rel2|go build ./..."
  "go:edr-editTx|edr|internal/edit/transaction.go:NewTransaction|NewTransaction2|go build ./..."
  "go:edr-idxQuery|edr|internal/idx/query.go:Query|Query2|go build ./..."
  "go:edr-ondemandGetSym|edr|internal/index/ondemand.go:GetSymbol|GetSymbol2|go build ./..."
  # Java: hand-built fixture under scripts/eval/fixtures/java-demo.
  "java:lib-static-method|edr|scripts/eval/fixtures/java-demo/src/com/example/lib/Lib.java:compute|compute2|cd scripts/eval/fixtures/java-demo && javac -d /tmp/javabuild_lib_static \$(find src -name '*.java')"
  "java:lib-instance-method|edr|scripts/eval/fixtures/java-demo/src/com/example/lib/Lib.java:process|process2|cd scripts/eval/fixtures/java-demo && javac -d /tmp/javabuild_lib_inst \$(find src -name '*.java')"
  "java:iface-impl|edr|scripts/eval/fixtures/java-demo/src/com/example/iface/ServiceImpl.java:run|runImpl|cd scripts/eval/fixtures/java-demo && javac -d /tmp/javabuild_iface \$(find src -name '*.java')"
  # Rust: tokio with cargo check.
  "rust:tokio-unique|tokio|tokio/src/runtime/blocking/pool.rs:BlockingPool|BlockingPool2|cargo check --quiet --package tokio"
  "rust:tokio-common|tokio|tokio/src/task/spawn.rs:spawn|spawn_renamed|cargo check --quiet --package tokio"
  "rust:tokio-mid|tokio|tokio/src/runtime/handle.rs:Handle|Handle2|cargo check --quiet --package tokio"
)

check_tool() { command -v "$1" >/dev/null 2>&1; }

# Safety: abort if any targeted repo has uncommitted changes. The
# eval does `git reset --hard HEAD` between iterations to restore
# post-rename state; with uncommitted work that would destroy it.
declare -A seen_repos
for tuple in "${TUPLES[@]}"; do
  r=$(printf '%s' "$tuple" | cut -d'|' -f2)
  seen_repos["$r"]=1
done
for r in "${!seen_repos[@]}"; do
  repo_path="$REPO_BASE/$r"
  [ ! -d "$repo_path" ] && continue
  if ! git -C "$repo_path" diff --quiet || ! git -C "$repo_path" diff --cached --quiet; then
    echo "ERROR: $repo_path has uncommitted changes. Commit first; the eval does reset --hard." >&2
    exit 1
  fi
done

pass=0; fail=0; skip=0; total=0
printf "%-22s %-6s %-8s %6s  %s\n" "label" "mode" "result" "files" "notes"
printf "%s\n" "----------------------------------------------------------------------"

for tuple in "${TUPLES[@]}"; do
  total=$((total + 1))
  label=${tuple%%|*}; rest=${tuple#*|}
  repo=${rest%%|*};   rest=${rest#*|}
  target=${rest%%|*}; rest=${rest#*|}
  new_name=${rest%%|*}; build_cmd=${rest#*|}
  repo_path="$REPO_BASE/$repo"
  if [ ! -d "$repo_path" ]; then
    printf "%-22s %-6s %-8s %6s  %s\n" "$label" "-" "SKIP" "-" "missing $repo_path"
    skip=$((skip + 1)); continue
  fi
  tool="${build_cmd%% *}"
  if ! check_tool "$tool"; then
    printf "%-22s %-6s %-8s %6s  %s\n" "$label" "-" "SKIP" "-" "tool $tool not installed"
    skip=$((skip + 1)); continue
  fi

  set +o pipefail
  rename_json=$(cd "$repo_path" && "$EDR_BIN" rename "$target" --to "$new_name" --cross-file --force 2>&1 | head -1)
  set -o pipefail
  status=$(printf "%s" "$rename_json" | sed -n 's/.*"status":"\([^"]*\)".*/\1/p')
  mode=$(printf "%s" "$rename_json" | sed -n 's/.*"mode":"\([^"]*\)".*/\1/p')
  files_changed=$(cd "$repo_path" && git diff --stat 2>/dev/null | tail -1 | awk '{print $1}')
  [ -z "$files_changed" ] && files_changed=0

  result="-"; notes=""
  if [ "$status" = "applied" ]; then
    log="/tmp/rename_correctness_${label//[^a-zA-Z0-9_]/_}.log"
    (cd "$repo_path" && eval "$build_cmd") >"$log" 2>&1
    if [ $? -eq 0 ]; then
      result="PASS"; pass=$((pass + 1))
    else
      result="FAIL"; fail=$((fail + 1))
      notes=$(grep -Ei "error|Error" "$log" | head -1 | cut -c1-80)
      notes="$notes (see $log)"
    fi
  elif [ "$status" = "noop" ]; then
    result="NOOP"; skip=$((skip + 1)); notes="rename reported no changes"
  else
    result="ERR"; fail=$((fail + 1))
    notes=$(printf "%s" "$rename_json" | cut -c1-60)
  fi

  printf "%-22s %-6s %-8s %6s  %s\n" "$label" "${mode:--}" "$result" "$files_changed" "$notes"

  # Restore tracked state. Clean-tree invariant was asserted up
  # front, so reset --hard is safe — it only drops post-rename
  # modifications, nothing pre-existing.
  git -C "$repo_path" reset --hard HEAD >/dev/null 2>&1
done

echo
printf "Total: %d  PASS: %d  FAIL: %d  SKIP: %d  Mode: %s\n" "$total" "$pass" "$fail" "$skip" "$EDR_EVAL_FORCE_MODE"
