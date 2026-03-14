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

# Support both .sh profiles (sourced directly) and .json scenarios (converted).
if [[ "$PROFILE" == *.json ]]; then
    eval "$(python3 "$SCRIPT_DIR/json_to_shell.py" "$PROFILE" "${BASE_DIR:-/tmp}")"
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
    "$EDR" -r "$BENCH_ROOT" "$@"
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
    cat -n "$1" | wc -c | tr -d ' '
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

# Cap follow-up reads to MAX_FOLLOWUP_READS files (default 3).
# A real agent reads grep output first and opens a few relevant files, not all.
MAX_FOLLOWUP_READS="${MAX_FOLLOWUP_READS:-3}"

native_grep_followup_read_bytes() {
    local pattern="$1"; shift
    local total=0 count=0
    local file
    while IFS= read -r file; do
        [ -n "$file" ] || continue
        [ "$count" -ge "$MAX_FOLLOWUP_READS" ] && break
        total=$((total + $(native_read_bytes "$(rel_path "$file")")))
        count=$((count + 1))
    done < <(native_grep_files "$pattern" "$@")
    echo "$total"
}

native_grep_followup_read_calls() {
    local pattern="$1"; shift
    local count=0
    local file
    while IFS= read -r file; do
        [ -n "$file" ] || continue
        [ "$count" -ge "$MAX_FOLLOWUP_READS" ] && break
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
    JSON_SCENARIOS=$(echo "$JSON_SCENARIOS" | NAME="$name" NB="$native_bytes" NC="$native_calls" EB="$edr_bytes" EC="$edr_calls" PCT="$pct" python3 -c "
import json,sys,os
s=json.load(sys.stdin)
s.append({
    'name': os.environ['NAME'],
    'native_bytes': int(os.environ['NB']),
    'native_calls': int(os.environ['NC']),
    'edr_bytes': int(os.environ['EB']),
    'edr_calls': int(os.environ['EC']),
    'savings_pct': int(os.environ['PCT'])
})
print(json.dumps(s))
")
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
    native_bytes=$((native_read * 2 + ${NATIVE_EDIT_CONFIRM_BYTES:-200}))
    native_calls=3
    edr_bytes_arr=()
    printf '%s' "$EDIT_NEW_TEXT" | edr_cmd edit "$EDIT_FILE" --old_text "$EDIT_OLD_TEXT" --dry-run >/dev/null 2>/dev/null || true
    for ((i = 0; i < ITERS; i++)); do
        out=$(printf '%s' "$EDIT_NEW_TEXT" | edr_cmd edit "$EDIT_FILE" --old_text "$EDIT_OLD_TEXT" --dry-run 2>/dev/null) || true
        edr_bytes_arr+=("${#out}")
    done
    edr_bytes=$(median "${edr_bytes_arr[@]}")
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
    native_bytes=$(($(native_read_bytes "$file") + ${NATIVE_EDIT_CONFIRM_BYTES:-200}))
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

total_native=0
total_edr=0
total_native_calls=0
total_edr_calls=0
for row in $(echo "$JSON_SCENARIOS" | python3 -c "
import json,sys
for s in json.load(sys.stdin):
    print(f\"{s['native_bytes']},{s['native_calls']},{s['edr_bytes']},{s['edr_calls']}\")
"); do
    IFS=',' read -r nb nc eb ec <<< "$row"
    total_native=$((total_native + nb))
    total_edr=$((total_edr + eb))
    total_native_calls=$((total_native_calls + nc))
    total_edr_calls=$((total_edr_calls + ec))
done

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
echo "$JSON_SCENARIOS" | \
    BENCH_NAME="$BENCH_NAME" BENCH_ROOT="$BENCH_ROOT" SCOPE_DIR="${SCOPE_DIR:-}" PROFILE="$PROFILE" ITERS="$ITERS" \
    python3 -c "
import json, sys, os
data = json.load(sys.stdin)
tn = sum(s['native_bytes'] for s in data)
te = sum(s['edr_bytes'] for s in data)
print(json.dumps({
    'benchmark': 'native_comparison',
    'benchmark_name': os.environ['BENCH_NAME'],
    'benchmark_root': os.environ['BENCH_ROOT'],
    'scope_dir': os.environ['SCOPE_DIR'],
    'profile': os.environ['PROFILE'],
    'iterations': int(os.environ['ITERS']),
    'scenarios': data,
    'totals': {
        'native_bytes': tn,
        'edr_bytes': te,
        'savings_pct': round((tn-te)*100/tn) if tn else 0,
        'native_calls': sum(s['native_calls'] for s in data),
        'edr_calls': sum(s['edr_calls'] for s in data),
    }
}, indent=2))
"
