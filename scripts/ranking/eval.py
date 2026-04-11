#!/usr/bin/env python3
"""Agent eval: run randomized navigation tasks against repos.

For each query, edr shows a shortlist. Haiku picks a candidate based on
the shortlist + repo context. edr then focuses that candidate, generating
a training label. Each run produces different queries and picks.

Usage:
    python eval.py /path/to/repo1 /path/to/repo2 ... [--queries-per-repo 50]

Requires: ANTHROPIC_API_KEY, edr on PATH, repos indexed (edr index).
"""

import argparse
import concurrent.futures
import json
import os
import random
import subprocess
import sys
import threading
import time

try:
    import anthropic
except ImportError:
    print("pip install anthropic", file=sys.stderr)
    sys.exit(1)


def run_edr(repo: str, *args: str) -> tuple[dict, str, int]:
    """Run an edr command and return (header_dict, full_output, exit_code)."""
    env = {**os.environ, "EDR_ROOT": repo}
    result = subprocess.run(
        ["edr", *args],
        capture_output=True, text=True, env=env, timeout=30,
    )
    output = result.stdout.strip()
    header = {}
    if output:
        try:
            first_line = output.split("\n")[0]
            header = json.loads(first_line)
        except (json.JSONDecodeError, IndexError):
            pass
    return header, output, result.returncode


def get_ambiguous_symbols(repo: str, limit: int = 200) -> list[str]:
    """Get ambiguous symbol names from a repo using generate.go output."""
    env = {**os.environ}
    result = subprocess.run(
        ["go", "run", "scripts/ranking/generate.go", repo, "--limit", str(limit)],
        capture_output=True, text=True, env=env, timeout=120,
    )
    queries = []
    for line in result.stdout.strip().split("\n"):
        if not line:
            continue
        try:
            ex = json.loads(line)
            queries.append(ex["query"])
        except (json.JSONDecodeError, KeyError):
            pass
    return queries


def format_shortlist(header: dict, output: str) -> str:
    """Format the shortlist output for Haiku."""
    lines = output.strip().split("\n")
    # Skip header line, take candidate lines
    candidates = [l.strip() for l in lines[1:] if l.strip()]
    return "\n".join(candidates[:10])


def haiku_pick(client, model: str, repo_name: str, query: str,
               shortlist_text: str) -> str | None:
    """Ask Haiku to pick a candidate from the shortlist.
    Returns the file:symbol string to focus, or None."""

    prompt = f"""You are navigating the {repo_name} codebase. You searched for "{query}" and got this shortlist:

{shortlist_text}

Pick the candidate that is most likely the canonical/primary definition — the one most other code in this repo imports or calls.

Reply with ONLY the file:symbol to focus on, like: path/to/file.c:symbol_name
Nothing else."""

    try:
        response = client.messages.create(
            model=model,
            max_tokens=100,
            messages=[{"role": "user", "content": prompt}],
        )
        text = response.content[0].text.strip()
        # Extract file:symbol — first line, strip whitespace
        pick = text.split("\n")[0].strip()
        # Basic validation: must contain : and /
        if ":" in pick and ("/" in pick or "." in pick):
            return pick
        return None
    except Exception as e:
        return None


def run_one_query(client, model: str, repo: str, repo_name: str,
                  query: str, stats: dict, lock: threading.Lock):
    """Run one query: shortlist → haiku pick → focus."""

    # Step 1: bare focus to get shortlist
    header, output, exit_code = run_edr(repo, "focus", query)

    # Check if we got a shortlist (ambiguous result)
    if header.get("resolve") != "ambiguous" and "method" not in header:
        # Auto-resolved or error — skip
        with lock:
            stats["skipped"] += 1
        return

    # If it auto-resolved, that's still useful (just no shortlist pick)
    if header.get("file"):
        with lock:
            stats["auto_resolved"] += 1
        return

    shortlist_text = format_shortlist(header, output)
    if not shortlist_text:
        with lock:
            stats["skipped"] += 1
        return

    # Step 2: Haiku picks a candidate
    pick = haiku_pick(client, model, repo_name, query, shortlist_text)
    if not pick:
        with lock:
            stats["haiku_failed"] += 1
        return

    # Step 3: Focus the picked candidate (generates training label)
    pick_header, pick_output, pick_exit = run_edr(repo, "focus", pick)

    if pick_header.get("file"):
        with lock:
            stats["labels"] += 1
            if stats["labels"] % 10 == 0:
                print(f"  [{stats['labels']} labels, {stats['skipped']} skipped]")
    else:
        with lock:
            stats["pick_failed"] += 1


