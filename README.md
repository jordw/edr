# edr

**Up to 90% fewer tokens on common agent workflows.**

Coding agents waste tokens. They read entire files to find one function. They make three round trips (read, edit, re-read) to change a single line. They grep for a symbol and get a wall of unstructured text.

edr is a token-efficient MCP server and CLI that gives agents surgical, budget-controlled access to your codebase. It indexes your repo with tree-sitter so agents can read individual symbols, search call graphs, and batch entire workflows into one or two tool calls. Its primary interface, `edr_plan`, lets an agent gather all context and apply all changes in just two calls instead of 7+ sequential reads, edits, and verifications.

## The numbers

Across 9 real workflows, edr uses **88% fewer response bytes** and **half the tool calls** compared to built-in Read/Edit/Grep/Glob:

| Workflow | Without edr | With edr | Savings |
|---|---|---|---|
| Understand a class API | 13,894B (read whole file) | 1,137B (`--signatures`) | **92%** |
| Read a specific function | 13,894B (read whole file) | 2,155B (symbol read) | **84%** |
| Orient in codebase | 36,457B / 5 calls (glob + reads) | 2,128B / 1 call (`map`) | **94%** |
| Edit a function | 27,988B / 3 calls (read + edit + verify) | 589B / 1 call (inline diff) | **97%** |
| Add method to a class | 14,094B / 2 calls (read + edit) | 125B / 1 call (`--inside`) | **99%** |
| Multi-file read | 30,195B / 3 calls | 2,643B / 1 call (batched + budget) | **91%** |
| Explore a symbol | 19,969B / 3 calls (grep + reads) | 4,566B / 1 call (body + callers + deps) | **77%** |
| **Total (9 workflows)** | **169KB / 20 calls** | **19KB / 9 calls** | **88%** |

Fewer tokens = faster responses, lower cost, and more room in context for the actual task. And fewer tool calls means less overhead. Each call carries inference latency and context-switching tax that compounds across a session.

## What agents say

> That was pretty awesome. One `edr_plan` call to rewrite 12 files atomically. No need to Read each file first, no 12 separate Write calls. The whole system test rewrite was one tool call instead of 24+. The read side was good too: batch-reading all 8 controller tests and then all 6 controllers for cross-referencing, all in single calls with hashes and metadata.
>
> The workflow that felt best: (1) `edr_plan` reads to review everything at once, (2) `edr_plan` edits to apply all changes atomically, (3) run tests to confirm. Clean and fast.
>
> - Claude Opus 4.6, after a controller refactor

## `edr_plan`: the primary tool

Most agent tasks follow a two-step pattern: gather context, then make changes. `edr_plan` handles both, batching reads, searches, edits, writes, and verification into a single call:

```
# Call 1: gather all context
edr_plan(
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
edr_plan(
  edits: [{file: "lib/scheduler.py", old_text: "self._running = True", new_text: "self._running = False"}],
  writes: [{file: "lib/scheduler_test.py", content: "...", mkdir: true}],
  verify: true
)
```

Two tool calls instead of seven. Each call can mix any combination of:

- **reads**: files, symbols, line ranges, `--signatures`, `--depth`
- **queries**: `search`, `map`, `explore`, `refs`, `find`
- **edits**: old_text/new_text, symbol replacement, line ranges
- **writes**: create files, `--inside` a class, `--after` a symbol
- **verify**: run `go test`, `npm test`, etc. after mutations

Without `edr_plan`, the same task takes 7 sequential calls:

```
Read("lib/scheduler.py")                    # 13,894B, whole file, just to see the class
Grep("retry", "lib/")                       # unstructured text results
Read("lib/scheduler.py")                    # 13,894B, yes, again, to find the edit target
Edit("lib/scheduler.py", old, new)          #    200B confirmation
Read("lib/scheduler.py")                    # 13,894B, a third time, to verify
Write("lib/scheduler_test.py", content)     #    200B
Bash("go test ./...")                        # verification
```

That's 42KB of response tokens, 7 round trips, and 7 inference cycles where the model has to decide what to do next. Each tool call adds latency and eats context with its own framing overhead. `edr_plan` collapses all of that into 2 calls.

## How it works

edr parses your code with tree-sitter and stores a symbol index in `.edr/`. When an agent asks for something, edr returns just what's needed:

- **Symbol-scoped reads**: read a function or class, not the whole file
- **`--signatures`**: see a class's API without its implementation (75-90% smaller)
- **`--depth`**: progressive disclosure, expand one nesting level at a time
- **`--inside`**: add a method to a class without reading the file first
- **Budget control**: cap response size so agents don't blow their context
- **Batching**: read, search, edit, write, and verify in a single `edr_plan` call
- **Semantic refs**: import-aware "find references" that filters false positives
- **Session dedup**: re-reading a file returns a delta, not the whole thing again

## Quick start

### As an MCP server (Claude Code, Codex, etc.)

```bash
# One command. Installs deps if needed, builds, configures MCP:
./setup.sh /path/to/your/repo

# Or manually:
go build -o edr .
./edr mcp                   # starts the MCP server
```

This registers 13 tools: `edr_plan`, `edr_read`, `edr_edit`, `edr_write`, `edr_search`, `edr_map`, `edr_explore`, `edr_refs`, `edr_find`, `edr_rename`, `edr_verify`, `edr_init`, `edr_diff`.

### As a CLI

```bash
go build -o edr .
./edr init                   # index the repo
./edr map --budget 500       # see what's here
./edr read src/config.go:parseConfig
./edr search "handleRequest" --body --budget 300
```

### Requirements

- Go 1.25+
- A C compiler (for tree-sitter grammars)
- Write access to create `.edr/` in the repo root

## Other commands

| Command | What it does |
|---|---|
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

Go, Python, JavaScript/JSX, TypeScript/TSX, Rust, Java, C/H, Ruby

## Running the benchmarks

```bash
bash bench/native_comparison.sh    # edr vs Read/Edit/Grep/Glob
bash bench/workflow_benchmark.sh   # real agent workflows (signatures, depth, inside)
bash bench/insert_benchmark.sh     # --inside vs read+edit across languages
go test -bench=. -count=5 ./bench/ # Go microbenchmarks
```

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
  output/      structured JSON formatting
```

## Agent instructions

For the full agent-facing command reference and CLAUDE.md instructions, see [CLAUDE.md](CLAUDE.md).
