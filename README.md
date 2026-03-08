# edr — the editor for agents

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.25+-00ADD8.svg)](https://go.dev)

**Up to 89% less context burned and 55% fewer tool calls than simulated default Read/Edit/Grep/Glob workflows.**

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

On the included benchmark fixture in `bench/testdata`, `bench/native_comparison.sh`
shows `edr` using **89% fewer response bytes** and **11 fewer tool calls**
than simulated Read/Edit/Grep/Glob workflows:

| Workflow | Without edr | With edr | Savings |
|---|---|---|---|
| Understand a class API | 13,894B (read whole file) | 1,137B (`--signatures`) | **91%** |
| Read a specific function | 13,894B (read whole file) | 2,155B (symbol read) | **84%** |
| Find refs | 175B (`grep`) | 406B (`refs`) | **-132%** |
| Search with context | 12,869B (`grep -C3`) | 4,823B (`search --text --context 3 --budget 500`) | **62%** |
| Orient in codebase | 36,457B / 5 calls (glob + reads) | 2,149B / 1 call (`map`) | **94%** |
| Edit a function | 27,988B / 3 calls (read + edit + verify) | 589B / 1 call (inline diff) | **97%** |
| Add method to a class | 14,094B / 2 calls (read + edit) | 125B / 1 call (`--inside`) | **99%** |
| Multi-file read | 30,195B / 3 calls | 2,643B / 1 call (batched + budget) | **91%** |
| Explore a symbol | 19,969B / 3 calls (grep + reads) | 4,566B / 1 call (body + callers + deps) | **77%** |
| **Total** | **169,535B / 20 calls** | **18,593B / 9 calls** | **89%** |

`refs` is larger than raw `grep` on this fixture because it returns structured,
symbol-aware results. The tradeoff is precision rather than smaller bytes.

Smaller responses still mean lower context pressure, lower cost, and faster tool
round trips for the actual task.

## Agent instructions

For the full agent-facing command reference and CLAUDE.md instructions, see [CLAUDE.md](CLAUDE.md).

## Contributing

Bug reports and pull requests welcome on [GitHub](https://github.com/jordw/edr/issues).

## License

[MIT](LICENSE)
