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

    print(f"Labeling {len(examples)} examples with {model} ({args.concurrency} concurrent)")

    # Skip already-labeled examples (resume support)
    # Key includes candidate files to avoid collisions when candidates change
    def example_key(ex):
        cands = "|".join(c.get("file", "") + ":" + str(c.get("start_line", ""))
                         for c in ex.get("candidates", [])[:5])
        return ex["query"] + "\x00" + ex.get("repo", "") + "\x00" + cands

    already = set()
    try:
        with open(args.output) as f:
            for line in f:
                ex = json.loads(line.strip())
                already.add(example_key(ex))
    except FileNotFoundError:
        pass
    remaining = [ex for ex in examples if example_key(ex) not in already]
    if already:
        print(f"  Resuming: {len(already)} already done, {len(remaining)} remaining")

    import concurrent.futures

    labeled = len(already)
    skipped = 0
    lock = __import__("threading").Lock()

    with open(args.output, "a") as f:
        def process(i_ex):
            nonlocal labeled, skipped
            i, ex = i_ex
            label = label_example(client, ex, model)
            if label is not None:
                ex["label"] = label
                c = ex["candidates"][label]
                with lock:
                    f.write(json.dumps(ex) + "\n")
                    f.flush()
                    labeled += 1
                    n = labeled + skipped
                    if n % 50 == 0 or n == len(remaining):
                        print(f"  [{n}/{len(remaining)}] labeled={labeled} skipped={skipped}")
            else:
                with lock:
                    skipped += 1

        with concurrent.futures.ThreadPoolExecutor(max_workers=args.concurrency) as pool:
            list(pool.map(process, enumerate(remaining)))

    print(f"\nLabeled {labeled}/{len(examples)} examples → {args.output}")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description="Label candidates with Haiku")
    parser.add_argument("input", help="Unlabeled candidates (JSONL)")
    parser.add_argument("--output", "-o", default="labeled.jsonl")
    parser.add_argument("--model", default="claude-haiku-4-5-20251001",
                        help="Anthropic model to use")
    parser.add_argument("--concurrency", "-j", type=int, default=20,
                        help="Number of concurrent API requests")
    main(parser.parse_args())
