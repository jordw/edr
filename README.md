# edr — the editor for agents

[![CI](https://github.com/jordw/edr/actions/workflows/ci.yml/badge.svg)](https://github.com/jordw/edr/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.25+-00ADD8.svg)](https://go.dev)

Coding agents read entire files to find one function, grep then re-read every match, and make edits one file at a time with no way to verify the result.

edr indexes your codebase by symbol (functions, classes, structs) and lets agents read exactly what they need, batch operations into single calls, and track what's already in context so repeated reads shrink to diffs.

## The difference

An agent adding a retry parameter to a scheduler. Without edr — 8 tool calls, ~180KB of context:

```bash
Read src/scheduler.py              # 14KB — whole file, need one class
Grep "retry" src/                  # file:line list, no structure
Read src/config.py                 # 11KB — whole file, need one function
Read src/worker.py                 # 9KB  — whole file, need the API shape
Read src/retry.py                  # 6KB  — whole file for one call site
Edit src/scheduler.py              # old_text/new_text, no verification
Read src/scheduler.py              # re-read to confirm the edit took
Run "python -m pytest"             # separate verify step
```

With edr — 2 calls, ~8KB of context:

```bash
# Call 1: gather exactly what's needed
edr do '{
  "reads": [
    {"file": "src/scheduler.py", "symbol": "Scheduler", "signatures": true},
    {"file": "src/config.py", "symbol": "parse_config"},
    {"file": "src/worker.py", "symbol": "Worker", "signatures": true}
  ],
  "queries": [
    {"cmd": "refs", "symbol": "Scheduler.run"},
    {"cmd": "search", "pattern": "retry", "body": true}
  ]
}'

# Call 2: edit + write + verify, atomically
edr do '{
  "edits": [
    {"file": "src/scheduler.py", "old_text": "def run(self):", "new_text": "def run(self, retries=3):"},
    {"file": "src/config.py", "old_text": "\"timeout\": 30", "new_text": "\"timeout\": 30, \"retries\": 3"}
  ],
  "writes": [
    {"file": "tests/test_retry.py", "content": "import pytest\n...", "mkdir": true}
  ],
  "verify": true
}'
```

Symbol reads return just the function or class asked for. `--signatures` returns a class's API without implementation bodies (75-86% smaller). Refs are import-aware. Edits across multiple files are atomic and auto-reindex. Sessions track what's already in context so repeated reads return diffs, not full content.

## Benchmarks

Baselines model how agents use built-in tools: `Read` returns whole files, `Grep` returns line matches without symbol context, edits require a separate read-then-write. Both baselines and edr equivalents are defined in [`bench/scenarios/`](bench/scenarios/).

6 real-world repos, 9 scenarios each, 3 iterations, median response bytes:

| Repo | Language | Baseline | edr | Reduction |
|---|---|---|---|---|
| [urfave/cli](https://github.com/urfave/cli) | Go | 322KB / 24 calls | 21KB / 9 calls | **93%** |
| [vitess/sqlparser](https://github.com/vitessio/vitess) | Go | 660KB / 21 calls | 16KB / 9 calls | **98%** |
| [vitess/vtgate](https://github.com/vitessio/vitess) | Go | 929KB / 23 calls | 32KB / 9 calls | **97%** |
| [pallets/click](https://github.com/pallets/click) | Python | 455KB / 24 calls | 21KB / 9 calls | **95%** |
| [rails/thor](https://github.com/rails/thor) | Ruby | 234KB / 24 calls | 15KB / 9 calls | **94%** |
| [reduxjs/redux-toolkit](https://github.com/reduxjs/redux-toolkit) | TypeScript | 245KB / 24 calls | 21KB / 9 calls | **91%** |

<details>
<summary>Per-scenario breakdown (urfave/cli)</summary>

| Workflow | Baseline | edr | Reduction |
|---|---|---|---|
| Understand a class API | 21,941B (read whole file) | 3,698B (`--signatures`) | **83%** |
| Read a specific function | 21,927B (read whole file) | 1,955B (symbol read) | **91%** |
| Find refs | 83,997B / 4 calls (`grep` + read matched files) | 1,055B / 1 call (`refs`) | **99%** |
| Search with context | 4,634B (`grep -C3`) | 4,153B (`search --text --context 3`) | **10%** |
| Orient in codebase | 65,238B / 5 calls (glob + reads) | 2,154B / 1 call (`map`) | **97%** |
| Edit a function | 25,164B / 3 calls (read + edit + verify) | 481B / 1 call (inline diff) | **98%** |
| Add method to a class | 22,141B / 2 calls (read + edit) | 161B / 1 call (`--inside`) | **99%** |
| Multi-file read | 42,465B / 3 calls | 2,639B / 1 call (batched + budget) | **94%** |
| Explore a symbol | 42,536B / 4 calls (grep + reads) | 5,437B / 1 call (body + callers + deps) | **87%** |
| **Total** | **330,043B / 24 calls** | **21,733B / 9 calls** | **93%** |

</details>

Reproduce: `bash bench/run_real_repo_benchmarks.sh` (clones repos to `/tmp`, ~5 min).

## Install

For cloud agents and CI, the setup script handles everything — installs Go and gcc if needed, builds, adds to PATH:

```bash
git clone https://github.com/jordw/edr.git
./edr/setup.sh /path/to/your/project
```

If you already have Go 1.25+ and a C compiler:

```bash
go install github.com/jordw/edr@latest
```

edr stores its index in `.edr/` at the repo root. Add `.edr/` to your `.gitignore`. Delete it at any time — it rebuilds automatically on the next query.

## How it works

edr uses [tree-sitter](https://tree-sitter.github.io/tree-sitter/) to parse source files, extracts symbols (functions, classes, structs, methods) with their byte ranges, and stores them in a SQLite index. This makes every operation symbol-aware: reads return just the symbol you asked for, searches scope results to symbol boundaries, and edits can target a symbol by name.

Sessions track what content the agent has already seen. Re-reading a file returns `{unchanged: true}` or a diff of what changed. Symbol bodies that appeared in a previous response are replaced with `[in context]`. Small edit diffs are inlined automatically; large ones are summarized with on-demand retrieval. Sessions are automatic (derived from the parent process ID) — no setup required.

## Capabilities

| Capability | What it does |
|---|---|
| Symbol reads | Read a function or class by name — not the whole file |
| `--signatures` | A container's API without implementation bodies |
| `--depth N` | Progressive disclosure — collapse nesting below level N |
| `--inside` | Add a method to a class without reading the file first |
| Token budgets | Cap any response to N tokens; large repos degrade gracefully |
| Semantic refs | Import-aware references, false positives filtered (Go, Python, JS, TS) |
| Batch operations | Reads, searches, edits, writes, renames, and verify in one call |
| Cross-file rename | Import-aware, with `--dry-run` preview |

## Batch interface

Individual commands work standalone (`edr read`, `edr search`, `edr edit`), but the batch interface is how agents typically use edr — gathering context and making changes in minimal round trips:

```bash
# Call 1: gather context
edr do '{
  "reads": [
    {"file": "lib/scheduler.py", "symbol": "Scheduler", "signatures": true},
    {"file": "lib/scheduler.py", "symbol": "_execute_task"}
  ],
  "queries": [
    {"cmd": "search", "pattern": "retry", "body": true},
    {"cmd": "map", "dir": "internal/", "type": "function"}
  ]
}'

# Call 2: make changes + verify
edr do '{
  "edits": [{"file": "lib/scheduler.py", "old_text": "self._running = True", "new_text": "self._running = False"}],
  "writes": [{"file": "lib/scheduler_test.py", "content": "...", "mkdir": true}],
  "verify": true
}'
```

An `edr do` call can mix **reads**, **queries** (search, map, explore, refs, find, diff), **edits** (old_text/new_text, symbol replacement, regex, move), **writes** (create, append, insert inside a class), **renames** (cross-file, import-aware), and **verify** (build/test).

## CLI reference

| Command | What it does |
|---|---|
| `edr read file:Symbol` | Read a specific symbol (function, class, struct) |
| `edr read file:Class --signatures` | Container API without implementation bodies |
| `edr search "pattern" --body` | Symbol search with optional body snippets |
| `edr search "pattern" --text` | Text search (like grep, structured output) |
| `edr map` | Symbol overview of the repo or a directory |
| `edr explore Symbol --gather --body` | Symbol body + callers + deps in one call |
| `edr refs Symbol --impact` | Transitive impact analysis before refactoring |
| `edr edit file --old_text x --new_text y` | Edit with inline diff, auto re-index |
| `edr write file --inside Class` | Add a method/field without reading the file |
| `edr rename old new --dry-run` | Cross-file, import-aware rename with preview |
| `edr find "**/*.go"` | Find files by glob pattern |
| `edr verify` | Run build/test checks (auto-detects Go/npm/Cargo) |
| `edr do '{...}'` | Batch any combination of the above |
| `edr init` | Build or rebuild the symbol index |

## How edr compares

| Tool | Strength | Tradeoff |
|---|---|---|
| **ripgrep** | Fast text search, zero setup, universal | No symbol awareness or structured output. edr adds scoping but requires indexing |
| **ctags** | Mature symbol indexing, wide editor support | Index only — no reads, edits, or sessions. edr is the index *and* the access layer, but with fewer languages than ctags |
| **LSP** | Deep per-language semantics, refactoring | Richer type info than edr, but requires a running server per language. edr is a single binary across 13 languages |
| **Built-in agent tools** | No setup, always available | File-at-a-time with no symbol awareness. edr reduces context but adds a build dependency |

## Supported languages

**Full symbol indexing** (map, read, edit, signatures, inside, move):
Go, Python, JavaScript/JSX, TypeScript/TSX, Rust, Java, C, C++, Ruby, PHP, Zig, Lua, Bash/Shell

**Import-aware semantic refs** (refs, rename, explore callers/deps):
Go, Python, JavaScript, TypeScript — other languages fall back to text-based references.

edr can read and edit any text file regardless of language support.

## Limitations

- **C compiler required** — tree-sitter grammars need CGO. The setup script handles this, but it's a real dependency.
- **Semantic refs are partial** — import-aware reference tracking covers Go, Python, JS, and TS. Other languages fall back to text matching, which can produce false positives.
- **Tree-sitter, not LSP** — the index captures structure (functions, classes, types) but not full type information. It won't catch everything a language server would.
- **Indexing cost** — first `edr init` takes a few seconds on small repos, longer on large ones. Incremental re-indexing after edits is fast.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for development setup, project structure, and guidelines. Bug reports and pull requests welcome on [GitHub](https://github.com/jordw/edr/issues).

## License

[MIT](LICENSE)
