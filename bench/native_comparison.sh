#!/usr/bin/env bash
# Benchmark: edr vs native tools (Read, Edit, Grep, Glob)
#
# This script benchmarks a single repo or repo subtree using a sourced bash
# profile. The profile defines repo-relative file paths, symbols, and patterns
# for each scenario.
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
PROFILE="${1:-$SCRIPT_DIR/profiles/fixture.sh}"
EDR="${EDR:-./edr}"
ITERS="${ITERS:-5}"

if [ ! -f "$PROFILE" ]; then
    echo "profile not found: $PROFILE" >&2
    exit 1
fi

# Support both .json scenarios (read via jq) and .sh profiles (sourced directly).
if [[ "$PROFILE" == *.json ]]; then
    if ! command -v jq &>/dev/null; then
        echo "jq is required for JSON profiles: brew install jq / apt-get install jq" >&2
        exit 1
    fi
    jq_str()  { jq -r "$1 // empty" "$PROFILE"; }
    jq_int()  { jq -r "$1 // 0" "$PROFILE"; }
    jq_arr()  {
        local -n _ref="$1"
        _ref=()
        while IFS= read -r item; do
            [ -n "$item" ] && _ref+=("$item")
        done < <(jq -r "$2[]? // empty" "$PROFILE")
    }
    _base_dir="${BASE_DIR:-/tmp}"
    BENCH_NAME="$(jq_str '.name')"
    BENCH_ROOT="$(jq_str '.root' | sed "s|\${BASE_DIR}|$_base_dir|g")"
    SCOPE_DIR="$(jq_str '.scope_dir')"
    API_FILE="$(jq_str '.scenarios.understand_api.file')"
    API_READ_SPEC="$(jq_str '.scenarios.understand_api.spec')"
    READ_SYMBOL_FILE="$(jq_str '.scenarios.read_symbol.file')"
    READ_SYMBOL_SPEC="$(jq_str '.scenarios.read_symbol.spec')"
    REFS_PATTERN="$(jq_str '.scenarios.find_refs.pattern')"
    REFS_GREP_ROOT="$(jq_str '.scenarios.find_refs.grep_root')"
    jq_arr REFS_ARGS '.scenarios.find_refs.args'
    SEARCH_PATTERN="$(jq_str '.scenarios.search_context.pattern')"
    SEARCH_ROOT="$(jq_str '.scenarios.search_context.search_root')"
    SEARCH_BUDGET="$(jq_int '.scenarios.search_context.budget')"
    ORIENT_DIR="$(jq_str '.scenarios.orient_map.dir')"
    ORIENT_BUDGET="$(jq_int '.scenarios.orient_map.budget')"
    jq_arr ORIENT_GLOBS '.scenarios.orient_map.globs'
    jq_arr ORIENT_READ_FILES '.scenarios.orient_map.read_files'
    EDIT_FILE="$(jq_str '.scenarios.edit_function.file')"
    EDIT_OLD_TEXT="$(jq_str '.scenarios.edit_function.old_text')"
    EDIT_NEW_TEXT="$(jq_str '.scenarios.edit_function.new_text')"
    WRITE_FILE="$(jq_str '.scenarios.add_method.file')"
    WRITE_INSIDE="$(jq_str '.scenarios.add_method.inside')"
    WRITE_CONTENT="$(jq_str '.scenarios.add_method.content')"
    MULTI_READ_BUDGET="$(jq_int '.scenarios.multi_file_read.budget')"
    jq_arr MULTI_READ_FILES '.scenarios.multi_file_read.files'
    EXPLORE_PATTERN="$(jq_str '.scenarios.explore_symbol.pattern')"
    EXPLORE_GREP_ROOT="$(jq_str '.scenarios.explore_symbol.grep_root')"
    jq_arr EXPLORE_ARGS '.scenarios.explore_symbol.args'
    jq_arr EXPLORE_NATIVE_READ_FILES '.scenarios.explore_symbol.native_read_files'
