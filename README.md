# edr

[![CI](https://github.com/jordw/edr/actions/workflows/ci.yml/badge.svg)](https://github.com/jordw/edr/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.25+-00ADD8.svg)](https://go.dev)

**Coding agents waste most of their context window reading entire files to find one function. edr fixes that.**

edr (editor) gives agents symbol-level reads, batched operations, and structured output — so a task that takes 6 tool calls and 200KB of context takes 2 calls and 16KB. Works with any agent that can run shell commands (Claude Code, Cursor, Codex, etc). Fully local, no telemetry.

## Before and after

Task: add a `retries` parameter to a scheduler class.

**Without edr** — agents read whole files to reach individual symbols:
```
read src/scheduler.py            → 400 lines (needed 1 class)
grep "retry" src/                → flat file:line list
read src/config.py               → 200 lines (needed 1 function)
read src/worker.py               → 300 lines (needed the API shape)
edit src/scheduler.py            → no verification
read src/scheduler.py            → re-read to confirm
```
6 calls, ~200KB of context.

**With edr** — read symbols directly, batch everything:
```bash
# Call 1: gather context (--sig = signatures only, no bodies)
edr -r src/scheduler.py:Scheduler --sig \
    -r src/config.py:parse_config \
    -r src/worker.py:Worker --sig \
    -s "retry"

# Call 2: apply edits (auto-verifies build)
edr -e src/scheduler.py --old "def run(self):" --new "def run(self, retries=3):" \
    -e src/config.py --old '"timeout": 30' --new '"timeout": 30, "retries": 3'
```
2 calls, ~16KB. Same result.

## Install

```bash
brew install jordw/tap/edr
edr setup .
```

`edr setup` indexes your project, adds `.edr/` to `.gitignore`, and appends agent instructions to your config (`CLAUDE.md`, `.cursorrules`, or `AGENTS.md`). Existing content is preserved.

<details>
<summary>Other install methods</summary>

**Pre-built binary** ([review the script first](install.sh)):

```bash
curl -fsSL https://raw.githubusercontent.com/jordw/edr/main/install.sh | sh
```

**From source** (requires Go 1.25+ and a C/C++ compiler for tree-sitter):

```bash
go install github.com/jordw/edr@latest
edr setup /path/to/your/project
```

**Cloud agents and CI** (installs Go/gcc if needed, builds, indexes):

```bash
git clone https://github.com/jordw/edr.git && ./edr/setup.sh
```

</details>

The index lives in `.edr/` at the repo root and rebuilds automatically if deleted.

## How it works

edr parses your codebase with [tree-sitter](https://tree-sitter.github.io/tree-sitter/) and stores symbols in a SQLite index. This enables three things agents can't do with raw file tools:

**Symbol-level operations.** Read one function instead of a 400-line file. Get a class API with `--signatures` — method names, args, and return types without bodies (85% fewer tokens). Add a method with `--inside ClassName` without reading the file at all. Edits re-index immediately and auto-verify.

**Sessions.** Set `EDR_SESSION` once per conversation. Re-reading an unchanged file returns `{unchanged: true}` instead of the full content. Already-seen symbol bodies in search results are replaced with `[in context]`.

**Batching.** `-r`, `-s`, `-e`, `-w` combine reads, searches, edits, and writes into one CLI call. One call to gather context, one to apply mutations.

## Benchmarks

Median **93% context reduction** across 9 scenarios and 7 repos — from small libraries to Django (880 files). Scenarios and methodology in [`bench/scenarios/`](bench/scenarios/).

| Repo | Language | Files | Baseline | edr | Reduction |
|---|---|---|---|---|---|
| [urfave/cli](https://github.com/urfave/cli) | Go | ~70 | 197KB / 20 calls | 16KB / 9 calls | **92%** |
| [vitess/sqlparser](https://github.com/vitessio/vitess) | Go | ~70 | 322KB / 17 calls | 16KB / 9 calls | **95%** |
| [vitess/vtgate](https://github.com/vitessio/vitess) | Go | ~490 | 747KB / 19 calls | 33KB / 9 calls | **96%** |
| [pallets/click](https://github.com/pallets/click) | Python | ~17 | 297KB / 20 calls | 19KB / 9 calls | **93%** |
| [rails/thor](https://github.com/rails/thor) | Ruby | ~35 | 170KB / 20 calls | 16KB / 9 calls | **91%** |
| [reduxjs/redux-toolkit](https://github.com/reduxjs/redux-toolkit) | TypeScript | ~190 | 186KB / 20 calls | 23KB / 9 calls | **88%** |
| [django/django](https://github.com/django/django) | Python | ~880 | 1,328KB / 20 calls | 30KB / 9 calls | **98%** |

The baseline simulates how agents actually work: whole-file reads, grep for search, glob-then-read for exploration. No symbol extraction, no batching.

<details>
<summary>Per-scenario breakdown (urfave/cli)</summary>

Biggest wins: operations with no built-in equivalent. Text search goes negative — edr's structured JSON adds overhead when grep output is already compact.

| Scenario | Baseline | edr | Reduction |
|---|---|---|---|
| Understand a class API | 10,072B (whole file) | 1,592B (`--signatures`) | **84%** |
| Read a specific function | 15,307B (whole file) | 1,463B (symbol read) | **90%** |
| Find references | 67,101B / 4 calls | 865B / 1 call (`refs`) | **99%** |
| Search with context | 614B (grep -C3) | 1,812B (structured) | **-195%** |
| Orient in codebase | 11,481B / 2 calls | 2,235B / 1 call (`map`) | **81%** |
| Edit a function | 10,172B / 2 calls | 680B / 1 call (batch) | **93%** |
| Add method to a class | 10,072B / 2 calls | 184B / 1 call (`--inside`) | **98%** |
| Multi-file read | 30,794B / 3 calls | 2,606B / 1 call (batched) | **92%** |
| Explore a symbol | 41,285B / 4 calls | 4,562B / 1 call | **89%** |
| **Total** | **196,898B / 20 calls** | **15,999B / 9 calls** | **92%** |

</details>

Reproduce: `bash bench/run_real_repo_benchmarks.sh` (clones repos to `/tmp`, ~10 min).

## Commands

**Batch flags** (primary interface — what agents use):
```bash
edr -r file[:Symbol]              # Read file or symbol
edr -r file:Class --sig           # Signatures only, no bodies
edr -s "pattern"                  # Search symbols or text (--text)
edr -e file --old "x" --new "y"   # Edit with auto re-index + verify
edr -w file --inside Class        # Add method/field without reading
```

Combine freely — one call to read, one to mutate:
```bash
# Gather context
edr -r src/config.go:parseConfig --sig -r src/main.go:Server -s "handleRequest"

# Apply mutations (verify runs automatically)
edr -e src/config.go --old "old" --new "new" -w src/new_test.go --content "..."
```

**Standalone commands:**
```
edr read file:Symbol              Read a function, class, or struct
edr read file:Class --signatures  API shape without bodies
edr read file --skeleton          Collapsed block view
edr search "pattern"              Symbol or text search (--text)
edr map                           Symbol overview of repo or directory
edr edit file --old "x" --new "y" Edit with auto re-index + verify
edr write file --inside Class     Add method/field without reading the file
edr refs Symbol                   Find references (import-aware: Go/Py/JS/TS)
edr rename old new --dry-run      Cross-file rename with preview
edr verify                        Auto-detect and run build/test
```

**Admin:**
```
edr setup .                       Index repo + configure agent instructions
edr reindex                       Rebuild symbol index
```

All output is structured JSON.

## Supported languages

edr reads and edits any text file. Symbol-aware features require a supported language.

**Symbol indexing:** Go, Python, JavaScript/JSX, TypeScript/TSX, Rust, Java, C, C++, Ruby, PHP, Zig, Lua, Bash/Shell, C#, Kotlin

**Import-aware refs:** Go, Python, JavaScript, TypeScript (others fall back to text matching)

## Limitations

- **Tree-sitter, not LSP.** Fast, no build step, works on broken code, zero config. The tradeoff: no type information. Refs use import-path matching, not type resolution, so cross-package references may produce false positives. For agent read/edit/search workloads, structural parsing is enough.
- **macOS and Linux only.** Windows is not planned.
- **C/C++ compiler required** when building from source (tree-sitter grammars). Homebrew and the install script use pre-built binaries.
- **First index: 1-3s** on small repos, ~30s on large ones (vitess, 1.5M LOC). Incremental re-index after edits: ~50ms/file.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Bug reports and PRs welcome on [GitHub](https://github.com/jordw/edr/issues).

## License

[MIT](LICENSE)
