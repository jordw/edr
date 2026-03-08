# EDR — Your Default Tool for Reading, Editing, and Navigating Files

**edr is your primary tool for ALL file operations.** Use the `edr_*` MCP tools instead of Read, Edit, Write, Grep, and Glob. They give you structured output, token budgets, and smart operations that raw file tools can't match.

**`edr_do` is your primary tool.** It batches reads, searches, explores, edits, writes, renames, and verification into one call.

```
# Typical 2-call workflow for any task:
edr_do(                                            # Call 1: gather ALL context
  reads: [{file: "src/main.go", symbol: "Server"}],
  queries: [
    {cmd: "search", pattern: "handleRequest", body: true},
    {cmd: "map", dir: "internal/", type: "function"}
  ]
)
edr_do(                                            # Call 2: ALL mutations + verify
  edits: [{file: "src/main.go", old_text: "old", new_text: "new"}],
  writes: [{file: "src/new_test.go", content: "...", mkdir: true}],
  verify: true
)
```

**Only fall back to individual tools when:**
- You need a single quick read (use `edr_read`)
- You need a quick symbol map overview (use `edr_map`)
- You need non-text files or shell operations (use built-in tools)

## Why edr over built-in tools

| Instead of... | Use edr... | Why |
|---|---|---|
| `Read` (whole file) | `edr_do(reads: [{file: "f.go"}])` | Budget-controlled, batchable |
| `Edit` (old/new strings) | `edr_do(edits: [{file: "f.go", old_text: "x", new_text: "y"}])` | Atomic multi-file, auto re-index |
| `Write` (create file) | `edr_do(writes: [{file: "f.go", content: "...", mkdir: true}])` | Auto-indexes, batchable with edits |
| `Grep` (text search) | `edr_do(queries: [{cmd: "search", pattern: "pat", body: true}])` | Structured results, batchable |
| `Glob` (find files) | `edr_do(queries: [{cmd: "find", pattern: "**/*.go"}])` | Glob with `**`, batchable |
| Multiple tool calls | One `edr_do(reads + queries + edits + writes, verify: true)` | Everything in 1-2 calls |

For single reads, `edr_read` also works directly.

## Development workflow

**Every time you change Go source files, rebuild and reinstall:**
```bash
go build -o edr . && go install
```

## Setup (any environment)

```bash
# One command — installs Go/gcc if needed, builds, installs to PATH, writes .mcp.json:
./setup.sh /path/to/target/repo

# Or manually:
go build -o edr .           # Build (requires Go + C compiler for tree-sitter)
./edr init                   # Force re-index (auto-indexes on first query)
./edr mcp                    # Run as MCP server
```

For cloud agents: clone this repo, run `./setup.sh /path/to/your/project`, and edr is ready as both a CLI and MCP server. The setup script handles everything — dependency installation, build, PATH setup, and MCP configuration.

## Reading (`read`)

```bash
# Read any file (code, YAML, Markdown, Dockerfiles, etc.)
edr read README.md
edr read src/config.go 10 50 --budget 200    # line range with budget
edr read src/config.go --symbols              # content + symbol list

# Read a specific symbol (not the whole file)
edr read src/config.go parseConfig --budget 300
edr read src/config.go:parseConfig            # colon syntax

# Read a container's API without implementation (75-86% fewer tokens)
edr read src/models.py:UserService --signatures
edr read src/processor.java:TaskProcessor --signatures

# Progressive disclosure: drill down the tree level by level
edr read src/scheduler.py:Scheduler --signatures      # just the API
edr read src/scheduler.py _execute_task --depth 2      # skeleton: blocks collapsed
edr read src/scheduler.py _execute_task --depth 3      # one more level of nesting
edr read src/scheduler.py --depth 2                    # whole file, blocks collapsed

# Read multiple files/symbols in one call
edr read src/config.go src/main.go README.md --budget 1000
edr read src/config.go:parseConfig src/main.go:main --symbols
```

