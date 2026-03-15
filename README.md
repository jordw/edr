# edr — the editor for agents

[![CI](https://github.com/jordw/edr/actions/workflows/ci.yml/badge.svg)](https://github.com/jordw/edr/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.25+-00ADD8.svg)](https://go.dev)

Coding agents burn through context reading entire files to find one function, making three round trips to change one line, and grepping symbols into walls of unstructured text.

edr fixes this. It parses your codebase with [tree-sitter](https://tree-sitter.github.io/tree-sitter/), stores a symbol index in SQLite, and gives agents exactly what they need: symbol-scoped reads, structured search, and batched mutations. Most tasks collapse into one or two calls.

**91-98% less context across 6 real-world repos. 63% fewer tool calls.** ([benchmarks](#benchmarks))

## Install

```bash
go install github.com/jordw/edr@latest
```

Requires Go 1.25+ and a C compiler (for tree-sitter grammars). edr stores its index in `.edr/` at the repo root — add it to your `.gitignore`.

## Quick start

```bash
cd your-repo
edr init                                    # build the symbol index
edr map --budget 500                        # repo overview
edr read src/config.go:parseConfig          # read one symbol, not the whole file
edr search "handleRequest" --body           # structured symbol search
edr refs parseConfig --impact               # who calls this, transitively?
edr edit src/config.go --old_text x --new_text y   # edit + auto re-index
edr write src/config.go --inside Config     # add a field without reading the file
edr rename oldFunc newFunc --dry-run        # cross-file rename with preview
```

## The two-call pattern

Agents gather context in one call, then make changes in another:

```
# Call 1: gather all context
edr(
  reads: [
    {file: "lib/scheduler.py", symbol: "Scheduler", signatures: true},
    {file: "lib/scheduler.py", symbol: "_execute_task"}
  ],
  queries: [
    {cmd: "search", pattern: "retry", body: true},
    {cmd: "map", dir: "internal/", type: "function"}
  ]
)

# Call 2: make changes + verify
edr(
  edits: [{file: "lib/scheduler.py", old_text: "self._running = True", new_text: "self._running = False"}],
  writes: [{file: "lib/scheduler_test.py", content: "...", mkdir: true}],
  verify: true
)
```

Each call can mix **reads** (files, symbols, signatures, depth), **queries** (search, map, explore, refs, find, diff), **edits** (old_text/new_text, symbol replacement, regex, move), **writes** (create files, append, insert inside a class), **renames** (cross-file, import-aware), and **verify** (build/test after mutations).

For the full agent-facing reference, see [CLAUDE.md](CLAUDE.md).

## How it works

edr runs tree-sitter over every source file, extracts symbols (functions, classes, structs, etc.) with their byte ranges, and stores them in a SQLite index. At query time, agents get exactly what they need:

- **Symbol reads** — read a function or class, not the whole file
- **`--signatures`** — a class's API without implementation (75-86% smaller)
- **`--depth`** — progressive disclosure, one nesting level at a time
- **`--inside`** — add a method to a class without reading the file first
- **Budget control** — cap any response to N tokens so large repos degrade gracefully
- **Semantic refs** — import-aware references with false positives filtered out
- **Session dedup** — re-reads return `{unchanged: true}` or a delta diff; repeated bodies become `"[in context]"`

### Context efficiency

edr combines several mechanisms to minimize context usage:

| Technique | What it does |
|---|---|
| Hash-chained edits | Each read/edit returns a file hash; pass it as `expect_hash` to reject stale writes |
| Incremental indexing | Content hashes skip re-parsing unchanged files |
| Session delta reads | Re-reading a file returns `{unchanged}` or a diff, not the full content |
| Body dedup | Repeated symbol bodies in search/explore are replaced with `"[in context]"` |
| Slim edit responses | Small diffs inline; large diffs summarized with on-demand retrieval |
| Parallel batching | Independent reads/queries in one call run concurrently |
| Parallel text search | Files searched concurrently with smart budget trimming (context → line truncation → match cap) |
| Atomic multi-file edits | Grouped edits validate in memory, write via temp-file rename, reindex in one batch |
| TOCTOU guard | File hash revalidated before writing; external modifications detected and aborted |
| Mutation status | Every edit/write returns `status` ("applied", "applied_index_stale") for trustworthy automation |
| Change summary | Mutation responses include `summary` with counts, status, and actionable `hints` |
| Auto-sessions | PPID-based sessions by default; `--no-session` to disable |
| Parse-tree caching | Repeated tree-sitter parses reuse cached ASTs keyed by content fingerprint |
| Freshness metadata | Read responses include `mtime` so agents can see file age without extra calls |

### Local data

edr stores all data in `.edr/` at the repo root (gitignored). This includes the symbol index (`index.db`), session files (`sessions/`), and traces (`traces.db`). Sessions are automatic (PPID-based) — delta reads, body dedup, and slim edits work out of the box. Delete `.edr/` at any time to reset.

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
| `edr '{"reads":[...], "edits":[...], "verify":true}'` | Batched JSON — reads, edits, writes, verify in one call |

## Supported languages

**Full symbol indexing** (map, read, edit, signatures, inside, move):
Go, Python, JavaScript/JSX, TypeScript/TSX, Rust, Java, C, C++, Ruby, PHP, Zig, Lua, Bash/Shell

**Import-aware semantic refs** (refs, rename, explore callers/deps):
Go, Python, JavaScript, TypeScript — other languages fall back to text-based references.

edr can read and edit any text file regardless of language support.

## Benchmarks

Benchmarked against simulated Read/Edit/Grep/Glob workflows (the tools agents use without edr) across 6 real-world repos — 9 scenarios each, 3 iterations, median bytes:

| Repo | Language | Without edr | With edr | Savings |
|---|---|---|---|---|
| [urfave/cli](https://github.com/urfave/cli) | Go | 322KB / 24 calls | 21KB / 9 calls | **93%** |
| [vitess/sqlparser](https://github.com/vitessio/vitess) | Go | 660KB / 21 calls | 16KB / 9 calls | **98%** |
| [vitess/vtgate](https://github.com/vitessio/vitess) | Go | 929KB / 23 calls | 32KB / 9 calls | **97%** |
| [pallets/click](https://github.com/pallets/click) | Python | 455KB / 24 calls | 21KB / 9 calls | **95%** |
| [rails/thor](https://github.com/rails/thor) | Ruby | 234KB / 24 calls | 15KB / 9 calls | **94%** |
| [reduxjs/redux-toolkit](https://github.com/reduxjs/redux-toolkit) | TypeScript | 245KB / 24 calls | 21KB / 9 calls | **91%** |

Scenarios: understand API (`--signatures`), read symbol, find refs, search with context,
orient (`map`), edit function, add method (`--inside`), multi-file read, explore symbol.

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

Reproduce: `bash bench/run_real_repo_benchmarks.sh` (clones repos to `/tmp`, ~5 min).

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

Bug reports and pull requests welcome on [GitHub](https://github.com/jordw/edr/issues).

### Development setup

```bash
git clone https://github.com/jordw/edr.git && cd edr
go build -o edr .       # build
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
