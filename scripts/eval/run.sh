#!/bin/bash
# Full eval: clone repos, build indexes, run ranking/speed/correctness checks.
#
# Usage:
#   ./scripts/eval/run.sh                    # run all
#   ./scripts/eval/run.sh --repo linux       # one repo
#   ./scripts/eval/run.sh --skip-clone       # skip clone/index step
#
# First run clones 9 repos (~15 min). After that, ~2 min for all checks.

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_BASE="${REPO_BASE:-$(cd "$SCRIPT_DIR/../.." && cd .. && pwd)}"
FILTER_REPO=""
SKIP_CLONE=false

while [[ $# -gt 0 ]]; do
    case "$1" in
        --repo) FILTER_REPO="$2"; shift 2 ;;
        --skip-clone) SKIP_CLONE=true; shift ;;
        *) shift ;;
    esac
done

GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[0;33m'
CYAN='\033[0;36m'
NC='\033[0m'

# --- Step 1: Ensure repos exist and are indexed ---
if ! $SKIP_CLONE; then
    echo -e "${CYAN}=== Step 1: Ensuring repos are cloned and indexed ===${NC}"

    while IFS= read -r repo_json; do
        name=$(echo "$repo_json" | python3 -c "import json,sys; print(json.loads(sys.stdin.read())['name'])")
        url=$(echo "$repo_json" | python3 -c "import json,sys; print(json.loads(sys.stdin.read())['url'])")
        shallow=$(echo "$repo_json" | python3 -c "import json,sys; print(json.loads(sys.stdin.read()).get('shallow', True))")

        if [[ -n "$FILTER_REPO" && "$name" != "$FILTER_REPO" ]]; then continue; fi
        if [[ "$url" == "self" ]]; then continue; fi

        repo_path="$REPO_BASE/$name"
        if [[ ! -d "$repo_path/.git" ]]; then
            echo "  Cloning $name..."
            if [[ "$shallow" == "True" ]]; then
                git clone --depth 1 "$url" "$repo_path" 2>&1 | tail -1 &
            else
                git clone "$url" "$repo_path" 2>&1 | tail -1 &
            fi
        fi
    done < <(python3 -c "import json; [print(json.dumps(r)) for r in json.load(open('$SCRIPT_DIR/repos.json'))]")
    wait

    # Index repos that need it
    while IFS= read -r repo_json; do
        name=$(echo "$repo_json" | python3 -c "import json,sys; print(json.loads(sys.stdin.read())['name'])")
        url=$(echo "$repo_json" | python3 -c "import json,sys; print(json.loads(sys.stdin.read())['url'])")

        if [[ -n "$FILTER_REPO" && "$name" != "$FILTER_REPO" ]]; then continue; fi

        if [[ "$url" == "self" ]]; then
            repo_path="$SCRIPT_DIR/../.."
        else
            repo_path="$REPO_BASE/$name"
        fi

        if [[ ! -d "$repo_path" ]]; then continue; fi

        # Check if index exists
        status_json=$(EDR_ROOT="$repo_path" edr status 2>/dev/null | head -1)
        has_index=$(echo "$status_json" | python3 -c "import json,sys; d=json.loads(sys.stdin.read()); print('yes' if 'index' in str(d) and 'stale' not in str(d.get('index','')) else 'no')" 2>/dev/null || echo "no")

        if [[ "$has_index" != "yes" ]]; then
            echo "  Indexing $name..."
            EDR_ROOT="$repo_path" edr index >/dev/null 2>&1 &
        fi
    done < <(python3 -c "import json; [print(json.dumps(r)) for r in json.load(open('$SCRIPT_DIR/repos.json'))]")
    wait
    echo ""
fi

# --- Step 2: Run assertions ---
echo -e "${CYAN}=== Step 2: Running eval tasks ===${NC}"

total=0
passed=0
failed=0
skipped=0
fail_details=""

while IFS= read -r task_json; do
    repo=$(echo "$task_json" | python3 -c "import json,sys; print(json.loads(sys.stdin.read())['repo'])")
    task=$(echo "$task_json" | python3 -c "import json,sys; print(json.loads(sys.stdin.read())['task'])")

    if [[ -n "$FILTER_REPO" && "$repo" != "$FILTER_REPO" ]]; then continue; fi

    # Resolve repo path
    if [[ "$repo" == "edr" ]]; then
        repo_path="$SCRIPT_DIR/../.."
    else
        repo_path="$REPO_BASE/$repo"
    fi

    if [[ ! -d "$repo_path" ]]; then
        echo -e "  ${YELLOW}SKIP${NC} $repo/$task — repo not found"
        skipped=$((skipped + 1))
        continue
    fi

    # Run each assertion
    task_ok=true
    assertions=$(echo "$task_json" | python3 -c "
import json, sys
task = json.loads(sys.stdin.read())
for a in task['assertions']:
    print(json.dumps(a))
")

    while IFS= read -r assert_json; do
        cmd=$(echo "$assert_json" | python3 -c "import json,sys; print(json.loads(sys.stdin.read())['cmd'])")
        assert_type=$(echo "$assert_json" | python3 -c "import json,sys; print(json.loads(sys.stdin.read())['assert'])")

        total=$((total + 1))

        # Run the command with timing
        start_ms=$(python3 -c "import time; print(int(time.time()*1000))")
        exit_code=0
        output=$(EDR_ROOT="$repo_path" edr $cmd 2>&1) || exit_code=$?
        end_ms=$(python3 -c "import time; print(int(time.time()*1000))")
        elapsed=$((end_ms - start_ms))

        header=$(echo "$output" | head -1)

        case "$assert_type" in
            exit_code)
                want=$(echo "$assert_json" | python3 -c "import json,sys; print(json.loads(sys.stdin.read())['code'])")
                if [[ "$exit_code" == "$want" ]]; then
                    passed=$((passed + 1))
                else
                    task_ok=false
                    fail_details="$fail_details\n  $repo/$task: edr $cmd → exit $exit_code, want $want"
                fi
                ;;
            resolves_to)
                pattern=$(echo "$assert_json" | python3 -c "import json,sys; print(json.loads(sys.stdin.read())['pattern'])")
                # Check: auto-resolved (file in header) OR shortlist #1 contains pattern
                if echo "$header" | grep -q "$pattern"; then
                    passed=$((passed + 1))
                elif echo "$output" | head -3 | grep -q "$pattern"; then
                    passed=$((passed + 1))
                else
                    task_ok=false
                    fail_details="$fail_details\n  $repo/$task: edr $cmd → did not resolve to $pattern"
                fi
                ;;
            top3_contains)
                pattern=$(echo "$assert_json" | python3 -c "import json,sys; print(json.loads(sys.stdin.read())['pattern'])")
                top3=$(echo "$output" | head -4)
                if echo "$top3" | grep -q "$pattern"; then
                    passed=$((passed + 1))
                else
                    task_ok=false
                    fail_details="$fail_details\n  $repo/$task: edr $cmd → top 3 missing $pattern"
                fi
                ;;
            header_contains)
                pattern=$(echo "$assert_json" | python3 -c "import json,sys; print(json.loads(sys.stdin.read())['pattern'])")
                if echo "$header" | grep -qi "$pattern"; then
                    passed=$((passed + 1))
                else
                    task_ok=false
                    fail_details="$fail_details\n  $repo/$task: edr $cmd → header missing $pattern"
                fi
                ;;
            output_contains)
                pattern=$(echo "$assert_json" | python3 -c "import json,sys; print(json.loads(sys.stdin.read())['pattern'])")
                if echo "$output" | grep -q "$pattern"; then
                    passed=$((passed + 1))
                else
                    task_ok=false
                    fail_details="$fail_details\n  $repo/$task: edr $cmd → output missing $pattern"
                fi
                ;;
            time_under)
                max_ms=$(echo "$assert_json" | python3 -c "import json,sys; print(json.loads(sys.stdin.read())['ms'])")
                if [[ "$elapsed" -le "$max_ms" ]]; then
                    passed=$((passed + 1))
                else
                    task_ok=false
                    fail_details="$fail_details\n  $repo/$task: edr $cmd → ${elapsed}ms, want <${max_ms}ms"
                fi
                ;;
        esac
    done <<< "$assertions"

    if $task_ok; then
        echo -e "  ${GREEN}PASS${NC} $repo/$task"
    else
        echo -e "  ${RED}FAIL${NC} $repo/$task"
        failed=$((failed + 1))
    fi

done < "$SCRIPT_DIR/assertions.jsonl"

# --- Summary ---
echo ""
echo -e "${CYAN}=== Results ===${NC}"
echo "  Assertions: $passed passed, $((total - passed)) failed, $skipped tasks skipped ($total total)"

if [[ -n "$fail_details" ]]; then
    echo ""
    echo -e "${RED}Failures:${NC}"
    echo -e "$fail_details"
fi

if [[ $((total - passed)) -gt 0 ]]; then
    exit 1
fi
