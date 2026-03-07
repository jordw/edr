#!/usr/bin/env bash
# Benchmark: Real agent workflow — "understand a class, then add a method"
# Compares traditional approach vs new features (--signatures, --depth, --inside)
#
# Improvements over v1:
#   - Multiple iterations (default 5) with min/median/max stats
#   - Warmup run before measurement
#   - Correctness verification (edits actually applied)
#   - Timing via date +%s%N (no python3 subprocess overhead)
#   - JSON summary at end for CI/regression tracking
#   - File restore without full re-index (just file-level re-index)
set -euo pipefail

EDR="${EDR:-./edr}"
ITERS="${ITERS:-5}"

$EDR init 2>/dev/null >/dev/null

# ---------------------------------------------------------------------------
# Timing helpers — nanosecond precision where available
# ---------------------------------------------------------------------------
if date +%s%N 2>/dev/null | grep -qv N; then
    now_ns() { date +%s%N; }
else
    # macOS: fall back to python3 (one-time cost per call)
    now_ns() { python3 -c 'import time; print(int(time.time()*1e9))'; }
fi

# ---------------------------------------------------------------------------
# Statistics helpers
# ---------------------------------------------------------------------------
sorted() { printf '%s\n' "$@" | sort -n; }

median() {
    local -a vals=("$@")
    local n=${#vals[@]}
    local sorted_vals
    sorted_vals=($(printf '%s\n' "${vals[@]}" | sort -n))
    echo "${sorted_vals[$((n / 2))]}"
}

min_val() { printf '%s\n' "$@" | sort -n | head -1; }
max_val() { printf '%s\n' "$@" | sort -n | tail -1; }

# Measure a command N times, return "median_bytes median_ms min_bytes max_bytes min_ms max_ms"
measure_n() {
    local -a bytes_arr=() ms_arr=()

    # Warmup (not counted)
    "$@" >/dev/null 2>/dev/null || true

    for ((i = 0; i < ITERS; i++)); do
        local t1 t2 out
        t1=$(now_ns)
        out=$("$@" 2>/dev/null) || true
        t2=$(now_ns)
        bytes_arr+=("${#out}")
        ms_arr+=("$(( (t2 - t1) / 1000000 ))")
    done

    echo "$(median "${bytes_arr[@]}") $(median "${ms_arr[@]}") $(min_val "${bytes_arr[@]}") $(max_val "${bytes_arr[@]}") $(min_val "${ms_arr[@]}") $(max_val "${ms_arr[@]}")"
}

measure_stdin_n() {
    local input="$1"; shift
    local -a bytes_arr=() ms_arr=()

    # Warmup
    echo "$input" | "$@" >/dev/null 2>/dev/null || true

    for ((i = 0; i < ITERS; i++)); do
        local t1 t2 out
        t1=$(now_ns)
        out=$(echo "$input" | "$@" 2>/dev/null) || true
        t2=$(now_ns)
        bytes_arr+=("${#out}")
        ms_arr+=("$(( (t2 - t1) / 1000000 ))")
    done

    echo "$(median "${bytes_arr[@]}") $(median "${ms_arr[@]}") $(min_val "${bytes_arr[@]}") $(max_val "${bytes_arr[@]}") $(min_val "${ms_arr[@]}") $(max_val "${ms_arr[@]}")"
}

# Parse measure_n output
m_bytes()   { echo "$1" | awk '{print $1}'; }
m_ms()      { echo "$1" | awk '{print $2}'; }
m_range()   { echo "$1" | awk '{printf "%s-%sB %s-%sms", $3, $4, $5, $6}'; }

# Verify a file contains expected text
verify_contains() {
    local file="$1" expected="$2" label="$3"
    if ! grep -qF "$expected" "$file" 2>/dev/null; then
        echo "    ⚠ CORRECTNESS FAILURE: $label — expected text not found in $file" >&2
        return 1
    fi
}

TMPDIR=$(mktemp -d)
trap "rm -rf $TMPDIR" EXIT

# JSON accumulator
JSON_SCENARIOS="[]"

echo "=================================================================="
echo "  WORKFLOW BENCHMARK (${ITERS} iterations per measurement)"
echo "=================================================================="
echo ""
echo "  Task: Understand a class's API, inspect one method,"
echo "        then add a new method inside the class."
echo ""

# ============================================================
# SCENARIO 1: Python — Scheduler class
# ============================================================
FILE="bench/testdata/lib/scheduler.py"
CLASS="Scheduler"
METHOD="_execute_task"
NEW_METHOD='def drain(self, timeout: float = 5.0) -> int:
    """Drain remaining tasks, return count processed."""
    count = 0
    deadline = time.time() + timeout
    while self._queue and time.time() < deadline:
        task = heapq.heappop(self._queue)
        self._execute_task(task)
        count += 1
    return count'

echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "  Scenario 1: Python Scheduler (${CLASS})"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""

# --- Traditional ---
echo "  TRADITIONAL (read class + read method + write --after):"
r1=$(measure_n $EDR read "$FILE:$CLASS")
echo "    1. read $CLASS             → $(m_bytes "$r1")B median, $(m_ms "$r1")ms  [$(m_range "$r1")]"
r2=$(measure_n $EDR read "$FILE" "$METHOD")
echo "    2. read $METHOD         → $(m_bytes "$r2")B median, $(m_ms "$r2")ms  [$(m_range "$r2")]"

# Write needs restore each time — measure manually
old_write_bytes=() old_write_ms=()
echo "$NEW_METHOD" | $EDR write "$FILE" --after "$METHOD" >/dev/null 2>/dev/null || true  # warmup
cp "$FILE" "$FILE.bak" 2>/dev/null || true
for ((i = 0; i < ITERS; i++)); do
    cp "$FILE.bak" "$FILE"
    $EDR init 2>/dev/null >/dev/null
    t1=$(now_ns)
    out=$(echo "$NEW_METHOD" | $EDR write "$FILE" --after "$METHOD" 2>/dev/null) || true
    t2=$(now_ns)
    old_write_bytes+=("${#out}")
    old_write_ms+=("$(( (t2 - t1) / 1000000 ))")
done
# Verify correctness on last write
verify_contains "$FILE" "def drain" "traditional write --after" || true
r3="$(median "${old_write_bytes[@]}") $(median "${old_write_ms[@]}") $(min_val "${old_write_bytes[@]}") $(max_val "${old_write_bytes[@]}") $(min_val "${old_write_ms[@]}") $(max_val "${old_write_ms[@]}")"
echo "    3. write --after $METHOD → $(m_bytes "$r3")B median, $(m_ms "$r3")ms  [$(m_range "$r3")]"

old_total=$(( $(m_bytes "$r1") + $(m_bytes "$r2") + $(m_bytes "$r3") ))
old_ms_total=$(( $(m_ms "$r1") + $(m_ms "$r2") + $(m_ms "$r3") ))
printf "    TOTAL: 3 calls, %dB, %dms (median)\n" $old_total $old_ms_total

# Restore
mv "$FILE.bak" "$FILE"
$EDR init 2>/dev/null >/dev/null

# --- Progressive ---
echo ""
echo "  PROGRESSIVE (--signatures + --depth 2 + --inside):"
r4=$(measure_n $EDR read "$FILE:$CLASS" --signatures)
echo "    1. read $CLASS --signatures → $(m_bytes "$r4")B median, $(m_ms "$r4")ms  [$(m_range "$r4")]"
r5=$(measure_n $EDR read "$FILE" "$METHOD" --depth 2)
echo "    2. read $METHOD --depth 2→ $(m_bytes "$r5")B median, $(m_ms "$r5")ms  [$(m_range "$r5")]"

# Write --inside with restore
new_write_bytes=() new_write_ms=()
cp "$FILE" "$FILE.bak"
for ((i = 0; i < ITERS; i++)); do
    cp "$FILE.bak" "$FILE"
    $EDR init 2>/dev/null >/dev/null
    t1=$(now_ns)
    out=$(echo "$NEW_METHOD" | $EDR write "$FILE" --inside "$CLASS" --after stop 2>/dev/null) || true
    t2=$(now_ns)
    new_write_bytes+=("${#out}")
    new_write_ms+=("$(( (t2 - t1) / 1000000 ))")
done
verify_contains "$FILE" "def drain" "progressive write --inside" || true
r6="$(median "${new_write_bytes[@]}") $(median "${new_write_ms[@]}") $(min_val "${new_write_bytes[@]}") $(max_val "${new_write_bytes[@]}") $(min_val "${new_write_ms[@]}") $(max_val "${new_write_ms[@]}")"
echo "    3. write --inside $CLASS  → $(m_bytes "$r6")B median, $(m_ms "$r6")ms  [$(m_range "$r6")]"

new_total=$(( $(m_bytes "$r4") + $(m_bytes "$r5") + $(m_bytes "$r6") ))
new_ms_total=$(( $(m_ms "$r4") + $(m_ms "$r5") + $(m_ms "$r6") ))
printf "    TOTAL: 3 calls, %dB, %dms (median)\n" $new_total $new_ms_total

saved=$((old_total - new_total))
pct=$((saved * 100 / old_total))
echo ""
echo "    Savings: ${saved}B (${pct}%)"

# Restore
mv "$FILE.bak" "$FILE"
$EDR init 2>/dev/null >/dev/null

JSON_SCENARIOS=$(echo "$JSON_SCENARIOS" | python3 -c "
import json,sys
s=json.load(sys.stdin)
s.append({'name':'python_scheduler','traditional_bytes':$old_total,'progressive_bytes':$new_total,'savings_pct':$pct,'traditional_ms':$old_ms_total,'progressive_ms':$new_ms_total})
print(json.dumps(s))
")

# ============================================================
# SCENARIO 2: Java — TaskProcessor class
# ============================================================
FILE="bench/testdata/lib/TaskProcessor.java"
CLASS="TaskProcessor"
METHOD="processWithRetry"
NEW_METHOD='public CompletableFuture<Void> submitBatch(List<Map<String, Object>> tasks,
                                              String taskType, Duration timeout) {
    List<CompletableFuture<TaskResult>> futures = tasks.stream()
        .map(payload -> submit(taskType, payload, 3, timeout))
        .collect(Collectors.toList());
    return CompletableFuture.allOf(futures.toArray(new CompletableFuture[0]));
}'

echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "  Scenario 2: Java TaskProcessor (${CLASS})"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""

# --- Traditional ---
echo "  TRADITIONAL:"
r1=$(measure_n $EDR read "$FILE:$CLASS")
echo "    1. read $CLASS          → $(m_bytes "$r1")B median, $(m_ms "$r1")ms  [$(m_range "$r1")]"
r2=$(measure_n $EDR read "$FILE" "$METHOD")
echo "    2. read $METHOD     → $(m_bytes "$r2")B median, $(m_ms "$r2")ms  [$(m_range "$r2")]"

old_write_bytes=() old_write_ms=()
cp "$FILE" "$FILE.bak"
for ((i = 0; i < ITERS; i++)); do
    cp "$FILE.bak" "$FILE"
    $EDR init 2>/dev/null >/dev/null
    t1=$(now_ns)
    out=$(echo "$NEW_METHOD" | $EDR write "$FILE" --after shutdown 2>/dev/null) || true
    t2=$(now_ns)
    old_write_bytes+=("${#out}")
    old_write_ms+=("$(( (t2 - t1) / 1000000 ))")
done
verify_contains "$FILE" "submitBatch" "traditional write" || true
r3="$(median "${old_write_bytes[@]}") $(median "${old_write_ms[@]}") $(min_val "${old_write_bytes[@]}") $(max_val "${old_write_bytes[@]}") $(min_val "${old_write_ms[@]}") $(max_val "${old_write_ms[@]}")"
echo "    3. write --after shutdown    → $(m_bytes "$r3")B median, $(m_ms "$r3")ms  [$(m_range "$r3")]"

old_total=$(( $(m_bytes "$r1") + $(m_bytes "$r2") + $(m_bytes "$r3") ))
old_ms_total=$(( $(m_ms "$r1") + $(m_ms "$r2") + $(m_ms "$r3") ))
printf "    TOTAL: 3 calls, %dB, %dms (median)\n" $old_total $old_ms_total

mv "$FILE.bak" "$FILE"
$EDR init 2>/dev/null >/dev/null

# --- Progressive ---
echo ""
echo "  PROGRESSIVE:"
r4=$(measure_n $EDR read "$FILE:$CLASS" --signatures)
echo "    1. read $CLASS --signatures→ $(m_bytes "$r4")B median, $(m_ms "$r4")ms  [$(m_range "$r4")]"
r5=$(measure_n $EDR read "$FILE" "$METHOD" --depth 2)
echo "    2. read $METHOD --depth 2→ $(m_bytes "$r5")B median, $(m_ms "$r5")ms  [$(m_range "$r5")]"

new_write_bytes=() new_write_ms=()
cp "$FILE" "$FILE.bak"
for ((i = 0; i < ITERS; i++)); do
    cp "$FILE.bak" "$FILE"
    $EDR init 2>/dev/null >/dev/null
    t1=$(now_ns)
    out=$(echo "$NEW_METHOD" | $EDR write "$FILE" --inside "$CLASS" --after shutdown 2>/dev/null) || true
    t2=$(now_ns)
    new_write_bytes+=("${#out}")
    new_write_ms+=("$(( (t2 - t1) / 1000000 ))")
done
verify_contains "$FILE" "submitBatch" "progressive write" || true
r6="$(median "${new_write_bytes[@]}") $(median "${new_write_ms[@]}") $(min_val "${new_write_bytes[@]}") $(max_val "${new_write_bytes[@]}") $(min_val "${new_write_ms[@]}") $(max_val "${new_write_ms[@]}")"
echo "    3. write --inside $CLASS  → $(m_bytes "$r6")B median, $(m_ms "$r6")ms  [$(m_range "$r6")]"

new_total=$(( $(m_bytes "$r4") + $(m_bytes "$r5") + $(m_bytes "$r6") ))
new_ms_total=$(( $(m_ms "$r4") + $(m_ms "$r5") + $(m_ms "$r6") ))
printf "    TOTAL: 3 calls, %dB, %dms (median)\n" $new_total $new_ms_total

saved=$((old_total - new_total))
pct=$((saved * 100 / old_total))
echo ""
echo "    Savings: ${saved}B (${pct}%)"

mv "$FILE.bak" "$FILE"
$EDR init 2>/dev/null >/dev/null

JSON_SCENARIOS=$(echo "$JSON_SCENARIOS" | python3 -c "
import json,sys
s=json.load(sys.stdin)
s.append({'name':'java_taskprocessor','traditional_bytes':$old_total,'progressive_bytes':$new_total,'savings_pct':$pct,'traditional_ms':$old_ms_total,'progressive_ms':$new_ms_total})
print(json.dumps(s))
")

# ============================================================
# SCENARIO 3: Quick add (agent already knows the class)
# ============================================================
FILE="bench/testdata/lib/scheduler.py"
CLASS="DependencyGraph"
NEW_METHOD='def has_circular_dependency(self, task_id: str) -> bool:
    """Check if adding this task would create a circular dependency."""
    visited: set[str] = set()
    stack = list(self._forward.get(task_id, set()))
    while stack:
        current = stack.pop()
        if current == task_id:
            return True
        if current not in visited:
            visited.add(current)
            stack.extend(self._forward.get(current, set()))
    return False'

echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "  Scenario 3: Quick add (agent already knows the class)"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""

# --- Traditional: must read to find insertion point ---
echo "  TRADITIONAL (read class + write --after):"
r1=$(measure_n $EDR read "$FILE:$CLASS")
echo "    1. read $CLASS          → $(m_bytes "$r1")B median, $(m_ms "$r1")ms  [$(m_range "$r1")]"

old_write_bytes=() old_write_ms=()
cp "$FILE" "$FILE.bak"
for ((i = 0; i < ITERS; i++)); do
    cp "$FILE.bak" "$FILE"
    $EDR init 2>/dev/null >/dev/null
    t1=$(now_ns)
    out=$(echo "$NEW_METHOD" | $EDR write "$FILE" --after remove_task 2>/dev/null) || true
    t2=$(now_ns)
    old_write_bytes+=("${#out}")
    old_write_ms+=("$(( (t2 - t1) / 1000000 ))")
done
verify_contains "$FILE" "has_circular_dependency" "traditional quick add" || true
r2="$(median "${old_write_bytes[@]}") $(median "${old_write_ms[@]}") $(min_val "${old_write_bytes[@]}") $(max_val "${old_write_bytes[@]}") $(min_val "${old_write_ms[@]}") $(max_val "${old_write_ms[@]}")"
echo "    2. write --after remove_task → $(m_bytes "$r2")B median, $(m_ms "$r2")ms  [$(m_range "$r2")]"

old_total=$(( $(m_bytes "$r1") + $(m_bytes "$r2") ))
old_ms_total=$(( $(m_ms "$r1") + $(m_ms "$r2") ))
printf "    TOTAL: 2 calls, %dB, %dms (median)\n" $old_total $old_ms_total

mv "$FILE.bak" "$FILE"
$EDR init 2>/dev/null >/dev/null

# --- New: single call ---
echo ""
echo "  PROGRESSIVE (write --inside, no read needed):"
new_write_bytes=() new_write_ms=()
cp "$FILE" "$FILE.bak"
for ((i = 0; i < ITERS; i++)); do
    cp "$FILE.bak" "$FILE"
    $EDR init 2>/dev/null >/dev/null
    t1=$(now_ns)
    out=$(echo "$NEW_METHOD" | $EDR write "$FILE" --inside "$CLASS" 2>/dev/null) || true
    t2=$(now_ns)
    new_write_bytes+=("${#out}")
    new_write_ms+=("$(( (t2 - t1) / 1000000 ))")
done
verify_contains "$FILE" "has_circular_dependency" "progressive quick add" || true
r3="$(median "${new_write_bytes[@]}") $(median "${new_write_ms[@]}") $(min_val "${new_write_bytes[@]}") $(max_val "${new_write_bytes[@]}") $(min_val "${new_write_ms[@]}") $(max_val "${new_write_ms[@]}")"
echo "    1. write --inside $CLASS → $(m_bytes "$r3")B median, $(m_ms "$r3")ms  [$(m_range "$r3")]"

new_total=$(m_bytes "$r3")
new_ms_total=$(m_ms "$r3")
printf "    TOTAL: 1 call, %dB, %dms (median)\n" $new_total $new_ms_total

saved=$((old_total - new_total))
pct=$((saved * 100 / old_total))
echo ""
echo "    Savings: ${saved}B (${pct}%)"

mv "$FILE.bak" "$FILE"
$EDR init 2>/dev/null >/dev/null

JSON_SCENARIOS=$(echo "$JSON_SCENARIOS" | python3 -c "
import json,sys
s=json.load(sys.stdin)
s.append({'name':'quick_add','traditional_bytes':$old_total,'progressive_bytes':$new_total,'savings_pct':$pct,'traditional_ms':$old_ms_total,'progressive_ms':$new_ms_total})
print(json.dumps(s))
")

# ============================================================
# SCENARIO 4: Signatures read benchmark (bytes only, no writes)
# ============================================================
echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "  Scenario 4: --signatures vs full read"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""
printf "  %-40s %10s %10s %8s\n" "Container" "Full(B)" "Sigs(B)" "Savings"
printf "  %-40s %10s %10s %8s\n" "─────────" "───────" "───────" "───────"

for spec in \
    "bench/testdata/lib/scheduler.py:DependencyGraph" \
    "bench/testdata/lib/scheduler.py:Scheduler" \
    "bench/testdata/lib/TaskProcessor.java:TaskProcessor" \
    "bench/testdata/lib/TaskProcessor.java:TaskContext" \
    "bench/testdata/internal/worker.go:Worker" \
    "bench/testdata/internal/worker.go:WorkerPool" \
    "bench/testdata/lib/config.rb:PluginRegistry" \
    "bench/testdata/lib/config.rb:RetryPolicy"; do

    rfull=$(measure_n $EDR read "$spec")
    rsigs=$(measure_n $EDR read "$spec" --signatures)

    full_b=$(m_bytes "$rfull")
    sigs_b=$(m_bytes "$rsigs")
    if [ "$full_b" -gt 0 ]; then
        pct=$(( (full_b - sigs_b) * 100 / full_b ))
    else
        pct=0
    fi
    name=$(echo "$spec" | sed 's|bench/testdata/||')
    printf "  %-40s %10s %10s %7s%%\n" "$name" "$full_b" "$sigs_b" "$pct"
done

# ============================================================
# JSON summary
# ============================================================
echo ""
echo ""
echo "=================================================================="
echo "  JSON SUMMARY (for CI/regression tracking)"
echo "=================================================================="
echo "$JSON_SCENARIOS" | python3 -c "
import json, sys
data = json.load(sys.stdin)
print(json.dumps({'benchmark': 'workflow', 'iterations': $ITERS, 'scenarios': data}, indent=2))
"