def eval_repo(client, model: str, repo: str, queries_per_repo: int,
              concurrency: int) -> dict:
    """Run eval on one repo."""
    repo_name = os.path.basename(repo.rstrip("/"))
    print(f"\n=== {repo_name} ===")

    # Get ambiguous queries
    print(f"  Generating queries...")
    all_queries = get_ambiguous_symbols(repo, limit=queries_per_repo * 2)
    random.shuffle(all_queries)
    queries = all_queries[:queries_per_repo]
    print(f"  {len(queries)} queries sampled from {len(all_queries)} ambiguous names")

    stats = {
        "labels": 0,
        "auto_resolved": 0,
        "skipped": 0,
        "haiku_failed": 0,
        "pick_failed": 0,
    }
    lock = threading.Lock()

    # Run queries with concurrency
    with concurrent.futures.ThreadPoolExecutor(max_workers=concurrency) as pool:
        futures = [
            pool.submit(run_one_query, client, model, repo, repo_name, q, stats, lock)
            for q in queries
        ]
        concurrent.futures.wait(futures)

    print(f"  Results: {stats['labels']} labels, {stats['auto_resolved']} auto-resolved, "
          f"{stats['skipped']} skipped, {stats['haiku_failed']} haiku failed, "
          f"{stats['pick_failed']} pick failed")
    return stats


def main():
    parser = argparse.ArgumentParser(description="Randomized agent eval for ranking")
    parser.add_argument("repos", nargs="+", help="Repo paths")
    parser.add_argument("--queries-per-repo", "-n", type=int, default=50,
                        help="Queries to run per repo")
    parser.add_argument("--concurrency", "-j", type=int, default=5,
                        help="Concurrent Haiku requests per repo")
    parser.add_argument("--model", default="claude-haiku-4-5-20251001")
    parser.add_argument("--seed", type=int, default=0,
                        help="Random seed (0 = time-based)")
    args = parser.parse_args()

    if args.seed:
        random.seed(args.seed)
    else:
        random.seed(int(time.time()))

    client = anthropic.Anthropic()

    total_stats = {
        "labels": 0,
        "auto_resolved": 0,
        "skipped": 0,
        "haiku_failed": 0,
        "pick_failed": 0,
    }

    for repo in args.repos:
        repo = os.path.abspath(repo)
        if not os.path.isdir(repo):
            print(f"Skipping {repo} — not found")
            continue

        stats = eval_repo(client, args.model, repo, args.queries_per_repo,
                          args.concurrency)
        for k, v in stats.items():
            total_stats[k] += v

    print(f"\n=== Total ===")
    print(f"  Labels:        {total_stats['labels']}")
    print(f"  Auto-resolved: {total_stats['auto_resolved']}")
    print(f"  Skipped:       {total_stats['skipped']}")
    print(f"  Haiku failed:  {total_stats['haiku_failed']}")
    print(f"  Pick failed:   {total_stats['pick_failed']}")

    # Count total training labels across all repos
    total_labels = 0
    for repo in args.repos:
        repo = os.path.abspath(repo)
        try:
            result = subprocess.run(
                ["edr", "status"],
                capture_output=True, text=True,
                env={**os.environ, "EDR_ROOT": repo},
                timeout=5,
            )
        except Exception:
            continue

    print(f"\n  Run again to generate more diverse labels.")
    print(f"  Labels are saved in each repo's .edr/training_labels.jsonl")


if __name__ == "__main__":
    main()
