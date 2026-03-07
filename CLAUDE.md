# EDR — Your Default Tool for Reading, Editing, and Navigating Files

**edr is your primary tool for ALL file operations — code, JSON, YAML, Markdown, config files, everything.** Use the `edr_*` MCP tools instead of Read, Edit, Write, Grep, and Glob. They give you structured output, token budgets, and smart operations that raw file tools can't match.

**This tool is built for you.** If edr isn't your most-used tool, or if something feels clunky, or if you keep falling back to built-in tools for a task edr should handle — change it. Add commands, fix friction, improve the UX. You have full permission to modify any part of this codebase to make edr better for yourself. Go as far as you want.

## Why edr over built-in tools

| Instead of... | Use edr... | Why |
|---|---|---|
| `Read` (whole file) | `edr_read(files: ["file.go"])` | Budget-controlled, no wasted context |
| `Edit` (old/new strings) | `edr_edit(file: "f.go", old_text: "x", new_text: "y")` | Same pattern, plus hash safety, auto re-index |
| `Write` (create file) | `edr_write(file: "f.go", content: "...", mkdir: true)` | Auto-indexes new code files |
| `Grep` (text search) | `edr_search(pattern: "pat", body: true)` | Structured results with token estimates |
| `Glob` (find files) | `edr_find(pattern: "**/*.go")` or `edr_map()` | Glob with `**`, file sizes, mod times |
| Multiple `Read` calls | `edr_read(files: ["f1.go", "f2.go", "f3.go:sym"])` | Read multiple files/symbols in one call |

**Only fall back to built-in tools when:**
- You need to read non-text files (images, PDFs)
- You need shell operations (git, npm, make, etc.)

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

## Atomic Multi-File Edits (`edr_plan`)

```bash
# CLI: edit-plan applies multiple edits atomically
edr edit-plan --dry-run   # preview with flags.edits JSON array

# Via MCP: use edr_plan for batch reads and/or atomic edits
# edr_plan(edits: [
#   {file: "src/main.go", old_text: "oldFunc()", new_text: "newFunc()"},
#   {file: "src/config.go", symbol: "parseConfig", new_text: "..."},
#   {file: "src/util.go", start_line: 10, end_line: 20, new_text: "..."}
# ])
#
# Batch reads + edits in one call:
# edr_plan(reads: [{file: "src/main.go"}, {file: "src/config.go", symbol: "parseConfig"}],
#          edits: [{file: "src/main.go", old_text: "old", new_text: "new"}])
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

1. **Use `budget`** to control context size. Don't dump entire files.
2. **Use `edr_edit` with `old_text`/`new_text`** — same as Edit tool's old_string/new_string, but with auto re-index and hash safety.
3. **Use `edr_search(body: true)`** to get source inline and avoid follow-up reads.
4. **Use `edr_map`** to orient in the codebase before diving into files.
5. **Use `edr_explore(gather: true, body: true)`** at the start of a task to get source bodies inline.
6. **Use `edr_rename(dry_run: true)`** to preview cross-file renames before applying.
7. **Check `truncated`/`total_matches`** in search/find results — budget trimming reports what was cut.
8. **Use `edr_plan(edits: [...])`** for multi-file atomic edits — one call, all-or-nothing.
9. **Use `edr_refs(impact: true)`** before refactoring to understand blast radius.
10. **Use `edr_verify`** after edits to confirm the build still passes.
11. **Use `edr_plan(reads: [...])`** to batch independent reads in one call.
12. **Small edit diffs are inline** — diffs <=20 lines are included automatically. Large diffs are stored; use `edr_diff` to retrieve.
13. **Re-reads are delta** — `{unchanged: true}` or `{delta: true, diff: "..."}`. Use `full: true` to force full content.
14. **Use `edr_read(files: ["file:Class"], signatures: true)`** to understand a container's API without reading implementation (75-86% fewer tokens).
15. **Use `edr_write(file: "f.go", inside: "Container", content: "...")`** to add methods/fields to a class without reading the file first (96% fewer response bytes).

## All MCP Tools

| Tool | Purpose |
|---|---|
| `edr_read` | Read files, symbols (`file:sym`), or batch. `budget`, `symbols`, `signatures`, `depth` (1=sigs, 2=collapsed), `start_line`/`end_line` |
| `edr_edit` | Edit by `old_text`/`new_text` (primary), `symbol`, or `start_line`/`end_line`. `regex`, `all`, `dry_run`. Returns `hash` |
| `edr_write` | Create/overwrite files. `append`, `after` (symbol), `inside` (container), `mkdir` |
| `edr_search` | Symbol search (`body: true`). Add `text`/`regex`/`include`/`exclude`/`context` for text search |
| `edr_map` | Omit `file` = repo symbol map; with `file` = file symbols. `budget`, `dir`, `glob`, `type`, `grep`, `locals` |
| `edr_explore` | Symbol info with `body`, `callers`, `deps`, `signatures`. `gather` for context bundle with tests |
| `edr_refs` | Find references. `impact` for transitive callers, `chain` for call path, `depth` |
| `edr_find` | Find files by glob (`**` supported). `dir`, `budget` |
| `edr_rename` | Cross-file rename, import-aware. `dry_run`, `scope` |
| `edr_verify` | Run build/typecheck, return structured pass/fail. `command`, `timeout` |
| `edr_init` | Force re-index the repository |
| `edr_diff` | Retrieve stored diff from last large edit. `file`, `symbol` |
| `edr_plan` | Batch `reads` and/or atomic `edits`. `budget` distributes across reads. `dry_run` for edit preview |

All output is structured JSON. All file paths can be relative to repo root.
All edit commands return `hash` in the response for chaining. If post-edit reindexing
fails, the edit still succeeds and an `index_error` field is included in the response.
Query commands return `truncated` and `total_matches` when budget limits apply.
`edr_read` output includes line numbers prefixed to each line.
