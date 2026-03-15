# edr — the editor for agents

[![CI](https://github.com/jordw/edr/actions/workflows/ci.yml/badge.svg)](https://github.com/jordw/edr/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.25+-00ADD8.svg)](https://go.dev)

A file-access CLI for coding agents. Agents read one function instead of a whole file, and batch complex workflows — context gathering, multi-file edits, new files, verification — into a single transactional call.

edr indexes your codebase by symbol and exposes it through a batch JSON interface with automatic session tracking, token budgets, and deduplication so repeated reads shrink to diffs.

## What it looks like

Agents typically gather context in one call, then make changes in another:

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

Each call can mix **reads** (files, symbols, signatures, depth), **queries** (search, map, explore, refs, find, diff), **edits** (old_text/new_text, symbol replacement, regex, move), **writes** (create files, append, insert inside a class), **renames** (cross-file, import-aware), and **verify** (build/test after mutations).

Individual commands also work standalone:

```bash
edr map --budget 500                        # repo overview
edr read src/config.go:parseConfig          # one symbol, not the whole file
edr search "handleRequest" --body           # structured symbol search
edr refs parseConfig --impact               # transitive callers
edr edit src/config.go --old_text x --new_text y   # edit + auto re-index
edr write src/config.go --inside Config     # add a field without reading the file
edr rename oldFunc newFunc --dry-run        # cross-file rename with preview
```

## Benchmarks

Benchmarked against simulated baseline workflows that model how agents typically use Read/Edit/Grep/Glob (e.g., reading a whole file to get one function, grepping then reading each matched file). 6 real-world repos, 9 scenarios each, 3 iterations, median response bytes:

| Repo | Language | Without edr | With edr | Savings |
|---|---|---|---|---|
| [urfave/cli](https://github.com/urfave/cli) | Go | 322KB / 24 calls | 21KB / 9 calls | **93%** |
| [vitess/sqlparser](https://github.com/vitessio/vitess) | Go | 660KB / 21 calls | 16KB / 9 calls | **98%** |
| [vitess/vtgate](https://github.com/vitessio/vitess) | Go | 929KB / 23 calls | 32KB / 9 calls | **97%** |
| [pallets/click](https://github.com/pallets/click) | Python | 455KB / 24 calls | 21KB / 9 calls | **95%** |
| [rails/thor](https://github.com/rails/thor) | Ruby | 234KB / 24 calls | 15KB / 9 calls | **94%** |
| [reduxjs/redux-toolkit](https://github.com/reduxjs/redux-toolkit) | TypeScript | 245KB / 24 calls | 21KB / 9 calls | **91%** |

<details>
<summary>Per-scenario breakdown (urfave/cli)</summary>

| Workflow | Without edr | With edr | Savings |
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

Reproduce: `bash bench/run_real_repo_benchmarks.sh` (clones repos to `/tmp`, ~5 min). Baseline workflows and edr equivalents are defined in [`bench/scenarios/`](bench/scenarios/) — inspect them to judge fairness.

## Install

```bash
go install github.com/jordw/edr@latest
```

Requires Go 1.25+ and a C compiler (for tree-sitter grammars).

For cloud agents and CI environments, the setup script handles everything (installs Go/gcc if needed, builds, adds to PATH):

```bash
git clone https://github.com/jordw/edr.git
./edr/setup.sh /path/to/your/project
```

edr stores its index in `.edr/` at the repo root (symbol index, session files, traces). Add `.edr/` to your `.gitignore`. Delete it at any time to reset.

## How it works

edr uses [tree-sitter](https://tree-sitter.github.io/tree-sitter/) to parse source files, extracts symbols (functions, classes, structs, etc.) with their byte ranges, and stores them in a SQLite index. On top of that index, it layers session tracking and batching:

| Capability | What it does |
|---|---|
| Symbol reads | Read a function or class by name, not the whole file |
| `--signatures` | A class's API without implementation bodies (75-86% smaller) |
| `--depth` | Progressive disclosure — one nesting level at a time |
| `--inside` | Add a method to a class without reading the file first |
| Token budgets | Cap any response to N tokens so large repos degrade gracefully |
| Semantic refs | Import-aware references with false positives filtered out (Go, Python, JS, TS) |
| Delta reads | Re-reading a file/symbol returns `{unchanged: true}` or a diff |
| Body dedup | Repeated symbol bodies in search/explore replaced with `"[in context]"` |
| Slim edits | Small diffs inline; large diffs summarized with on-demand retrieval |
| Hash-chained edits | File hashes detect stale writes; `expect_hash` rejects them |
| Atomic batching | Grouped edits validate in memory, write atomically, reindex in one pass |

Sessions are PPID-based and automatic — no setup required. `--no-session` to disable.

## CLI reference

| Command | What it does |
|---|---|
| `edr init` | Build or rebuild the symbol index |
| `edr map` | Symbol overview of the repo or a directory |
| `edr read file:Symbol` | Read a specific symbol (function, class, struct) |
| `edr read file:Class --signatures` | Class API without implementation bodies |
| `edr search "pattern" --body` | Symbol search with optional body snippets |
| `edr search "pattern" --text` | Text search (like grep, but structured output) |
| `edr explore Symbol --gather --body` | Symbol body + callers + deps in one call |
| `edr refs Symbol --impact` | Transitive impact analysis before refactoring |
| `edr edit file --old_text x --new_text y` | Edit with inline diff, auto re-index |
| `edr write file --inside Class` | Add a method/field without reading the file |
| `edr rename old new --dry-run` | Cross-file, import-aware rename with preview |
| `edr find "**/*.go"` | Find files by glob pattern |
| `edr verify` | Run build/test checks (auto-detects Go/npm/Cargo) |
| `edr do '{"reads":[...], "edits":[...], "verify":true}'` | Batch operations in one call |
| `edr bench-session` | Score a session's efficiency (delta hits, edit success rate) |

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

## How edr compares

| Tool | What it does well | Tradeoff vs. edr |
|---|---|---|
| **ripgrep** | Fast text search, zero setup, universal | No symbol awareness or structured output. edr adds scoping but requires indexing |
| **ctags** | Mature symbol indexing, wide editor support | Index only — no reads, edits, or sessions. edr is the index *and* the access layer, but with fewer languages than ctags |
| **LSP** | Deep per-language semantics, refactoring | Richer type info than edr, but requires a running server per language. edr is a single binary across 13 languages |
| **Built-in agent tools** | No setup, always available | File-at-a-time with no symbol awareness. edr reduces context but adds a build dependency |

## Project structure

```
cmd/           CLI commands, batch orchestrator
internal/
  cmdspec/     canonical command registry (names, categories, flags)
  index/       tree-sitter parsing, SQLite symbol index
  search/      symbol and text search (parallel, cached)
  edit/        file edits, transactions, diffing
  dispatch/    command routing and execution
  gather/      context collection with token budgets
  session/     auto-session state (deltas, dedup, slim edits)
  trace/       session tracing and benchmarks
  output/      structured JSON formatting
bench/         benchmarks and multi-language test suite
```

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for details. Bug reports and pull requests welcome on [GitHub](https://github.com/jordw/edr/issues).

### Development setup

```bash
git clone https://github.com/jordw/edr.git && cd edr
go build -o edr .       # build (requires Go 1.25+ and a C compiler)
go test ./...            # run all tests
```

After changing Go source files, rebuild with `go build -o edr . && go install`.

### Running benchmarks

```bash
go test ./bench/ -bench . -benchmem                    # Go benchmarks
go test ./bench/ -run TestSessionMultiLang -v           # multi-language session test
bash bench/run_real_repo_benchmarks.sh                  # real-repo comparison (~5 min)
```

## License

[MIT](LICENSE)