else
    # shellcheck source=/dev/null
    source "$PROFILE"
fi

if [ -z "${BENCH_ROOT:-}" ]; then
    echo "profile must set BENCH_ROOT" >&2
    exit 1
fi

BENCH_ROOT="$(cd "$BENCH_ROOT" && pwd)"
BENCH_NAME="${BENCH_NAME:-$(basename "$BENCH_ROOT")}"

require_array() {
    local name="$1"
    eval "set -- \"\${$name[@]-}\""
    [ "$#" -gt 0 ]
}

rel_path() {
    printf '%s/%s\n' "$BENCH_ROOT" "$1"
}

edr_cmd() {
    "$EDR" --root "$BENCH_ROOT" "$@"
}

median() {
    local -a vals=("$@")
    local n=${#vals[@]}
    if [ "$n" -eq 0 ]; then
        echo 0
        return
    fi
    local sorted_vals
    sorted_vals=($(printf '%s\n' "${vals[@]}" | sort -n))
    echo "${sorted_vals[$((n / 2))]}"
}

pct_round() {
    local native_bytes="$1" edr_bytes="$2"
    awk -v native="$native_bytes" -v edr="$edr_bytes" '
        BEGIN {
            if (native <= 0) {
                print 0
                exit
            }
            pct = ((native - edr) * 100) / native
            if (pct >= 0) {
                printf "%d", int(pct + 0.5)
            } else {
                printf "%d", int(pct - 0.5)
            }
        }
    '
}

native_read_bytes() {
    wc -c < "$1" | tr -d ' '
}

grep_include_args() {
    local ext
    for ext in "${ORIENT_GLOBS[@]-}"; do
        [ -n "$ext" ] && printf -- '--include\n%s\n' "$ext"
    done
}

native_grep_bytes() {
    local pattern="$1"; shift
    (
        cd "$BENCH_ROOT"
        local -a inc=()
        while IFS= read -r arg; do
            [ -n "$arg" ] && inc+=("$arg")
        done < <(grep_include_args)
        grep -rn --exclude-dir='.edr' --exclude-dir='.git' "${inc[@]}" "$pattern" "$@" 2>/dev/null | wc -c | tr -d ' '
    )
}

native_grep_files() {
    local pattern="$1"; shift
    (
        cd "$BENCH_ROOT"
        local -a inc=()
        while IFS= read -r arg; do
            [ -n "$arg" ] && inc+=("$arg")
        done < <(grep_include_args)
        grep -rl --exclude-dir='.edr' --exclude-dir='.git' "${inc[@]}" "$pattern" "$@" 2>/dev/null | sort -u || true
    )
}

# Read ALL files that grep matches — no artificial cap.
# When an agent greps for a symbol and needs to understand every reference,
# it reads every matched file. Capping at an arbitrary number (e.g., 3)
# would undercount the native baseline.

native_grep_followup_read_bytes() {
    local pattern="$1"; shift
    local total=0
    local file
    while IFS= read -r file; do
        [ -n "$file" ] || continue
        total=$((total + $(native_read_bytes "$(rel_path "$file")")))
    done < <(native_grep_files "$pattern" "$@")
    echo "$total"
}

native_grep_followup_read_calls() {
    local pattern="$1"; shift
    local count=0
    local file
    while IFS= read -r file; do
        [ -n "$file" ] || continue
        count=$((count + 1))
    done < <(native_grep_files "$pattern" "$@")
    echo "$count"
}

native_glob_bytes() {
    (
        cd "$BENCH_ROOT"
        find "$1" \
            -path '*/.edr' -prune -o \
            -path '*/.git' -prune -o \
            -name "$2" -type f -print 2>/dev/null | wc -c | tr -d ' '
    )
}

edr_median_bytes() {
    local -a bytes_arr=()
    local warmup_out
    warmup_out=$("$@" 2>&1) || {
        echo "  WARN: edr command failed: $*" >&2
        echo "        output: ${warmup_out:0:200}" >&2
    }
    for ((i = 0; i < ITERS; i++)); do
        local out
        out=$("$@" 2>/dev/null) || true
        bytes_arr+=("${#out}")
    done
    median "${bytes_arr[@]}"
}

report() {
    local label="$1" native_bytes="$2" native_calls="$3" edr_bytes="$4" edr_calls="$5"
    local saved pct
    saved=$((native_bytes - edr_bytes))
    pct=$(pct_round "$native_bytes" "$edr_bytes")
    if [ "$saved" -ge 0 ]; then
        printf "  %-14s │ %6dB × %d calls │ %6dB × %d calls │ %4d%% │ -%dB\n" \
            "$label" "$native_bytes" "$native_calls" "$edr_bytes" "$edr_calls" "$pct" "$saved"
    else
        printf "  %-14s │ %6dB × %d calls │ %6dB × %d calls │ %4d%% │ +%dB\n" \
            "$label" "$native_bytes" "$native_calls" "$edr_bytes" "$edr_calls" "$pct" "$((-saved))"
    fi
}

skip() {
    printf "  %-14s │ %-19s │ %-19s │ %5s │ %s\n" "$1" "skipped" "skipped" "-" "-"
}

JSON_SCENARIOS="[]"
json_add() {
    local name="$1" native_bytes="$2" native_calls="$3" edr_bytes="$4" edr_calls="$5"
    local pct
    pct=$(pct_round "$native_bytes" "$edr_bytes")
    JSON_SCENARIOS=$(echo "$JSON_SCENARIOS" | jq \
        --arg name "$name" \
        --argjson nb "$native_bytes" --argjson nc "$native_calls" \
        --argjson eb "$edr_bytes" --argjson ec "$edr_calls" \
        --argjson pct "$pct" \
        '. + [{name: $name, native_bytes: $nb, native_calls: $nc, edr_bytes: $eb, edr_calls: $ec, savings_pct: $pct}]')
}

run_understand_api() {
    if [ -z "${API_READ_SPEC:-}" ] || [ -z "${API_FILE:-}" ]; then
        skip "Understand API"
        return
    fi
    local file native_bytes edr_bytes
    file="$(rel_path "$API_FILE")"
    native_bytes=$(native_read_bytes "$file")
    edr_bytes=$(edr_median_bytes edr_cmd read "$API_READ_SPEC" --signatures)
    report "Understand API" "$native_bytes" 1 "$edr_bytes" 1
    json_add "understand_api" "$native_bytes" 1 "$edr_bytes" 1
}

run_read_symbol() {
    if [ -z "${READ_SYMBOL_SPEC:-}" ] || [ -z "${READ_SYMBOL_FILE:-}" ]; then
        skip "Read symbol"
        return
    fi
    local file native_bytes edr_bytes
    file="$(rel_path "$READ_SYMBOL_FILE")"
    native_bytes=$(native_read_bytes "$file")
    edr_bytes=$(edr_median_bytes edr_cmd read "$READ_SYMBOL_SPEC")
    report "Read symbol" "$native_bytes" 1 "$edr_bytes" 1
    json_add "read_symbol" "$native_bytes" 1 "$edr_bytes" 1
}

run_find_refs() {
    if [ -z "${REFS_PATTERN:-}" ] || ! require_array REFS_ARGS; then
        skip "Find refs"
        return
    fi
    local grep_root grep_bytes read_bytes native_bytes native_calls edr_bytes
    grep_root="${REFS_GREP_ROOT:-$SCOPE_DIR}"
    grep_bytes=$(native_grep_bytes "$REFS_PATTERN" "$grep_root")
    read_bytes=$(native_grep_followup_read_bytes "$REFS_PATTERN" "$grep_root")
    native_bytes=$((grep_bytes + read_bytes))
    native_calls=$((1 + $(native_grep_followup_read_calls "$REFS_PATTERN" "$grep_root")))
    edr_bytes=$(edr_median_bytes edr_cmd refs "${REFS_ARGS[@]}")
    report "Find refs" "$native_bytes" "$native_calls" "$edr_bytes" 1
    json_add "find_refs" "$native_bytes" "$native_calls" "$edr_bytes" 1
}

run_search_context() {
    if [ -z "${SEARCH_PATTERN:-}" ]; then
        skip "Search+context"
        return
    fi
    local search_root native_bytes edr_bytes
    search_root="${SEARCH_ROOT:-$SCOPE_DIR}"
    native_bytes=$(
        cd "$BENCH_ROOT"
        local -a inc=()
        while IFS= read -r arg; do
            [ -n "$arg" ] && inc+=("$arg")
        done < <(grep_include_args)
        grep -rn -C3 --exclude-dir='.edr' --exclude-dir='.git' "${inc[@]}" "$SEARCH_PATTERN" "$search_root" 2>/dev/null | wc -c | tr -d ' '
    )
    edr_bytes=$(edr_median_bytes edr_cmd search "$SEARCH_PATTERN" --text --context 3 --budget "${SEARCH_BUDGET:-500}")
    report "Search+context" "$native_bytes" 1 "$edr_bytes" 1
    json_add "search_context" "$native_bytes" 1 "$edr_bytes" 1
}

run_orient_map() {
    if [ -z "${ORIENT_DIR:-}" ] || ! require_array ORIENT_GLOBS || ! require_array ORIENT_READ_FILES; then
        skip "Orient (map)"
        return
    fi
    local orient_root glob_bytes read_bytes native_bytes native_calls edr_bytes ext file
    orient_root="$ORIENT_DIR"
    glob_bytes=0
    for ext in "${ORIENT_GLOBS[@]}"; do
        glob_bytes=$((glob_bytes + $(native_glob_bytes "$orient_root" "$ext")))
    done
    read_bytes=0
    for file in "${ORIENT_READ_FILES[@]}"; do
        read_bytes=$((read_bytes + $(native_read_bytes "$(rel_path "$file")")))
    done
    native_bytes=$((glob_bytes + read_bytes))
    native_calls=$((1 + ${#ORIENT_READ_FILES[@]}))
    if [ "$ORIENT_DIR" = "." ]; then
        edr_bytes=$(edr_median_bytes edr_cmd map --budget "${ORIENT_BUDGET:-500}")
    else
        edr_bytes=$(edr_median_bytes edr_cmd map "${ORIENT_DIR}" --budget "${ORIENT_BUDGET:-500}")
    fi
    report "Orient (map)" "$native_bytes" "$native_calls" "$edr_bytes" 1
    json_add "orient_map" "$native_bytes" "$native_calls" "$edr_bytes" 1
}

run_edit_function() {
    if [ -z "${EDIT_FILE:-}" ] || [ -z "${EDIT_OLD_TEXT:-}" ] || [ -z "${EDIT_NEW_TEXT:-}" ]; then
        skip "Edit function"
        return
    fi
    local file native_read native_bytes native_calls edr_bytes out i
    local -a edr_bytes_arr
    file="$(rel_path "$EDIT_FILE")"
    native_read=$(native_read_bytes "$file")
    # Native workflow: read file + edit tool response (~negligible) + re-read to verify.
    # We count file bytes twice (read + verify). The edit tool's own response is small
    # enough to be noise on any real file.
    native_bytes=$((native_read * 2))
    native_calls=3
    # Use --no-verify: the native baseline doesn't run a build, so neither
    # should edr.  This keeps the comparison fair across all languages (Go,
    # Python, Ruby, TS) — some roots lack go.mod/package.json and verify
    # would fail with "could not auto-detect project type".
    cp "$file" "$file.bak"
    edr_bytes_arr=()
    for ((i = 0; i < ITERS; i++)); do
        cp "$file.bak" "$file"
        touch "$file"
        edr_cmd init >/dev/null 2>/dev/null || true
        out=$(edr_cmd -e "$EDIT_FILE" --old "$EDIT_OLD_TEXT" --new "$EDIT_NEW_TEXT" --no-verify 2>/dev/null) || true
        edr_bytes_arr+=("${#out}")
    done
    edr_bytes=$(median "${edr_bytes_arr[@]}")
    mv "$file.bak" "$file"
    edr_cmd init >/dev/null 2>/dev/null || true
    report "Edit function" "$native_bytes" "$native_calls" "$edr_bytes" 1
    json_add "edit_function" "$native_bytes" "$native_calls" "$edr_bytes" 1
}

run_add_method() {
    if [ -z "${WRITE_FILE:-}" ] || [ -z "${WRITE_INSIDE:-}" ] || [ -z "${WRITE_CONTENT:-}" ]; then
        skip "Add method"
        return
    fi
    local file native_bytes native_calls edr_bytes_arr out i
    file="$(rel_path "$WRITE_FILE")"
    native_bytes=$(native_read_bytes "$file")
    native_calls=2
    cp "$file" "$file.bak"
    edr_bytes_arr=()
    for ((i = 0; i < ITERS; i++)); do
        cp "$file.bak" "$file"
        touch "$file"
        edr_cmd init >/dev/null 2>/dev/null || true
        out=$(printf '%s' "$WRITE_CONTENT" | edr_cmd write "$WRITE_FILE" --inside "$WRITE_INSIDE" 2>/dev/null) || true
        edr_bytes_arr+=("${#out}")
    done
    edr_bytes=$(median "${edr_bytes_arr[@]}")
    mv "$file.bak" "$file"
    edr_cmd init >/dev/null 2>/dev/null || true
    report "Add method" "$native_bytes" "$native_calls" "$edr_bytes" 1
    json_add "add_method" "$native_bytes" "$native_calls" "$edr_bytes" 1
}

run_multi_file_read() {
    if ! require_array MULTI_READ_FILES; then
        skip "Multi-file read"
        return
    fi
    local native_bytes native_calls edr_bytes file
    native_bytes=0
    for file in "${MULTI_READ_FILES[@]}"; do
        native_bytes=$((native_bytes + $(native_read_bytes "$(rel_path "$file")")))
    done
    native_calls=${#MULTI_READ_FILES[@]}
    edr_bytes=$(edr_median_bytes edr_cmd read "${MULTI_READ_FILES[@]}" --budget "${MULTI_READ_BUDGET:-500}")
    report "Multi-file read" "$native_bytes" "$native_calls" "$edr_bytes" 1
    json_add "multi_file_read" "$native_bytes" "$native_calls" "$edr_bytes" 1
}

run_explore_symbol() {
    if [ -z "${EXPLORE_PATTERN:-}" ] || ! require_array EXPLORE_ARGS || ! require_array EXPLORE_NATIVE_READ_FILES; then
        skip "Explore symbol"
        return
    fi
    local grep_root grep_bytes caller_reads native_bytes native_calls edr_bytes file
    grep_root="${EXPLORE_GREP_ROOT:-$SCOPE_DIR}"
    grep_bytes=$(native_grep_bytes "$EXPLORE_PATTERN" "$grep_root")
    caller_reads=0
    for file in "${EXPLORE_NATIVE_READ_FILES[@]}"; do
        caller_reads=$((caller_reads + $(native_read_bytes "$(rel_path "$file")")))
    done
    native_bytes=$((grep_bytes + caller_reads))
    native_calls=$((1 + ${#EXPLORE_NATIVE_READ_FILES[@]}))
    edr_bytes=$(edr_median_bytes edr_cmd explore "${EXPLORE_ARGS[@]}" --body --callers --deps)
    report "Explore symbol" "$native_bytes" "$native_calls" "$edr_bytes" 1
    json_add "explore_symbol" "$native_bytes" "$native_calls" "$edr_bytes" 1
}

edr_cmd init >/dev/null 2>/dev/null

echo "=================================================================="
echo "  NATIVE TOOLS vs EDR (${ITERS} iterations, median bytes)"
echo "=================================================================="
echo ""
echo "  Benchmark: ${BENCH_NAME}"
echo "  Root:      ${BENCH_ROOT}"
if [ -n "${SCOPE_DIR:-}" ]; then
    echo "  Scope:     ${SCOPE_DIR}"
fi
echo ""
echo "  'Native' = simulated Read/Edit/Grep/Glob output size."
echo "  These tools return raw file content; edr returns structured,"
echo "  budget-controlled, symbol-scoped JSON."
echo ""
printf "  %-14s │ %-19s │ %-19s │ %5s │ %s\n" "Scenario" "Native (Read/etc)" "edr" "Save%" "Delta"
printf "  %-14s─┼─%-19s─┼─%-19s─┼─%5s─┼─%s\n" "──────────────" "───────────────────" "───────────────────" "─────" "──────"

run_understand_api
run_read_symbol
run_find_refs
run_search_context
run_orient_map
run_edit_function
run_add_method
run_multi_file_read
run_explore_symbol

echo ""
printf "  %-14s─┼─%-19s─┼─%-19s─┼─%5s─┼─%s\n" "──────────────" "───────────────────" "───────────────────" "─────" "──────"

total_native=$(echo "$JSON_SCENARIOS" | jq '[.[].native_bytes] | add // 0')
total_edr=$(echo "$JSON_SCENARIOS" | jq '[.[].edr_bytes] | add // 0')
total_native_calls=$(echo "$JSON_SCENARIOS" | jq '[.[].native_calls] | add // 0')
total_edr_calls=$(echo "$JSON_SCENARIOS" | jq '[.[].edr_calls] | add // 0')

total_saved=$((total_native - total_edr))
total_pct=$(pct_round "$total_native" "$total_edr")

if [ "$total_saved" -ge 0 ]; then
    printf "  %-14s │ %6dB × %d calls │ %6dB × %d calls │ %4d%% │ -%dB\n" \
        "TOTAL" "$total_native" "$total_native_calls" "$total_edr" "$total_edr_calls" "$total_pct" "$total_saved"
else
    printf "  %-14s │ %6dB × %d calls │ %6dB × %d calls │ %4d%% │ +%dB\n" \
        "TOTAL" "$total_native" "$total_native_calls" "$total_edr" "$total_edr_calls" "$total_pct" "$((-total_saved))"
fi

echo ""
echo "  Native tools: ${total_native_calls} calls, ${total_native}B total response"
echo "  edr:          ${total_edr_calls} calls, ${total_edr}B total response"
echo "  Savings:      ${total_saved}B (${total_pct}%), $((total_native_calls - total_edr_calls)) fewer calls"

echo ""
echo ""
echo "=================================================================="
echo "  JSON SUMMARY"
echo "=================================================================="
echo "$JSON_SCENARIOS" | jq \
    --arg name "$BENCH_NAME" \
    --arg root "$BENCH_ROOT" \
    --arg scope "${SCOPE_DIR:-}" \
    --arg profile "$PROFILE" \
    --argjson iters "$ITERS" \
    '{
        benchmark: "native_comparison",
        benchmark_name: $name,
        benchmark_root: $root,
        scope_dir: $scope,
        profile: $profile,
        iterations: $iters,
        scenarios: .,
        totals: {
            native_bytes: ([.[].native_bytes] | add // 0),
            edr_bytes: ([.[].edr_bytes] | add // 0),
            savings_pct: (([.[].native_bytes] | add // 0) as $tn | ([.[].edr_bytes] | add // 0) as $te |
                if $tn > 0 then ((($tn - $te) * 100 / $tn) | round) else 0 end),
            native_calls: ([.[].native_calls] | add // 0),
            edr_calls: ([.[].edr_calls] | add // 0)
        }
    }'
