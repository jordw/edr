# edr — the editor for agents

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.25+-00ADD8.svg)](https://go.dev)

**91–98% less context burned across 6 real-world repos (Go, Python, Ruby, TypeScript). 63% fewer tool calls.**

Coding agents waste context. They read entire files to find one function, make three round trips to change one line, and grep symbols into walls of unstructured text.

edr fixes this. It parses your repo with tree-sitter, stores a symbol index in SQLite, and gives agents exactly what they need — symbol-scoped reads, structured search, and batched mutations. Most tasks collapse into one or two `edr` calls.

## Quick start

### MCP server (Claude Code, Codex, etc.)

```bash
git clone https://github.com/jordw/edr.git && cd edr
./setup.sh /path/to/your/repo    # installs deps, builds, configures MCP
```

Registers 1 tool: `edr` — handles reads, queries, edits, writes, renames, and verification.
The tool schema is ~1,000 tokens. Self-documenting fields (file, symbol, pattern, etc.) omit descriptions to minimize per-conversation overhead.

### Install from source

```bash
go install github.com/jordw/edr@latest
# or: go build -o edr . && go install
```

### CLI

```bash
edr init                   # index the repo
edr map --budget 500       # orient
edr read src/config.go:parseConfig
edr search "handleRequest" --body --budget 300
```

### Requirements

Go 1.25+, a C compiler (tree-sitter grammars), write access for `.edr/` in the repo root.

## `edr`: the primary tool

Gather context, then make changes — two calls instead of seven:

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

Each call can mix any combination of **reads** (files, symbols, signatures, depth), **queries** (search, map, explore, refs, find, diff), **edits** (old_text/new_text, symbol replacement, regex), **writes** (create files, `--inside` a class), **renames** (cross-file, import-aware), and **verify** (run tests after mutations). Two calls instead of seven.

## How it works

edr runs tree-sitter over every source file, extracts symbols (functions, classes, structs, etc.) with their byte ranges, and stores them in a SQLite index at `.edr/index.db`. At query time, agents get exactly what they need without reading entire files:

- **Symbol reads** — a function or class, not the whole file
- **`--signatures`** — class API without implementation (75-86% smaller)
- **`--depth`** — progressive disclosure, one nesting level at a time
- **`--inside`** — add a method to a class without reading the file
- **Budget control** — cap response size to protect context
- **Semantic refs** — import-aware references, false positives filtered
- **Session dedup** — re-reads return `{unchanged}` or a delta; seen bodies become `"[in context]"`

## CLI commands

| Command | What it does |
|---|---|
| `edr '{"reads":[...], "queries":[...]}'` | Batched JSON — same interface as the MCP tool |
| `edr map` | Symbol overview of the repo or a directory |
| `edr read file:Symbol` | Read a specific symbol (function, class, struct) |
| `edr read file:Class --signatures` | Class API without implementation bodies |
| `edr search "pattern" --body` | Symbol search with optional body snippets |
| `edr explore Symbol --gather --body` | Symbol body + callers + deps in one call |
| `edr refs Symbol --impact` | Transitive impact analysis before refactoring |
| `edr edit file --old_text x --new_text y` | Edit with inline diff, auto re-index |
| `edr write file --inside Class` | Add a method without reading the file |
| `edr rename old new --dry-run` | Cross-file, import-aware rename with preview |

## Supported languages

Go, Python, JavaScript/JSX, TypeScript/TSX, Rust, Java, C, C++, Ruby, PHP, Zig, Lua, Bash/Shell

All languages support full symbol indexing (functions, classes, structs, enums, etc.), symbol-targeted reads/edits, `--signatures`, `--inside`, `--move`, and `map`. Import-aware semantic refs are available for Go, Python, JavaScript, and TypeScript.

## Project structure

```
cmd/           CLI commands, MCP server
internal/
  index/       tree-sitter parsing, SQLite symbol index
  search/      symbol and text search
  edit/        file edits, transactions, diffing
  dispatch/    command routing (CLI, batch, MCP)
  gather/      context collection with token budgets
  session/     MCP session state (deltas, dedup)
  trace/       session tracing and benchmarks
  output/      structured JSON formatting
```

## The numbers

Benchmarked against simulated Read/Edit/Grep/Glob workflows across 6 real-world
repos (`bench/native_comparison.sh`, 9 scenarios each, 3 iterations, median bytes):

| Repo | Language | Native | edr | Savings |
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
For the benchmark plan and profile template, see [bench/REAL_REPOS.md](bench/REAL_REPOS.md).

## Agent instructions

For the full agent-facing command reference and CLAUDE.md instructions, see [CLAUDE.md](CLAUDE.md).

## Contributing

Bug reports and pull requests welcome on [GitHub](https://github.com/jordw/edr/issues).

## License

[MIT](LICENSE)