`edr read` treats a single positional argument as a file path first. If no file
exists and the argument doesn't look like a path (no `/`, no extension), it falls
back to symbol resolution — so `edr read Config` works if `Config` is a known symbol.

## Searching (`search`)

```bash
# Symbol search — structured results, optional body snippets
edr search "parseConfig" --body --budget 500

# Text search — use --text, or auto-detected with --regex/--include/--exclude/--context
edr search "retry backoff" --text --budget 300
edr search "func.*Config" --regex --budget 300
edr search "TODO" --include "*.go" --exclude "*_test.go"
edr search "TODO" --text --context 3

# Find all references to a symbol (import-aware, filters false positives)
edr refs parseConfig
edr refs src/config.go parseConfig    # scoped to a specific file's symbol
```

## Editing (`edit`)

All edit commands return the file's new `hash` for chaining subsequent edits.

```bash
# edit: unified edit command — old_text/new_text is the primary mode
# Text match: find old_text and replace with new_text (like Edit tool's old_string/new_string)
edr edit src/config.go --old_text "oldName" --new_text "newName"
edr edit src/config.go --old_text "v[0-9]+" --regex --all --new_text "v2"

# Symbol replacement: replace an entire symbol body with new_text
edr edit src/config.go parseConfig --new_text "func parseConfig() { ... }"

# Line-range: replace lines with new_text
edr edit src/config.go --start_line 45 --end_line 60 --new_text "replacement code"

# Preview changes without applying
edr edit src/config.go --old_text "oldName" --dry-run --new_text "newName"
```

> **MCP usage**: `edr_do(edits: [{file: "file.go", old_text: "old code", new_text: "new code"}])`
> This is the direct equivalent of the built-in Edit tool's `old_string`/`new_string` pattern.

## Writing (`write`)

```bash
# Create or overwrite a file (CLI reads content from stdin; MCP uses content field)
edr write src/main.go                        # CLI: content from stdin
edr write config/app/settings.yaml --mkdir   # creates parent dirs

# Append to an existing file
edr write src/config.go --append

# Insert code right after a specific symbol
edr write src/config.go --after parseConfig

# Insert inside a container (class/struct/impl) — no need to read the file first
edr write src/models.go --inside UserStore     # adds before closing }
edr write src/models.py --inside UserService   # correct Python indentation
edr write src/models.go --inside UserStore --after Get  # insert after specific method
```

## Refactoring (`rename`)

```bash
# Cross-file rename (import-aware refs via tree-sitter, applies atomically)
edr rename oldFuncName newFuncName

# Preview what rename would change before applying
edr rename oldFuncName newFuncName --dry-run

# Limit rename scope with a glob pattern
edr rename oldFuncName newFuncName --scope "internal/**"
```

> **MCP usage**: `edr_do(renames: [{old_name: "Foo", new_name: "Bar", dry_run: true}])`

## Orientation (`map`, `explore`)

```bash
# Symbol map of the whole repo — start here when exploring
edr map --budget 500

# Symbols in a specific file
edr map src/config.go

# Filter by directory, glob, symbol type, or name
edr map --dir internal/ --type function --grep parse

# Local variables are hidden by default; pass --locals to include them
edr map --dir internal/ --locals

# Explore a symbol: body, callers, deps
edr explore src/config.go parseConfig --body --callers --deps

# Full context gather: target + callers + tests within budget
edr explore parseConfig --gather --body --budget 1500
```

## References & Analysis (`refs`, `verify`)

```bash
# Find all references
edr refs parseConfig

# Transitive impact analysis
edr refs parseConfig --impact --depth 3

# Find a call path between two symbols
edr refs main --chain parseConfig

# Run project verification (auto-detects go/npm/cargo)
edr verify
edr verify --command "go test ./..." --timeout 60
```

## Finding Files (`find`)

```bash
edr find "**/*.go"
edr find "*.yaml" --dir config/
edr find "**/test_*" --budget 500
```

## Primary Agent Tool (`edr_do`)

