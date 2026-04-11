# edr — code editing tools for agents

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

**edr gives coding agents code-aware file tools that front-load the context needed for the next step.**

Instead of raw files and grep output, edr returns structured code context:

- **`orient`** — budgeted structural overview of the codebase in terms of symbols and files.
- **`focus file:Symbol`** — reads a symbol, not the whole file. Includes relevant surrounding context.
- **`focus SymbolName`** — resolves likely matches and opens the best candidate.
- **`edit --old X --new Y --verify`** — diff, updated context, and verification feedback.
- **`edr --orient cmd/ --focus file:Sym --edit ...`** — survey, inspect, and mutate in one call when needed.
- Repeated reads are deduplicated so agents do less work.

Fully local, shell-friendly, no telemetry. Designed to replace generic file operations with agent-oriented ones.

## Install

```bash
brew install jordw/tap/edr
edr setup
```

`edr setup` installs agent instructions to your global config (`~/.claude/CLAUDE.md`, `~/.cursor/rules/edr.mdc`, or `~/.codex/AGENTS.md`). Instructions auto-update when edr is rebuilt. They teach the agent to use edr instead of built-in file tools. Session data is stored in `~/.edr/repos/`, not in the project directory.

<details>
<summary>Other install methods</summary>

**Pre-built binary** ([review the script first](install.sh)):

```bash
curl -fsSL https://raw.githubusercontent.com/jordw/edr/main/install.sh | sh
```

**From source** (requires Go 1.24+):

```bash
go install github.com/jordw/edr@latest
edr setup
```

**Cloud agents and CI** (installs Go if needed, builds):

```bash
git clone https://github.com/jordw/edr.git && ./edr/setup.sh
```

</details>

## Example

Orient in the project, focus on a function, edit it:

```bash
# Without edr: grep to find it, read the range, grep for callers, read each caller
# 5+ tool calls, ~25KB of context

# With edr: 3 calls, ~3KB
edr orient --dir src/                      # structural overview
edr focus src/scheduler.py:run             # just the function (not the file)
edr edit src/scheduler.py \
    --old "def run(self):" \
    --new "def run(self, retries=3):" --verify
```

**Batched:** gather everything in one call, mutate in one call:

```bash
# 1. Orient + focus on three APIs, one call
edr -o --dir src/ \
    -f src/scheduler.py:Scheduler --sig \
    -f src/config.py:parse_config \
    -f src/worker.py:Worker --sig

# 2. Edit two files, verify at the end
edr -e src/scheduler.py --old "def run(self):" --new "def run(self, retries=3):" \
    -e src/config.py --old '"timeout": 30' --new '"timeout": 30, "retries": 3'
```

## How it works

edr uses pure-Go regex-based symbol extraction to give agents capabilities they don't have with raw file tools:

**Symbol-level reads and edits.** Focus on one function instead of a 400-line file. Get a class API with `--signatures`. Scope edits to a symbol with `--in Symbol`. Use `--verify` for build feedback after edits. The agent sees what it needs and makes better decisions.

**Structural navigation.** `orient` shows what's in a directory or project in terms of symbols and files, not raw listings. `focus SymbolName` resolves ambiguous names with ranked matching. Budget controls keep output sized for agent context windows.

**Sessions.** edr tracks what the agent has already seen and returns only what changed. Repeated reads produce zero output. Zero config.

**Search and indexing.** `edr index` builds a trigram + symbol index that accelerates search, orient, and symbol resolution. On the Linux kernel (93K files), indexed operations complete in 0.02-0.5s. `edr bench` measures real performance on your repo.

## Commands

### Primary commands

| Command | Description |
|---|---|
| `orient [path]` | Structural overview of a directory or project (replaces `map`) |
| `focus file[:Symbol]` | Read file or symbol with context (replaces `read`) |
| `edit file` | Edit, write, create files. `--verify` to check build. |
| `status` | Repo root, index coverage, undo, build state, warnings |
| `undo` | Revert last edit/write (auto-checkpointed) |
| `files "pattern"` | Find files containing text (trigram-accelerated) |
| `index` | Build or inspect the search index |
| `bench` | Benchmark operations on current repo |
| `setup` | Install agent instructions |

Old command names `map` and `read` still work as aliases for `orient` and `focus`.

### Batch flags

Chain operations with `--focus`, `--orient`, `--search`, `--edit`, `--write` (short: `-f -o -s -e -w`). File carries forward. Edit includes read-back automatically.

```bash
edr --orient cmd/ --focus file:Sym --sig
edr --focus file:Func --edit --old "x" --new "y"
edr --search "TODO" --include "*.go"
edr --focus file:Func --expand callers
```

### Cross-repo targeting

```bash
edr focus file:Symbol --root /path/to/repo
export EDR_ROOT=/path/to/repo    # set once, all commands use it
```

### Batch modifiers

**Focus** (after `-f`): `--sig`, `--skeleton`, `--full`, `--expand[=deps]`, `--symbols`, `--lines 10:50`, `--budget N`

**Orient** (after `-o`): `--dir`, `--lang`, `--grep`, `--glob`, `--type`, `--budget N`

**Edit** (after `-e`): `--old "x" --new "y"`, `--where Sym` (resolves file), `--in Sym` (scope), `--content "..."`, `--inside Class`, `--after Sym`, `--append`, `--mkdir`, `--all`, `--delete`, `--dry-run`, `--fuzzy`, `--read-back`, `--no-verify`, `@file` for metacharacters

Output uses plain mode: one JSON header line followed by raw-text body.

## Languages

edr reads and edits any text file. Symbol-aware features (symbol reads, `--signatures`, `map`) require a supported language:

**Symbol parsing:** Go, Python, JavaScript/JSX, TypeScript/TSX, Rust, Java, C, C++, Ruby

## Limitations

- **Structural navigation, not code analysis.** edr finds functions and classes by pattern, not by parsing or type-checking. This means it works instantly, on broken code, with zero config — but it does not have type information. It may miss unusual syntax (e.g. deeply nested anonymous functions).
- **macOS and Linux only.** Windows is not planned.
- **Pure Go.** No CGO, no C compiler needed. Single ~6MB binary.

## How it compares

Without edr, agents grep to find code, read line ranges, guess what's relevant, edit, then re-read to check. Each step is a separate tool call with context the agent has to filter.

With edr, `focus file:Symbol` returns the function body with relevant context. `edit` returns the diff and updated code with optional build verification. The agent makes fewer decisions about what to read because edr front-loads useful context.

| Operation | What the agent gets |
|---|---|
| `orient` | Budgeted structural overview (symbols and files) |
| `focus file:Symbol` | Symbol body + relevant surrounding context |
| `focus SymbolName` | Ranked resolution, auto-opens best match |
| `edit --old X --new Y --verify` | Diff + updated context + verification feedback |
| Re-read unchanged file | Deduplicated (zero output, zero waste) |

## License

[MIT](LICENSE)
