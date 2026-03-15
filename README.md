# edr: the editor for agents

[![CI](https://github.com/jordw/edr/actions/workflows/ci.yml/badge.svg)](https://github.com/jordw/edr/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.25+-00ADD8.svg)](https://go.dev)

Coding agents waste most of their context on content they don't need. They read entire files to find one function, grep then re-read every match, and edit files one at a time with no verification.

edr indexes your codebase by symbol and lets agents read exactly what they need, batch operations into single calls, and track what's already in context so repeated reads shrink to diffs.

## Before and after

Adding a retry parameter to a scheduler. Without edr (8 tool calls, ~180KB of context):

```bash
Read src/scheduler.py              # 14KB, whole file for one class
Grep "retry" src/                  # file:line list, no structure
Read src/config.py                 # 11KB, whole file for one function
Read src/worker.py                 # 9KB, whole file for the API shape
Read src/retry.py                  # 6KB, whole file for one call site
Edit src/scheduler.py              # old_text/new_text, no verification
Read src/scheduler.py              # re-read to confirm the edit took
Run "python -m pytest"             # separate verify step
```

With edr (2 calls, ~8KB of context):

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

# Call 2: edit + write + verify
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

Symbol reads return the function or class asked for, not the whole file. `--signatures` returns a container's public API without implementation bodies (75-86% smaller). Refs are import-aware. Multi-file edits are atomic and auto-reindex. Sessions track what's already in context so repeated reads return diffs instead of full content.

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

For cloud agents and CI, the setup script installs Go and gcc if needed, builds edr, and adds it to PATH:

```bash
git clone https://github.com/jordw/edr.git
./edr/setup.sh /path/to/your/project
```

If you already have Go 1.25+ and a C compiler:

```bash
go install github.com/jordw/edr@latest
```

edr stores its index in `.edr/` at the repo root. Add `.edr/` to your `.gitignore`. The index rebuilds automatically if deleted.

## How it works

edr uses [tree-sitter](https://tree-sitter.github.io/tree-sitter/) to parse source files, extract symbols (functions, classes, structs, methods) with their byte ranges, and store them in a SQLite index. This makes every operation symbol-aware: reads return the symbol you asked for, searches scope results to symbol boundaries, and edits can target a symbol by name.

Sessions track what content the agent has already seen. Re-reading a file returns `{unchanged: true}` or a diff of what changed. Symbol bodies from previous responses are replaced with `[in context]`. Small edit diffs are inlined; large ones are summarized with on-demand retrieval. Sessions are derived from the parent process ID and require no setup.

## CLI reference

| Command | What it does |
|---|---|
| `edr read file:Symbol` | Read a specific symbol (function, class, struct) |
| `edr read file:Class --signatures` | Container API without implementation bodies |
| `edr read file --depth N` | Progressive disclosure: collapse nesting below level N |
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

All output is structured JSON. Token budgets (`--budget N`) cap any response to N tokens.

## Batch interface

Individual commands work standalone, but the batch interface is how agents typically use edr, gathering context and making changes in minimal round trips:

```bash
edr do '{
  "reads": [{"file": "src/scheduler.py", "symbol": "Scheduler", "signatures": true}],
  "queries": [{"cmd": "search", "pattern": "retry", "body": true}],
  "edits": [{"file": "src/scheduler.py", "old_text": "old", "new_text": "new"}],
  "writes": [{"file": "tests/test_retry.py", "content": "...", "mkdir": true}],
  "renames": [{"old_name": "OldFunc", "new_name": "NewFunc", "dry_run": true}],
  "verify": true
}'
```

A single `edr do` call can mix **reads**, **queries** (search, map, explore, refs, find, diff), **edits** (old_text/new_text, symbol replacement, regex, move), **writes** (create, append, insert inside a class), **renames** (cross-file, import-aware), and **verify** (build/test).

## How edr compares

| Tool | Strength | Tradeoff |
|---|---|---|
| **ripgrep** | Fast text search, zero setup, universal | No symbol awareness or structured output. edr adds scoping but requires indexing |
| **ctags** | Mature symbol indexing, wide editor support | Index only, no reads/edits/sessions. edr is the index *and* the access layer, but supports fewer languages |
| **LSP** | Deep per-language semantics, refactoring | Richer type info, but requires a running server per language. edr is a single binary across 13 languages |
| **Built-in agent tools** | No setup, always available | File-at-a-time with no symbol awareness. edr reduces context but adds a build dependency |

## Supported languages

**Full symbol indexing** (map, read, edit, signatures, inside, move):
Go, Python, JavaScript/JSX, TypeScript/TSX, Rust, Java, C, C++, Ruby, PHP, Zig, Lua, Bash/Shell

**Import-aware semantic refs** (refs, rename, explore callers/deps):
Go, Python, JavaScript, TypeScript. Other languages fall back to text-based references.

edr can read and edit any text file regardless of language support.

## Limitations

- **C compiler required.** Tree-sitter grammars need CGO. The setup script handles this, but it is a real dependency.
- **Semantic refs are partial.** Import-aware reference tracking covers Go, Python, JS, and TS. Other languages use text matching, which can produce false positives.
- **Tree-sitter, not LSP.** The index captures structure (functions, classes, types) but not full type information. It will not catch everything a language server would.
- **Indexing cost.** First `edr init` takes a few seconds on small repos, longer on large ones. Incremental re-indexing after edits is fast.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for development setup, project structure, and guidelines. Bug reports and pull requests welcome on [GitHub](https://github.com/jordw/edr/issues).

## License

[MIT](LICENSE)