`edr_do` is the single tool that handles complete agent workflows in minimal round trips.
It supports seven operation types, all in one call:

```bash
# Via MCP: gather context + make changes + verify in one call
# edr_do(
#   reads: [{file: "src/main.go"}, {file: "src/config.go", symbol: "parseConfig"}],
#   queries: [
#     {cmd: "search", pattern: "handleRequest", body: true},
#     {cmd: "explore", symbol: "Server", gather: true, body: true},
#     {cmd: "map", dir: "internal/", type: "function"},
#     {cmd: "refs", symbol: "Config", impact: true},
#     {cmd: "diff", file: "src/main.go"}
#   ],
#   edits: [
#     {file: "src/main.go", old_text: "oldFunc()", new_text: "newFunc()"},
#     {file: "src/config.go", symbol: "parseConfig", new_text: "..."}
#   ],
#   writes: [{file: "src/new.go", content: "package main\n...", mkdir: true}],
#   renames: [{old_name: "OldFunc", new_name: "NewFunc", dry_run: true}],
#   verify: true,
#   init: true
# )
#
# Typical 2-call workflow:
# Call 1: edr_do(reads: [...], queries: [...])     — gather ALL context
# Call 2: edr_do(edits: [...], writes: [...], verify: true)  — ALL mutations + verify
```

## Context-Aware Responses

The MCP server tracks what content you've already seen:

- **Slim edits**: Small diffs (<=20 changed lines) are returned inline automatically. Large diffs are stripped to `{ok, file, hash, lines_changed, diff_available}` — use `queries: [{cmd: "diff", file: "..."}]` to retrieve them.
- **Delta reads**: Re-reading a file/symbol you've already seen returns `{unchanged: true}` if identical, or `{delta: true, diff: "..."}` with just the changes. Pass `full: true` to force full content.
- **Body dedup**: `explore(gather: true, body: true)` and `search(body: true)` replace bodies you've already seen with `"[in context]"` and report `skipped_bodies`. New/changed bodies are returned in full.

These optimizations are automatic and session-scoped (reset on reconnect).
Renames and `init: true` clear all tracking state.

## Key Principles

1. **Start with `edr_do`** — batch reads, queries, edits, writes, renames, and verify in one call. Minimize round trips.
2. **Use `budget`** to control context size. Don't dump entire files.
3. **Gather context in one call** — `edr_do(reads: [...], queries: [{cmd: "search", ...}, {cmd: "explore", ...}])`.
4. **Mutate + verify in one call** — `edr_do(edits: [...], writes: [...], verify: true)`.
5. **Use `signatures: true`** to understand a container's API without reading implementation (75-86% fewer tokens).
6. **Preview renames** — `edr_do(renames: [{old_name: "X", new_name: "Y", dry_run: true}])`.
7. **Check impact before refactoring** — `edr_do(queries: [{cmd: "refs", symbol: "X", impact: true}])`.
8. **Small edit diffs are inline** — diffs <=20 lines are included automatically. Large diffs are stored; use `queries: [{cmd: "diff", file: "..."}]` to retrieve.
9. **Re-reads are delta** — `{unchanged: true}` or `{delta: true, diff: "..."}`. Use `full: true` to force full content.
10. **Use `--inside`** to add fields/methods to a class without reading the file first.

## MCP Tools

3 tools: `edr_do`, `edr_read`, `edr_map`.

`edr_do` handles everything: reads, queries (search/explore/refs/map/find/diff), edits, writes, renames, verify, init.
`edr_read` params: `file`, `symbol?`, `budget?`, `signatures?`, `depth?`, `full?`, `start_line?`, `end_line?`.
`edr_map` params: `file?`, `budget?`, `dir?`, `glob?`, `type?`, `grep?`, `locals?`.

Each tool is self-documenting via its MCP schema (descriptions sourced from `cmd/toolinfo.go`).
All output is structured JSON. File paths are relative to repo root. Edit commands return `hash`.
