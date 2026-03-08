# EDR — Your Default Tool for Reading, Editing, and Navigating Files

**edr is your primary tool for ALL file operations.** Use the `edr_*` MCP tools instead of Read, Edit, Write, Grep, and Glob. They give you structured output, token budgets, and smart operations that raw file tools can't match.

**`edr_plan` is your most powerful tool.** For any task involving multiple operations, prefer `edr_plan` over individual tools — it batches reads, searches, explores, edits, writes, and verification into one call.

```
# Typical 2-call workflow for any task:
edr_plan(                                          # Call 1: gather ALL context
  reads: [{file: "src/main.go", symbol: "Server"}],
  queries: [
    {cmd: "search", pattern: "handleRequest", body: true},
    {cmd: "map", dir: "internal/", type: "function"}
  ]
)
edr_plan(                                          # Call 2: ALL mutations + verify
  edits: [{file: "src/main.go", old_text: "old", new_text: "new"}],
  writes: [{file: "src/new_test.go", content: "...", mkdir: true}],
  verify: true
)
```

**Only fall back to individual tools when:**
- You need a single quick read or edit (use `edr_read`, `edr_edit`)
- You need `edr_rename` (cross-file, import-aware)
- You need non-text files or shell operations (use built-in tools)

## Why edr over built-in tools

| Instead of... | Use edr... | Why |
|---|---|---|
| `Read` (whole file) | `edr_plan(reads: [{file: "f.go"}])` | Budget-controlled, batchable |
| `Edit` (old/new strings) | `edr_plan(edits: [{file: "f.go", old_text: "x", new_text: "y"}])` | Atomic multi-file, auto re-index |
| `Write` (create file) | `edr_plan(writes: [{file: "f.go", content: "...", mkdir: true}])` | Auto-indexes, batchable with edits |
| `Grep` (text search) | `edr_plan(queries: [{cmd: "search", pattern: "pat", body: true}])` | Structured results, batchable |
| `Glob` (find files) | `edr_plan(queries: [{cmd: "find", pattern: "**/*.go"}])` | Glob with `**`, batchable |
| Multiple tool calls | One `edr_plan(reads + queries + edits + writes, verify: true)` | Everything in 1-2 calls |

For single operations, shorthand tools also work: `edr_read`, `edr_edit`, `edr_write`, `edr_search`, `edr_find`.

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

> **MCP usage**: `edr_edit(file: "file.go", old_text: "old code", new_text: "new code")`
> This is the direct equivalent of the built-in Edit tool's `old_string`/`new_string` pattern.

## Writing (`write`)

```bash
# Create or overwrite a file (CLI reads content from stdin; MCP uses content/new_text flag)
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

## Orientation (`map`, `explore`)

```bash
# Symbol map of the whole repo — start here when exploring
edr map --budget 500

# Symbols in a specific file
edr map src/config.go

# Filter by directory, glob, symbol type, or name
edr map --dir internal/ --type function --grep parse

# MCP supports a `locals` parameter on `edr_map`, but the current CLI does not
# expose a `--locals` flag. File maps may still include some local variables.

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

## Unified Agent Tool (`edr_plan`)

`edr_plan` is the single tool that handles complete agent workflows in minimal round trips.
It supports five operation types, all in one call:

```bash
# Via MCP: gather context + make changes + verify in one call
# edr_plan(
#   reads: [{file: "src/main.go"}, {file: "src/config.go", symbol: "parseConfig"}],
#   queries: [
#     {cmd: "search", pattern: "handleRequest", body: true},
#     {cmd: "explore", symbol: "Server", gather: true, body: true},
#     {cmd: "map", dir: "internal/", type: "function"},
#     {cmd: "refs", symbol: "Config", impact: true}
#   ],
#   edits: [
#     {file: "src/main.go", old_text: "oldFunc()", new_text: "newFunc()"},
#     {file: "src/config.go", symbol: "parseConfig", new_text: "..."}
#   ],
#   writes: [{file: "src/new.go", content: "package main\n...", mkdir: true}],
#   verify: true
# )
#
# Typical 2-call workflow:
# Call 1: edr_plan(reads: [...], queries: [...])     — gather ALL context
# Call 2: edr_plan(edits: [...], writes: [...], verify: true)  — ALL mutations + verify
```

