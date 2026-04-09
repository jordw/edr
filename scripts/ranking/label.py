#!/usr/bin/env python3
"""Label ranking candidates using Claude Haiku.

Usage:
    python label.py candidates.jsonl --output labeled.jsonl

Input format (one JSON object per line):
    {"query": "probe", "repo": "linux", "candidates": [
        {"name": "probe", "type": "function", "file": "drivers/media/mxl5xx.c",
         "start_line": 1698, "end_line": 1750},
        ...
    ]}

Output: same format with "label" field added (0-based index of best candidate).
"""

import argparse
import json
import os
import sys
import time

try:
    import anthropic
except ImportError:
    print("pip install anthropic", file=sys.stderr)
    sys.exit(1)


def format_candidates(candidates: list[dict]) -> str:
    lines = []
    for i, c in enumerate(candidates):
        span = c.get("end_line", 0) - c.get("start_line", 0)
        lines.append(f"  {i}. {c['file']}:{c.get('start_line', '?')}  "
                      f"{c.get('type', '?')} {c['name']}  ({span} lines)")
    return "\n".join(lines)


def label_example(client, example: dict, model: str) -> int | None:
    query = example["query"]
    repo = example.get("repo", "unknown")
    candidates = example["candidates"]

    prompt = f"""You are labeling training data for a code navigation tool. Given a bare symbol query and a list of candidate definitions from a repository, pick the one most likely intended by a developer.

Repository: {repo}
Query: {query}

Candidates:
{format_candidates(candidates)}

Which candidate (by number) is the most likely intended target? Consider:
- Is this the primary/canonical definition, or a secondary/wrapper/binding?
- Is this in core infrastructure or leaf/peripheral code?
- Would most developers working in this repo mean this one?

Reply with just the number (0-based index). Nothing else."""

    try:
        response = client.messages.create(
            model=model,
            max_tokens=10,
            messages=[{"role": "user", "content": prompt}],
        )
        text = response.content[0].text.strip()
        # Extract first number
        for word in text.split():
            word = word.rstrip(".,;:")
            if word.isdigit():
                idx = int(word)
                if 0 <= idx < len(candidates):
                    return idx
        return None
    except Exception as e:
        print(f"  API error: {e}", file=sys.stderr)
        return None


def main(args):
    client = anthropic.Anthropic()
    model = args.model

    examples = []
    with open(args.input) as f:
        for line in f:
            line = line.strip()
            if line:
                examples.append(json.loads(line))

    print(f"Labeling {len(examples)} examples with {model}")

    labeled = []
    for i, ex in enumerate(examples):
        label = label_example(client, ex, model)
        if label is not None:
            ex["label"] = label
            labeled.append(ex)
            c = ex["candidates"][label]
            print(f"  [{i+1}/{len(examples)}] {ex['query']} → {c['file']}:{c.get('start_line', '?')} {c['name']}")
        else:
            print(f"  [{i+1}/{len(examples)}] {ex['query']} → SKIPPED (no valid label)")

        # Rate limiting
        if i < len(examples) - 1:
            time.sleep(0.1)

    with open(args.output, "w") as f:
        for ex in labeled:
            f.write(json.dumps(ex) + "\n")

    print(f"\nLabeled {len(labeled)}/{len(examples)} examples → {args.output}")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description="Label candidates with Haiku")
    parser.add_argument("input", help="Unlabeled candidates (JSONL)")
    parser.add_argument("--output", "-o", default="labeled.jsonl")
    parser.add_argument("--model", default="claude-haiku-4-5-20251001",
                        help="Anthropic model to use")
    main(parser.parse_args())
