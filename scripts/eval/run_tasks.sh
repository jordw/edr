#!/bin/bash
# Run ranking tasks against repos, collecting implicit training labels
# and reporting failures.
#
# Usage:
#   ./run_tasks.sh [--repo linux] [--task find_scheduler_tick] [--dry-run]
#
# With no args, runs all tasks for all repos found in ../

set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
TASKS_FILE="${TASKS_FILE:-$SCRIPT_DIR/tasks.jsonl}"
REPO_BASE="${REPO_BASE:-$(cd "$SCRIPT_DIR/../.." && cd .. && pwd)}"
FILTER_REPO="${1:-}"
FILTER_TASK=""
DRY_RUN=false

# Parse args
while [[ $# -gt 0 ]]; do
    case "$1" in
        --repo) FILTER_REPO="$2"; shift 2 ;;
        --task) FILTER_TASK="$2"; shift 2 ;;
        --dry-run) DRY_RUN=true; shift ;;
        *) shift ;;
    esac
done

# Colors
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[0;33m'
NC='\033[0m'

total=0
passed=0
failed=0
skipped=0
labels_before=0
labels_after=0

while IFS= read -r line; do
    repo=$(echo "$line" | python3 -c "import json,sys; print(json.loads(sys.stdin.read())['repo'])")
    task=$(echo "$line" | python3 -c "import json,sys; print(json.loads(sys.stdin.read())['task'])")

    # Filter
    if [[ -n "$FILTER_REPO" && "$repo" != "$FILTER_REPO" ]]; then continue; fi
    if [[ -n "$FILTER_TASK" && "$task" != "$FILTER_TASK" ]]; then continue; fi

    repo_path="$REPO_BASE/$repo"
    if [[ ! -d "$repo_path" ]]; then
        echo -e "${YELLOW}SKIP${NC} $repo/$task — repo not found at $repo_path"
        skipped=$((skipped + 1))
        continue
    fi

    total=$((total + 1))

    if $DRY_RUN; then
        echo "DRY  $repo/$task"
        continue
    fi

    # Count labels before
    edr_dir=$(EDR_ROOT="$repo_path" edr status 2>/dev/null | python3 -c "import json,sys; print(json.loads(sys.stdin.read().split(chr(10))[0]).get('edr_dir',''))" 2>/dev/null || echo "")

    # Run steps
    steps=$(echo "$line" | python3 -c "import json,sys; [print(s) for s in json.loads(sys.stdin.read())['steps']]")
    task_ok=true
    step_num=0
    task_start=$(date +%s)

    while IFS= read -r step; do
        step_num=$((step_num + 1))
        # Run edr command
        output=$(EDR_ROOT="$repo_path" edr $step 2>&1) || true
        exit_code=$?

        # Check for errors (but shortlists are OK — exit 1 with "resolve":"ambiguous")
        header=$(echo "$output" | head -1)
        if echo "$header" | python3 -c "import json,sys; d=json.loads(sys.stdin.read()); exit(0 if d.get('error') and 'ambiguous' not in d.get('error','') and d.get('resolve')!='ambiguous' else 1)" 2>/dev/null; then
            ec=$(echo "$header" | python3 -c "import json,sys; print(json.loads(sys.stdin.read()).get('ec','?'))" 2>/dev/null || echo "?")
            echo -e "  ${RED}ERR${NC}  step $step_num: edr $step → $ec"
            task_ok=false
        fi
    done <<< "$steps"

    task_end=$(date +%s)
    duration=$((task_end - task_start))

    if $task_ok; then
        echo -e "${GREEN}PASS${NC} $repo/$task (${duration}s, $step_num steps)"
        passed=$((passed + 1))
    else
        echo -e "${RED}FAIL${NC} $repo/$task (${duration}s, $step_num steps)"
        failed=$((failed + 1))
    fi

done < "$TASKS_FILE"

# Count training labels collected
total_labels=0
for repo_dir in "$REPO_BASE"/*/; do
    repo_name=$(basename "$repo_dir")
    edr_dir_path=$(python3 -c "
import hashlib, os
root = os.path.realpath('$repo_dir')
h = hashlib.sha256(root.encode()).hexdigest()[:12]
name = os.path.basename(root.rstrip('/'))
print(os.path.expanduser(f'~/.edr/repos/{h}_{name}/training_labels.jsonl'))
" 2>/dev/null)
    if [[ -f "$edr_dir_path" ]]; then
        count=$(wc -l < "$edr_dir_path" | tr -d ' ')
        total_labels=$((total_labels + count))
    fi
done

echo ""
echo "=== Results ==="
echo "  Tasks:  $passed passed, $failed failed, $skipped skipped ($total total)"
echo "  Labels: $total_labels training labels collected"
