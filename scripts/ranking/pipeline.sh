#!/bin/bash
# End-to-end ranking model training pipeline.
#
# Usage:
#   ./pipeline.sh /path/to/repo1 /path/to/repo2 ...
#
# Or with default repos:
#   ./pipeline.sh
#
# Requirements:
#   - Go (for generate)
#   - Python 3 with: pip install torch anthropic
#   - ANTHROPIC_API_KEY environment variable
#
# Output: rank_model.bin in the current directory

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
WORK_DIR="${WORK_DIR:-/tmp/edr-ranking-$$}"
LIMIT_PER_REPO="${LIMIT_PER_REPO:-500}"
MODEL="${HAIKU_MODEL:-claude-haiku-4-5-20251001}"
EPOCHS="${EPOCHS:-100}"
BATCH_SIZE="${BATCH_SIZE:-32}"
LR="${LR:-1e-3}"

mkdir -p "$WORK_DIR"
echo "Working directory: $WORK_DIR"

# --- Step 1: Generate candidate lists ---
echo ""
echo "=== Step 1: Generating candidate lists ==="
echo "  (Tip: run 'edr index' on each repo first for fast symbol loading)"

REPOS=("$@")
if [ ${#REPOS[@]} -eq 0 ]; then
    echo "Usage: $0 /path/to/repo1 /path/to/repo2 ..."
    echo ""
    echo "Environment variables:"
    echo "  LIMIT_PER_REPO  Max queries per repo (default: 500)"
    echo "  HAIKU_MODEL     Anthropic model for labeling (default: claude-haiku-4-5-20251001)"
    echo "  EPOCHS          Training epochs (default: 100)"
    echo "  WORK_DIR        Working directory (default: /tmp/edr-ranking-PID)"
    exit 1
fi

CANDIDATES="$WORK_DIR/candidates.jsonl"
> "$CANDIDATES"  # truncate

for repo in "${REPOS[@]}"; do
    echo "  Generating from: $repo"
    go run "$SCRIPT_DIR/generate.go" "$repo" --limit "$LIMIT_PER_REPO" >> "$CANDIDATES"
done

TOTAL=$(wc -l < "$CANDIDATES" | tr -d ' ')
echo "  Total candidate lists: $TOTAL"

if [ "$TOTAL" -eq 0 ]; then
    echo "No candidates generated. Check that repos have ambiguous symbol names."
    exit 1
fi

# --- Step 2: Label with Haiku ---
echo ""
echo "=== Step 2: Labeling with $MODEL ==="

LABELED="$WORK_DIR/labeled.jsonl"

if [ -z "${ANTHROPIC_API_KEY:-}" ]; then
    echo "Error: ANTHROPIC_API_KEY not set"
    echo "Set it to your Anthropic API key to enable Haiku labeling."
    exit 1
fi

python3 "$SCRIPT_DIR/label.py" "$CANDIDATES" \
    --output "$LABELED" \
    --model "$MODEL"

LABELED_COUNT=$(wc -l < "$LABELED" | tr -d ' ')
echo "  Labeled: $LABELED_COUNT / $TOTAL"

if [ "$LABELED_COUNT" -lt 10 ]; then
    echo "Too few labeled examples. Check API key and model access."
    exit 1
fi

# --- Step 3: Train ---
echo ""
echo "=== Step 3: Training model ==="

OUTPUT="${OUTPUT:-rank_model.bin}"

python3 "$SCRIPT_DIR/train.py" "$LABELED" \
    --output "$OUTPUT" \
    --epochs "$EPOCHS" \
    --batch-size "$BATCH_SIZE" \
    --lr "$LR"

echo ""
echo "=== Done ==="
echo "  Weights: $OUTPUT ($(du -h "$OUTPUT" | cut -f1))"
echo "  Candidates: $CANDIDATES"
echo "  Labels: $LABELED"
echo ""
echo "To install:"
echo "  cp $OUTPUT \$(edr status | python3 -c 'import json,sys; print(json.loads(next(sys.stdin).split(chr(10))[0]).get(\"edr_dir\",\"~/.edr\"))')/"
echo ""
echo "Or copy to any repo's .edr/ directory:"
echo "  cp $OUTPUT ~/.edr/repos/<repo>/rank_model.bin"