## MCP Server Mode

When running as an MCP server (`./edr mcp`), edr exposes 13 dedicated typed tools
(`edr_read`, `edr_edit`, `edr_write`, etc.). Each tool has typed parameters —
no need to construct `{cmd, args, flags}` objects. The DB stays open across calls,
so there is no per-call overhead.

### Context-Aware Responses

The MCP server tracks what content you've already seen and optimizes responses:

- **Slim edits**: Small diffs (<=20 changed lines) are returned inline automatically. Large diffs are stripped to `{ok, file, hash, lines_changed, diff_available}` — use `edr_diff` to retrieve them.
- **Delta reads**: Re-reading a file/symbol you've already seen returns `{unchanged: true}` if identical, or `{delta: true, diff: "..."}` with just the changes. Pass `full: true` to force full content.
- **Body dedup**: `edr_explore(gather: true, body: true)` and `edr_search(body: true)` replace bodies you've already seen with `"[in context]"` and report `skipped_bodies`. New/changed bodies are returned in full.

These optimizations are automatic and session-scoped (reset on reconnect).
`edr_rename` and `edr_init` clear all tracking state.

## Key Principles

1. **Start with `edr_plan`** — batch reads, queries, edits, writes, and verify in one call. Minimize round trips.
2. **Use `budget`** to control context size. Don't dump entire files.
3. **Gather context in one call** — `edr_plan(reads: [...], queries: [{cmd: "search", ...}, {cmd: "explore", ...}])`.
4. **Mutate + verify in one call** — `edr_plan(edits: [...], writes: [...], verify: true)`.
5. **Use `signatures: true`** to understand a container's API without reading implementation (75-86% fewer tokens).
6. **Use `edr_rename(dry_run: true)`** to preview cross-file renames before applying.
7. **Use `edr_refs(impact: true)`** before refactoring to understand blast radius.
8. **Small edit diffs are inline** — diffs <=20 lines are included automatically. Large diffs are stored; use `edr_diff` to retrieve.
9. **Re-reads are delta** — `{unchanged: true}` or `{delta: true, diff: "..."}`. Use `full: true` to force full content.
10. **Use `--inside`** to add fields/methods to a class without reading the file first.

## All MCP Tools

| Tool | Purpose |
|---|---|
| **`edr_plan`** | **Primary tool. Use for multi-step tasks.** `reads`, `queries` [{cmd: search/explore/refs/map/find, ...}], `edits`, `writes`, `verify`. |
| `edr_read` | Single read shorthand. `files` (supports `file:sym`), `budget`, `signatures`, `depth` |
| `edr_edit` | Single edit shorthand. `old_text`/`new_text`, `symbol`, `start_line`/`end_line`, `dry_run` |
| `edr_write` | Single write shorthand. `content`, `append`, `after`, `inside`, `mkdir` |
| `edr_search` | Single search shorthand. `pattern`, `body`, `text`/`regex`, `include`/`exclude` |
| `edr_map` | Repo/file symbol map. `budget`, `dir`, `glob`, `type`, `grep` |
| `edr_explore` | Symbol context. `body`, `callers`, `deps`, `gather`, `signatures` |
| `edr_refs` | References. `impact`, `chain`, `depth` |
| `edr_find` | Find files by glob. `dir`, `budget` |
| `edr_rename` | Cross-file rename, import-aware. `dry_run`, `scope` |
| `edr_verify` | Build check. `command`, `timeout` |
| `edr_init` | Force re-index |
| `edr_diff` | Retrieve stored diff from last large edit |

All output is structured JSON. All file paths are relative to repo root.
`edr_read` output includes line numbers. Edit commands return `hash` for chaining.
