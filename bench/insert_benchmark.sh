#!/usr/bin/env bash
# Benchmark: --inside vs traditional read+edit for adding methods to containers
# Measures: response bytes (proxy for tokens), number of calls, and latency
#
# Multiple iterations with min/median/max, warmup, correctness checks.
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

# ---------------------------------------------------------------------------
# Stats
# ---------------------------------------------------------------------------
median() {
    local -a vals=("$@")
    local n=${#vals[@]}
    local sorted_vals
    sorted_vals=($(printf '%s\n' "${vals[@]}" | sort -n))
    echo "${sorted_vals[$((n / 2))]}"
}
min_val() { printf '%s\n' "$@" | sort -n | head -1; }
max_val() { printf '%s\n' "$@" | sort -n | tail -1; }

# ---------------------------------------------------------------------------
# Accumulators
# ---------------------------------------------------------------------------
TOTAL_OLD_BYTES=0
TOTAL_NEW_BYTES=0
TOTAL_OLD_CALLS=0
TOTAL_NEW_CALLS=0
TOTAL_OLD_MS=0
TOTAL_NEW_MS=0
JSON_TESTS="[]"

separator() {
    printf "%-18s %8s %8s %5s  |  %8s %8s %5s  |  %7s %s\n" "$@"
}

run_test() {
    local label="$1" file="$2" container="$3" content_file="$4" after="${5:-}"
    local content
    content=$(cat "$content_file")

    # Identify a unique string for correctness verification
    local verify_str
    verify_str=$(head -1 "$content_file" | sed 's/^[[:space:]]*//' | cut -c1-30)

    # === TRADITIONAL: read container + write --after ===
    local -a old_bytes_arr=() old_ms_arr=()
    local old_calls=2

    cp "$file" "$file.bak"

    # Warmup
    $EDR read "$file:$container" >/dev/null 2>/dev/null || true

    for ((i = 0; i < ITERS; i++)); do
        cp "$file.bak" "$file"
        $EDR init 2>/dev/null >/dev/null

        local t1 t2 r1 r2 total_bytes total_ms

        # Call 1: read container
        t1=$(now_ns)
        r1=$($EDR read "$file:$container" 2>/dev/null) || true
        t2=$(now_ns)
        total_bytes=${#r1}
        total_ms=$(( (t2 - t1) / 1000000 ))

        # Call 2: write --after
        t1=$(now_ns)
        if [ -n "$after" ]; then
            r2=$(cat "$content_file" | $EDR write "$file" --after "$after" 2>/dev/null) || true
        else
            r2=$(cat "$content_file" | $EDR write "$file" --after "$container" 2>/dev/null) || true
        fi
        t2=$(now_ns)
        total_bytes=$((total_bytes + ${#r2}))
        total_ms=$((total_ms + (t2 - t1) / 1000000))

        old_bytes_arr+=("$total_bytes")
        old_ms_arr+=("$total_ms")
    done

    # Verify correctness on last run
    if ! grep -qF "$verify_str" "$file" 2>/dev/null; then
        echo "  ⚠ CORRECTNESS FAILURE: $label traditional" >&2
    fi

    mv "$file.bak" "$file"
    $EDR init 2>/dev/null >/dev/null

    local old_bytes old_ms
    old_bytes=$(median "${old_bytes_arr[@]}")
    old_ms=$(median "${old_ms_arr[@]}")

    # === NEW: write --inside (single call) ===
    local -a new_bytes_arr=() new_ms_arr=()
    local new_calls=1

    cp "$file" "$file.bak"

    for ((i = 0; i < ITERS; i++)); do
        cp "$file.bak" "$file"
        $EDR init 2>/dev/null >/dev/null

        t1=$(now_ns)
        if [ -n "$after" ]; then
            r1=$(cat "$content_file" | $EDR write "$file" --inside "$container" --after "$after" 2>/dev/null) || true
        else
            r1=$(cat "$content_file" | $EDR write "$file" --inside "$container" 2>/dev/null) || true
        fi
        t2=$(now_ns)

        new_bytes_arr+=("${#r1}")
        new_ms_arr+=("$(( (t2 - t1) / 1000000 ))")
    done

    # Verify correctness on last run
    if ! grep -qF "$verify_str" "$file" 2>/dev/null; then
        echo "  ⚠ CORRECTNESS FAILURE: $label progressive" >&2
    fi

    mv "$file.bak" "$file"
    $EDR init 2>/dev/null >/dev/null

    local new_bytes new_ms
    new_bytes=$(median "${new_bytes_arr[@]}")
    new_ms=$(median "${new_ms_arr[@]}")

    # Savings
    local saved_bytes pct range_old range_new
    saved_bytes=$((old_bytes - new_bytes))
    if [ "$old_bytes" -gt 0 ]; then
        pct=$((saved_bytes * 100 / old_bytes))
    else
        pct=0
    fi
    range_old_ms="$(min_val "${old_ms_arr[@]}")-$(max_val "${old_ms_arr[@]}")ms"
    range_new_ms="$(min_val "${new_ms_arr[@]}")-$(max_val "${new_ms_arr[@]}")ms"

    separator "$label" "$old_bytes" "${old_ms}ms" "$old_calls" "$new_bytes" "${new_ms}ms" "$new_calls" "-${pct}%" "[$range_old_ms → $range_new_ms]"

    TOTAL_OLD_BYTES=$((TOTAL_OLD_BYTES + old_bytes))
    TOTAL_NEW_BYTES=$((TOTAL_NEW_BYTES + new_bytes))
    TOTAL_OLD_CALLS=$((TOTAL_OLD_CALLS + old_calls))
    TOTAL_NEW_CALLS=$((TOTAL_NEW_CALLS + new_calls))
    TOTAL_OLD_MS=$((TOTAL_OLD_MS + old_ms))
    TOTAL_NEW_MS=$((TOTAL_NEW_MS + new_ms))

    JSON_TESTS=$(echo "$JSON_TESTS" | python3 -c "
import json,sys
s=json.load(sys.stdin)
s.append({'name':'$label','old_bytes':$old_bytes,'new_bytes':$new_bytes,'savings_pct':$pct,'old_ms':$old_ms,'new_ms':$new_ms,'old_ms_range':'$range_old_ms','new_ms_range':'$range_new_ms'})
print(json.dumps(s))
")
}

# Create temp content files
TMPDIR=$(mktemp -d)
trap "rm -rf $TMPDIR" EXIT

cat > "$TMPDIR/py_method.py" <<'PY'
def has_circular_dependency(self, task_id: str) -> bool:
    """Check for circular dependency."""
    visited: set[str] = set()
    stack = list(self._forward.get(task_id, set()))
    while stack:
        current = stack.pop()
        if current == task_id:
            return True
        if current not in visited:
            visited.add(current)
            stack.extend(self._forward.get(current, set()))
    return False
PY

cat > "$TMPDIR/py_small.py" <<'PY'
def get_all_tasks(self) -> set[str]:
    """Return all task IDs in the graph."""
    return set(self._forward.keys()) | set(self._reverse.keys())
PY

cat > "$TMPDIR/java_method.java" <<'JAVA'
public boolean hasCapacity(int limit) {
    return taskQueue.size() < limit;
}
JAVA

cat > "$TMPDIR/java_small.java" <<'JAVA'
public int queueDepth() {
    return taskQueue.size();
}
JAVA

echo "cancelCh chan struct{}" > "$TMPDIR/go_field.go"
echo "LastTaskTime time.Time" > "$TMPDIR/go_field2.go"

cat > "$TMPDIR/rb_method.rb" <<'RB'
def clear!
  @plugins.clear
end
RB

cat > "$TMPDIR/rb_small.rb" <<'RB'
def reset!
  @current_attempt = 0
end
RB

echo "=================================================================="
echo "  INSERT BENCHMARK: --inside vs read+write (${ITERS} iterations)"
echo "=================================================================="
echo ""
separator "Language" "OldBytes" "OldMs" "Calls" "NewBytes" "NewMs" "Calls" "Savings" "Time(range)"
separator "────────" "────────" "─────" "─────" "────────" "─────" "─────" "───────" "───────────"

# Run tests
run_test "Python/class"   bench/testdata/lib/scheduler.py       DependencyGraph "$TMPDIR/py_method.py"
run_test "Python/after"   bench/testdata/lib/scheduler.py       DependencyGraph "$TMPDIR/py_small.py"  is_ready
run_test "Java/class"     bench/testdata/lib/TaskProcessor.java TaskProcessor    "$TMPDIR/java_method.java"
run_test "Java/after"     bench/testdata/lib/TaskProcessor.java TaskProcessor    "$TMPDIR/java_small.java" submit
run_test "Go/struct"      bench/testdata/internal/worker.go     WorkerPool       "$TMPDIR/go_field.go"
run_test "Go/struct2"     bench/testdata/internal/worker.go     WorkerStats      "$TMPDIR/go_field2.go"
run_test "Ruby/class"     bench/testdata/lib/config.rb          PluginRegistry   "$TMPDIR/rb_method.rb"
run_test "Ruby/class2"    bench/testdata/lib/config.rb          RetryPolicy      "$TMPDIR/rb_small.rb"

echo ""
separator "────────" "────────" "─────" "─────" "────────" "─────" "─────" "───────" "─────"

saved=$((TOTAL_OLD_BYTES - TOTAL_NEW_BYTES))
pct=$((saved * 100 / TOTAL_OLD_BYTES))
separator "TOTAL" "$TOTAL_OLD_BYTES" "${TOTAL_OLD_MS}ms" "$TOTAL_OLD_CALLS" "$TOTAL_NEW_BYTES" "${TOTAL_NEW_MS}ms" "$TOTAL_NEW_CALLS" "-${pct}%" ""

echo ""
echo "Traditional: $TOTAL_OLD_CALLS calls, ${TOTAL_OLD_BYTES}B response (median), ${TOTAL_OLD_MS}ms"
echo "With --inside: $TOTAL_NEW_CALLS calls, ${TOTAL_NEW_BYTES}B response (median), ${TOTAL_NEW_MS}ms"
echo "Savings: ${saved}B (${pct}%), $((TOTAL_OLD_CALLS - TOTAL_NEW_CALLS)) fewer calls"

# ============================================================
# JSON summary
# ============================================================
echo ""
echo ""
echo "=================================================================="
echo "  JSON SUMMARY"
echo "=================================================================="
echo "$JSON_TESTS" | python3 -c "
import json, sys
data = json.load(sys.stdin)
total_old = sum(t['old_bytes'] for t in data)
total_new = sum(t['new_bytes'] for t in data)
pct = (total_old - total_new) * 100 // total_old if total_old else 0
print(json.dumps({
    'benchmark': 'insert',
    'iterations': $ITERS,
    'tests': data,
    'totals': {
        'old_bytes': total_old,
        'new_bytes': total_new,
        'savings_pct': pct,
    }
}, indent=2))
"
