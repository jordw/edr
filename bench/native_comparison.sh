#!/usr/bin/env bash
# Benchmark: edr vs native tools (Read, Edit, Grep, Glob)
#
# Simulates what built-in Claude Code tools return and compares response bytes.
# Native tools can't do symbol-scoped reads or budget-controlled output,
# so we measure the actual bytes an agent would consume in each workflow.
#
# Scenarios:
#   1. "Understand a class" — Read tool (whole file) vs edr read --signatures
#   2. "Find usages" — Grep tool vs edr search / edr refs
#   3. "Read, edit, verify" — Read+Edit+Read vs edr edit with inline diff
#   4. "Orient in codebase" — Glob+multiple Reads vs edr map
#   5. "Add a method" — Read+Edit vs edr write --inside
set -euo pipefail

EDR="${EDR:-./edr}"
ITERS="${ITERS:-5}"

$EDR init 2>/dev/null >/dev/null

# ---------------------------------------------------------------------------
# Timing
# ---------------------------------------------------------------------------
if date +%s%N 2>/dev/null | grep -qv N; then
    now_ns() { date +%s%N; }
else
    now_ns() { python3 -c 'import time; print(int(time.time()*1e9))'; }
fi

median() {
    local -a vals=("$@")
    local n=${#vals[@]}
    local sorted_vals
    sorted_vals=($(printf '%s\n' "${vals[@]}" | sort -n))
    echo "${sorted_vals[$((n / 2))]}"
}
min_val() { printf '%s\n' "$@" | sort -n | head -1; }
max_val() { printf '%s\n' "$@" | sort -n | tail -1; }

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

# ---------------------------------------------------------------------------
# Simulate native tool output sizes
# ---------------------------------------------------------------------------

# Read tool: returns file content with line numbers (cat -n format)
native_read_bytes() {
    cat -n "$1" | wc -c | tr -d ' '
}

# Read tool with line range
native_read_range_bytes() {
    local file="$1" start="$2" end="$3"
    sed -n "${start},${end}p" "$file" | cat -n | wc -c | tr -d ' '
}

# Grep tool: returns matching lines with file:line:content format
native_grep_bytes() {
    local pattern="$1"; shift
    grep -rn "$pattern" "$@" 2>/dev/null | wc -c | tr -d ' '
}

# Files an agent would likely open after a grep to inspect real usages.
native_grep_files() {
    local pattern="$1"; shift
    grep -rl "$pattern" "$@" 2>/dev/null | sort -u || true
}

# Sum full-file Read tool output for each unique grep match file.
native_grep_followup_read_bytes() {
    local pattern="$1"; shift
    local total=0
    local file
    while IFS= read -r file; do
        [ -n "$file" ] || continue
        total=$((total + $(native_read_bytes "$file")))
    done < <(native_grep_files "$pattern" "$@")
    echo "$total"
}

# Count follow-up Read calls needed after grep.
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

# Glob tool: returns file paths
native_glob_bytes() {
    find "$1" -name "$2" -type f 2>/dev/null | wc -c | tr -d ' '
}

# Measure edr command N times, return median bytes
edr_median_bytes() {
    local -a bytes_arr=()
    "$@" >/dev/null 2>/dev/null || true  # warmup
    for ((i = 0; i < ITERS; i++)); do
        local out
        out=$("$@" 2>/dev/null) || true
        bytes_arr+=("${#out}")
    done
    median "${bytes_arr[@]}"
}

# ---------------------------------------------------------------------------
# Report helper
# ---------------------------------------------------------------------------
scenario_num=0
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

JSON_SCENARIOS="[]"
json_add() {
    local name="$1" native_bytes="$2" native_calls="$3" edr_bytes="$4" edr_calls="$5"
    local saved=$((native_bytes - edr_bytes))
    local pct
    pct=$(pct_round "$native_bytes" "$edr_bytes")
    JSON_SCENARIOS=$(echo "$JSON_SCENARIOS" | python3 -c "
import json,sys
s=json.load(sys.stdin)
s.append({'name':'$name','native_bytes':$native_bytes,'native_calls':$native_calls,'edr_bytes':$edr_bytes,'edr_calls':$edr_calls,'savings_pct':$pct})
print(json.dumps(s))
")
}

echo "=================================================================="
echo "  NATIVE TOOLS vs EDR (${ITERS} iterations, median bytes)"
echo "=================================================================="
echo ""
echo "  'Native' = simulated Read/Edit/Grep/Glob output size."
echo "  These tools return raw file content; edr returns structured,"
echo "  budget-controlled, symbol-scoped JSON."
echo ""
printf "  %-14s │ %-19s │ %-19s │ %5s │ %s\n" "Scenario" "Native (Read/etc)" "edr" "Save%" "Delta"
printf "  %-14s─┼─%-19s─┼─%-19s─┼─%5s─┼─%s\n" "──────────────" "───────────────────" "───────────────────" "─────" "──────"

# ============================================================
# SCENARIO 1: Understand a class API
# ============================================================
# Native: Read the whole file (agent must read everything to find the class)
# edr: read file:Class --signatures (just the API surface)

FILE="bench/testdata/lib/scheduler.py"

native_bytes=$(native_read_bytes "$FILE")
edr_bytes=$(edr_median_bytes $EDR read "$FILE:Scheduler" --signatures)
report "Understand API" "$native_bytes" 1 "$edr_bytes" 1
json_add "understand_api" "$native_bytes" 1 "$edr_bytes" 1

# ============================================================
# SCENARIO 2: Read a specific symbol
# ============================================================
# Native: Read whole file (no symbol extraction), agent parses mentally
# edr: read file:symbol (just that function)

native_bytes=$(native_read_bytes "$FILE")
edr_bytes=$(edr_median_bytes $EDR read "$FILE:_execute_task")
report "Read symbol" "$native_bytes" 1 "$edr_bytes" 1
json_add "read_symbol" "$native_bytes" 1 "$edr_bytes" 1

# ============================================================
# SCENARIO 3: Find usages of a function
# ============================================================
# Native: Grep for the name across all files, then open each matched file
# to confirm which hits are real usages.
# edr: refs (import-aware, structured)

PATTERN="_execute_task"
grep_bytes=$(native_grep_bytes "$PATTERN" bench/testdata/)
read_bytes=$(native_grep_followup_read_bytes "$PATTERN" bench/testdata/)
native_bytes=$((grep_bytes + read_bytes))
native_calls=$((1 + $(native_grep_followup_read_calls "$PATTERN" bench/testdata)))
edr_bytes=$(edr_median_bytes $EDR refs "$PATTERN")
report "Find refs" "$native_bytes" "$native_calls" "$edr_bytes" 1
json_add "find_refs" "$native_bytes" "$native_calls" "$edr_bytes" 1

# ============================================================
# SCENARIO 4: Search for a pattern with context
# ============================================================
# Native: Grep -C3 across all files
# edr: search --text --context 3 --budget 500

PATTERN="retry"
native_bytes=$(grep -rn -C3 "$PATTERN" bench/testdata/ 2>/dev/null | wc -c | tr -d ' ')
edr_bytes=$(edr_median_bytes $EDR search "$PATTERN" --text --context 3 --budget 500)
report "Search+context" "$native_bytes" 1 "$edr_bytes" 1
json_add "search_context" "$native_bytes" 1 "$edr_bytes" 1

# ============================================================
# SCENARIO 5: Orient in codebase (map)
# ============================================================
# Native: Glob to find files + Read each file to scan for symbols
# edr: map --budget 500

TESTDATA="bench/testdata"
glob_bytes=0
for ext in "*.py" "*.go" "*.java" "*.rb" "*.js" "*.tsx" "*.rs" "*.c"; do
    b=$(native_glob_bytes "$TESTDATA" "$ext")
    glob_bytes=$((glob_bytes + b))
done

# Agent would need to read at least 3-4 files to understand structure
read_bytes=0
for f in "$TESTDATA/lib/scheduler.py" "$TESTDATA/lib/TaskProcessor.java" "$TESTDATA/internal/worker.go" "$TESTDATA/main.go"; do
    read_bytes=$((read_bytes + $(native_read_bytes "$f")))
done
native_bytes=$((glob_bytes + read_bytes))
native_calls=5  # 1 glob + 4 reads

edr_bytes=$(edr_median_bytes $EDR map --budget 500)
report "Orient (map)" "$native_bytes" "$native_calls" "$edr_bytes" 1
json_add "orient_map" "$native_bytes" "$native_calls" "$edr_bytes" 1

# ============================================================
# SCENARIO 6: Edit a function (read → edit → verify)
# ============================================================
# Native: Read file (to find the code) + Edit (old/new text) + Read again (verify)
# edr: edit --old_text --new_text (returns diff inline, auto re-indexes)

FILE="bench/testdata/lib/scheduler.py"
OLD_TEXT="self._running = True"
NEW_TEXT="self._running = False"

# Native: 2 Reads (before + after to verify) + 1 Edit call
native_read=$(native_read_bytes "$FILE")
# Edit tool returns ~200B confirmation
native_edit_confirm=200
native_bytes=$((native_read * 2 + native_edit_confirm))
native_calls=3

# edr edit reads new_text from stdin; --dry-run previews without applying
edr_bytes=$(edr_median_bytes bash -c "echo '$NEW_TEXT' | $EDR edit '$FILE' --old_text '$OLD_TEXT' --dry-run")
report "Edit function" "$native_bytes" "$native_calls" "$edr_bytes" 1
json_add "edit_function" "$native_bytes" "$native_calls" "$edr_bytes" 1

# ============================================================
# SCENARIO 7: Add a method to a class
# ============================================================
# Native: Read file (find insertion point) + Edit (add text) = 2 calls
# edr: write --inside = 1 call, no read needed

FILE="bench/testdata/lib/scheduler.py"
native_bytes=$(($(native_read_bytes "$FILE") + native_edit_confirm))
native_calls=2

NEW_METHOD='def drain(self, timeout: float = 5.0) -> int:
    """Drain remaining tasks."""
    return 0'

cp "$FILE" "$FILE.bak"
edr_bytes_arr=()
for ((i = 0; i < ITERS; i++)); do
    cp "$FILE.bak" "$FILE"
    $EDR init 2>/dev/null >/dev/null
    out=$(echo "$NEW_METHOD" | $EDR write "$FILE" --inside Scheduler 2>/dev/null) || true
    edr_bytes_arr+=("${#out}")
done
edr_bytes=$(median "${edr_bytes_arr[@]}")
mv "$FILE.bak" "$FILE"
$EDR init 2>/dev/null >/dev/null

report "Add method" "$native_bytes" "$native_calls" "$edr_bytes" 1
json_add "add_method" "$native_bytes" "$native_calls" "$edr_bytes" 1

# ============================================================
# SCENARIO 8: Multi-file read
# ============================================================
# Native: 3 separate Read calls
# edr: 1 read call with multiple args + budget

FILE1="bench/testdata/lib/scheduler.py"
FILE2="bench/testdata/lib/TaskProcessor.java"
FILE3="bench/testdata/internal/worker.go"

native_bytes=$(($(native_read_bytes "$FILE1") + $(native_read_bytes "$FILE2") + $(native_read_bytes "$FILE3")))
native_calls=3

edr_bytes=$(edr_median_bytes $EDR read "$FILE1" "$FILE2" "$FILE3" --budget 500)
report "Multi-file read" "$native_bytes" "$native_calls" "$edr_bytes" 1
json_add "multi_file_read" "$native_bytes" "$native_calls" "$edr_bytes" 1

# ============================================================
# SCENARIO 9: Explore a symbol (body + callers + deps)
# ============================================================
# Native: Grep for symbol + Read each file that references it
# edr: explore --body --callers --deps (1 call)

SYMBOL="_execute_task"
grep_bytes=$(native_grep_bytes "$SYMBOL" bench/testdata/)
# Agent reads ~2 files to understand callers
caller_reads=$(($(native_read_bytes "bench/testdata/lib/scheduler.py") + $(native_read_bytes "bench/testdata/main.go")))
native_bytes=$((grep_bytes + caller_reads))
native_calls=3

edr_bytes=$(edr_median_bytes $EDR explore "$SYMBOL" --body --callers --deps)
report "Explore symbol" "$native_bytes" "$native_calls" "$edr_bytes" 1
json_add "explore_symbol" "$native_bytes" "$native_calls" "$edr_bytes" 1

# ============================================================
# Totals
# ============================================================
echo ""
printf "  %-14s─┼─%-19s─┼─%-19s─┼─%5s─┼─%s\n" "──────────────" "───────────────────" "───────────────────" "─────" "──────"

total_native=0 total_edr=0 total_native_calls=0 total_edr_calls=0
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

# ============================================================
# JSON summary
# ============================================================
echo ""
echo ""
echo "=================================================================="
echo "  JSON SUMMARY"
echo "=================================================================="
echo "$JSON_SCENARIOS" | python3 -c "
import json, sys
data = json.load(sys.stdin)
tn = sum(s['native_bytes'] for s in data)
te = sum(s['edr_bytes'] for s in data)
pct = (tn-te)*100//tn if tn else 0
print(json.dumps({
    'benchmark': 'native_comparison',
    'iterations': $ITERS,
    'scenarios': data,
    'totals': {'native_bytes': tn, 'edr_bytes': te, 'savings_pct': round((tn-te)*100/tn) if tn else 0,
               'native_calls': sum(s['native_calls'] for s in data),
               'edr_calls': sum(s['edr_calls'] for s in data)}
}, indent=2))
"
