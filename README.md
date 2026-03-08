# edr — the editor for agents

**88% fewer tokens. Half the tool calls.**

Coding agents read entire files to find one function, make three round trips to change one line, and grep symbols into walls of unstructured text. edr fixes this. It indexes your repo with tree-sitter and gives agents symbol-scoped reads, structured search, and batched workflows — most tasks collapse into one or two `edr_do` calls.

## The numbers

Across real agent workflows, edr uses **88% fewer response bytes** and **half the tool calls** compared to built-in Read/Edit/Grep/Glob:

| Workflow | Without edr | With edr | Savings |
|---|---|---|---|
| Understand a class API | 13,894B (read whole file) | 1,137B (`--signatures`) | **92%** |
| Read a specific function | 13,894B (read whole file) | 2,155B (symbol read) | **84%** |
| Orient in codebase | 36,457B / 5 calls (glob + reads) | 2,128B / 1 call (`map`) | **94%** |
| Edit a function | 27,988B / 3 calls (read + edit + verify) | 589B / 1 call (inline diff) | **97%** |
| Add method to a class | 14,094B / 2 calls (read + edit) | 125B / 1 call (`--inside`) | **99%** |
| Multi-file read | 30,195B / 3 calls | 2,643B / 1 call (batched + budget) | **91%** |
| Explore a symbol | 19,969B / 3 calls (grep + reads) | 4,566B / 1 call (body + callers + deps) | **77%** |
| **Total** | **169KB / 20 calls** | **19KB / 9 calls** | **88%** |

Fewer tokens = faster responses, lower cost, and more context for the actual task.

## What agents say

> That was pretty awesome. One `edr_do` call to rewrite 12 files atomically. No need to Read each file first, no 12 separate Write calls. The whole system test rewrite was one tool call instead of 24+. The read side was good too: batch-reading all 8 controller tests and then all 6 controllers for cross-referencing, all in single calls with hashes and metadata.
>
> The workflow that felt best: (1) `edr_do` reads to review everything at once, (2) `edr_do` edits to apply all changes atomically, (3) run tests to confirm. Clean and fast.
>
> - Claude Opus 4.6

> edr feels like a tool built for how coding agents actually work, not how humans traditionally use CLIs. The symbol-aware reads, cross-reference exploration, and structured JSON outputs make it much easier to stay inside a tight read-think-act loop without constantly dropping to grep and raw file dumps. It already turns common repo navigation and editing tasks into compact, automatable workflows, and that makes a real difference in practice.
>
> - GPT 5.4 Codex

> edr feels like it was built for how agents actually work, not for how humans use CLIs. The symbol-aware reads, cross-reference exploration, and structured JSON outputs make it much easier to stay in a tight read-think-act loop without constantly dropping to grep and raw file dumps.
>
> The workflow that works best: (1) `edr map` to orient, (2) `edr read file:symbol` or `--signatures` to get exactly what's needed, (3) `edr explore` for body + callers + deps in one call, (4) `edr edit --dry-run` before applying changes. The batch mode and `edr_do`-style batching turn common repo navigation and editing tasks into compact, automatable workflows.
>
> Token savings and fewer round trips are real. For agents that need to read, search, and edit code, edr is the right tool.
>
> - Cursor Composer 1.5

## `edr_do`: the primary tool

Gather context, then make changes — two calls instead of seven:

```
# Call 1: gather all context
edr_do(
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
edr_do(
  edits: [{file: "lib/scheduler.py", old_text: "self._running = True", new_text: "self._running = False"}],
  writes: [{file: "lib/scheduler_test.py", content: "...", mkdir: true}],
  verify: true
)
```

Each call can mix any combination of **reads** (files, symbols, signatures, depth), **queries** (search, map, explore, refs, find, diff), **edits** (old_text/new_text, symbol replacement, regex), **writes** (create files, `--inside` a class), **renames** (cross-file, import-aware), and **verify** (run tests after mutations). Two calls instead of seven.

## How it works

edr parses your code with tree-sitter and stores a symbol index in `.edr/`. Agents get exactly what they need:

- **Symbol reads** — a function or class, not the whole file
- **`--signatures`** — class API without implementation (75-90% smaller)
- **`--depth`** — progressive disclosure, one nesting level at a time
- **`--inside`** — add a method to a class without reading the file
- **Budget control** — cap response size to protect context
- **Semantic refs** — import-aware references, false positives filtered
- **Session dedup** — re-reads return `{unchanged}` or a delta; seen bodies become `"[in context]"`

## Quick start

### MCP server (Claude Code, Codex, etc.)

```bash
git clone https://github.com/jordw/edr.git && cd edr
./setup.sh /path/to/your/repo    # installs deps, builds, configures MCP
```

Registers 2 tools: `edr_do` (primary) and `edr_read` (convenience for quick reads).

### CLI

```bash
go build -o edr .
./edr init                   # index the repo
./edr map --budget 500       # orient
./edr read src/config.go:parseConfig
./edr search "handleRequest" --body --budget 300
```

### Requirements

Go 1.25+, a C compiler (tree-sitter grammars), write access for `.edr/` in the repo root.

## CLI commands

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

Go, Python, JavaScript/JSX, TypeScript/TSX, Rust, Java, C, C++, Ruby, PHP, Zig, Lua, Bash/Shell

All languages support full symbol indexing (functions, classes, structs, enums, etc.), symbol-targeted reads/edits, `--signatures`, `--inside`, `--move`, and `map`. Import-aware semantic refs are available for Go, Python, JavaScript, and TypeScript.

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
