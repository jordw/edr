#!/usr/bin/env bash
# rename_fp.sh — per-language over-rewrite measurement.
#
# For each (lang, repo, file:symbol) tuple, runs edr rename twice with
# EDR_EVAL_FORCE_MODE=scope and =name-match. Both runs are --dry-run.
# Reports occurrence counts and the delta so we can quantify how much
# the scope-aware path narrows (or widens) rewrites vs. the regex path.
#
# Requires the eval corpus under $REPO_BASE (default: parent of edr).

set -euo pipefail

: "${REPO_BASE:=$(cd "$(dirname "$0")/../../.." && pwd)}"
EDR_BIN="${EDR_BIN:-edr}"

# Tuples: "label|repo|symbol". Pick symbols with realistic common names
# so we stress the cross-class false-positive surface.
TUPLES=(
  "go:kubernetes|kubernetes|staging/src/k8s.io/client-go/kubernetes/clientset.go:NewForConfig"
  "rust:tokio-ambiguous|tokio|tokio/src/task/spawn.rs:spawn"
  "rust:tokio-unique|tokio|tokio/src/runtime/blocking/pool.rs:BlockingPool"
  "java:spring|spring-framework|spring-core/src/main/java/org/springframework/core/io/ClassPathResource.java:getFilename"
  "kotlin:kotlin|kotlin|compiler/multiplatform-parsing/common/src/org/jetbrains/kotlin/kmp/lexer/KtTokens.kt:KtTokens"
  "python:pytorch|pytorch|torch/nn/modules/linear.py:forward"
  "ts:vscode|vscode|src/vs/platform/encryption/common/encryptionService.ts:isKwallet"
  "c:linux-common|linux|kernel/sched/core.c:sched_tick"
  "c:linux-unique|linux|arch/x86/kernel/tsc.c:tsc_read_refs"
)

printf "%-22s %-8s %-8s %8s %8s %+8s %s\n" "lang:repo" "scope" "regex" "n_scope" "n_regex" "delta" "target"
printf "%s\n" "------------------------------------------------------------------------------------------"

for tuple in "${TUPLES[@]}"; do
  label=${tuple%%|*}
  rest=${tuple#*|}
  repo=${rest%%|*}
  target=${rest#*|}
  repo_path="$REPO_BASE/$repo"

  if [ ! -d "$repo_path" ]; then
    printf "%-22s %-8s %s\n" "$label" "SKIP" "(missing $repo_path)"
    continue
  fi

  set +o pipefail
  scope_json=$(cd "$repo_path" && EDR_EVAL_FORCE_MODE=scope "$EDR_BIN" rename "$target" --to "__edr_eval_ren__" --dry-run --cross-file --force 2>&1 | head -1)
  regex_json=$(cd "$repo_path" && EDR_EVAL_FORCE_MODE=name-match "$EDR_BIN" rename "$target" --to "__edr_eval_ren__" --dry-run --cross-file --force 2>&1 | head -1)
  set -o pipefail

  scope_mode=$(printf "%s" "$scope_json" | sed -n 's/.*"mode":"\([^"]*\)".*/\1/p')
  regex_mode=$(printf "%s" "$regex_json" | sed -n 's/.*"mode":"\([^"]*\)".*/\1/p')
  n_scope=$(printf "%s" "$scope_json" | sed -n 's/.*"n":\([0-9]*\).*/\1/p')
  n_regex=$(printf "%s" "$regex_json" | sed -n 's/.*"n":\([0-9]*\).*/\1/p')

  # Handle error paths (JSON shape differs)
  if [ -z "$n_scope" ]; then n_scope=0; scope_mode="error"; fi
  if [ -z "$n_regex" ]; then n_regex=0; regex_mode="error"; fi

  delta=$((n_regex - n_scope))
  printf "%-22s %-8s %-8s %8d %8d %+8d %s\n" "$label" "$scope_mode" "$regex_mode" "$n_scope" "$n_regex" "$delta" "$target"
done

echo
echo "Interpretation:"
echo "  delta > 0: regex over-rewrote relative to scope (likely FP caught by scope)"
echo "  delta < 0: scope over-rewrote relative to regex (possible FP in scope)"
echo "  scope=name-match: ambiguity detected; scope aborted, fell back to regex"
