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
  # Go (kubernetes)
  "go:k8s-NewForConfig|kubernetes|staging/src/k8s.io/client-go/kubernetes/clientset.go:NewForConfig"

  # Rust (tokio)
  "rust:tokio-spawn|tokio|tokio/src/task/spawn.rs:spawn"
  "rust:tokio-BlockingPool|tokio|tokio/src/runtime/blocking/pool.rs:BlockingPool"
  "rust:tokio-Handle|tokio|tokio/src/runtime/handle.rs:Handle"

  # Java (spring)
  "java:spring-getFilename|spring-framework|spring-core/src/main/java/org/springframework/core/io/ClassPathResource.java:getFilename"
  "java:spring-getInputStream|spring-framework|spring-core/src/main/java/org/springframework/core/io/ClassPathResource.java:getInputStream"

  # Kotlin (kotlin)
  "kotlin:KtTokens|kotlin|compiler/multiplatform-parsing/common/src/org/jetbrains/kotlin/kmp/lexer/KtTokens.kt:KtTokens"
  # IntArrayList:toString omitted — symbol-index walk on the full
  # kotlin monorepo for a name as common as toString hangs > 10 min.
  # Coverage of overloaded common names belongs in a smaller corpus
  # repo, not the full kotlin tree.

  # Python (pytorch)
  "python:pytorch-forward|pytorch|torch/nn/modules/linear.py:forward"
  "python:pytorch-batchnorm-forward|pytorch|torch/nn/modules/batchnorm.py:forward"

  # TS/JS (vscode)
  "ts:vscode-isKwallet|vscode|src/vs/platform/encryption/common/encryptionService.ts:isKwallet"
  "ts:vscode-dispose|vscode|src/vs/base/common/lifecycle.ts:dispose"

  # C (linux)
  "c:linux-sched_tick|linux|kernel/sched/core.c:sched_tick"
  "c:linux-tsc_read_refs|linux|arch/x86/kernel/tsc.c:tsc_read_refs"

  # C++ (pytorch ATen) — newly admitted Tier 1
  "cpp:pytorch-Dropout-multiply|pytorch|aten/src/ATen/native/Dropout.cpp:multiply"
  "cpp:pytorch-Dropout-dropout|pytorch|aten/src/ATen/native/Dropout.cpp:dropout"
  "cpp:pytorch-make_feature_noise|pytorch|aten/src/ATen/native/Dropout.cpp:make_feature_noise"

  # Ruby (rails) — newly admitted
  "ruby:rails-save|rails|activerecord/lib/active_record/persistence.rb:save"
  "ruby:rails-find|rails|activerecord/lib/active_record/associations/collection_proxy.rb:find"

  # C# (roslyn) — newly admitted
  "cs:roslyn-MethodKind|roslyn|src/Compilers/Core/Portable/Symbols/MethodKind.cs:MethodKind"

  # Swift (vapor) — newly admitted
  "swift:vapor-BootCommand-run|vapor|Sources/Vapor/Commands/BootCommand.swift:run"
  "swift:vapor-RoutesCommand-run|vapor|Sources/Vapor/Commands/RoutesCommand.swift:run"

  # PHP (laravel types) — newly admitted
  "php:laravel-Model-newCollection|laravel|types/Database/Eloquent/Model.php:newCollection"
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
