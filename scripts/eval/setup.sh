#!/bin/bash
# Set up the dogfood/eval repo corpus. Clones every entry in repos.json
# as a sibling of edr (or under $REPO_BASE if set) and, unless --skip-
# index is passed, builds an edr index for each one.
#
# Used by scope-graph dogfood tests (EDR_SCOPE_DOGFOOD_DIR=<path>) and
# by the eval harness (scripts/eval/run.sh).
#
# Disk usage: the full corpus is ~30 GB shallow (linux ~6 GB, pytorch
# ~3 GB, roslyn ~2 GB, kotlin ~2 GB, vscode ~1 GB, spring ~1 GB, plus
# smaller repos). Use --repo NAME to bring in a single repo if you
# don't need the full set.
#
# Usage:
#   ./scripts/eval/setup.sh                    # clone + index all
#   ./scripts/eval/setup.sh --skip-index       # clone only
#   ./scripts/eval/setup.sh --repo NAME        # one repo
#   REPO_BASE=/tmp/corpus ./scripts/eval/setup.sh
#
# Idempotent: repos that already have a .git dir are skipped. Indexing
# is skipped when `edr status` reports a fresh non-stale index.

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_BASE="${REPO_BASE:-$(cd "$SCRIPT_DIR/../.." && cd .. && pwd)}"
FILTER_REPO=""
SKIP_INDEX=false

while [[ $# -gt 0 ]]; do
    case "$1" in
        --repo) FILTER_REPO="$2"; shift 2 ;;
        --skip-index) SKIP_INDEX=true; shift ;;
        -h|--help)
            sed -n '2,21p' "$0" | sed 's/^# //; s/^#//'
            exit 0
            ;;
        *) echo "unknown flag: $1" >&2; exit 2 ;;
    esac
done

GREEN='\033[0;32m'
CYAN='\033[0;36m'
YELLOW='\033[0;33m'
NC='\033[0m'

REPOS_JSON="$SCRIPT_DIR/repos.json"
if [[ ! -f "$REPOS_JSON" ]]; then
    echo "repos.json not found at $REPOS_JSON" >&2
    exit 1
fi

echo -e "${CYAN}=== Cloning repos into $REPO_BASE ===${NC}"

while IFS= read -r repo_json; do
    name=$(echo "$repo_json" | python3 -c "import json,sys; print(json.loads(sys.stdin.read())['name'])")
    url=$(echo "$repo_json" | python3 -c "import json,sys; print(json.loads(sys.stdin.read())['url'])")
    shallow=$(echo "$repo_json" | python3 -c "import json,sys; print(json.loads(sys.stdin.read()).get('shallow', True))")

    if [[ -n "$FILTER_REPO" && "$name" != "$FILTER_REPO" ]]; then continue; fi
    if [[ "$url" == "self" ]]; then continue; fi

    repo_path="$REPO_BASE/$name"
    if [[ -d "$repo_path/.git" ]]; then
        echo -e "  ${GREEN}✓${NC} $name already present"
        continue
    fi

    echo "  Cloning $name from $url..."
    if [[ "$shallow" == "True" ]]; then
        git clone --depth 1 "$url" "$repo_path" 2>&1 | tail -1 &
    else
        git clone "$url" "$repo_path" 2>&1 | tail -1 &
    fi
done < <(python3 -c "import json; [print(json.dumps(r)) for r in json.load(open('$REPOS_JSON'))]")
wait

if $SKIP_INDEX; then
    echo -e "${GREEN}Clone-only complete (--skip-index).${NC}"
    exit 0
fi

echo -e "${CYAN}=== Indexing repos ===${NC}"

if ! command -v edr >/dev/null 2>&1; then
    echo -e "${YELLOW}edr not on PATH; skipping index step.${NC}" >&2
    echo -e "${YELLOW}Install with: go install github.com/jordw/edr@latest${NC}" >&2
    exit 0
fi

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

    # Check if a fresh index already exists.
    status_json=$(EDR_ROOT="$repo_path" edr status 2>/dev/null | head -1)
    has_index=$(echo "$status_json" | python3 -c "import json,sys; d=json.loads(sys.stdin.read()); print('yes' if 'index' in str(d) and 'stale' not in str(d.get('index','')) else 'no')" 2>/dev/null || echo "no")

    if [[ "$has_index" == "yes" ]]; then
        echo -e "  ${GREEN}✓${NC} $name already indexed"
        continue
    fi

    echo "  Indexing $name..."
    EDR_ROOT="$repo_path" edr index >/dev/null 2>&1 &
done < <(python3 -c "import json; [print(json.dumps(r)) for r in json.load(open('$REPOS_JSON'))]")
wait

echo -e "${GREEN}Setup complete.${NC}"
