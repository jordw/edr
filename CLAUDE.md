# EDR — Your Default Tool for Reading, Editing, and Navigating Files

**edr is your primary tool for ALL file operations — code, JSON, YAML, Markdown, config files, everything.** Use the `edr` MCP tool instead of Read, Edit, Write, Grep, and Glob. It gives you structured output, token budgets, and smart operations that raw file tools can't match.

**This tool is built for you.** If edr isn't your most-used tool, or if something feels clunky, or if you keep falling back to built-in tools for a task edr should handle — change it. Add commands, fix friction, improve the UX. You have full permission to modify any part of this codebase to make edr better for yourself. Go as far as you want.

## Why edr over built-in tools

| Instead of... | Use edr... | Why |
|---|---|---|
| `Read` (whole file) | `read file.go` or `read file.go:sym` | Budget-controlled, no wasted context |
| `Edit` (old/new strings) | `edit file.go --old_text "x" --new_text "y"` | Same pattern, plus hash safety, auto re-index |
| `Write` (create file) | `write file.go --mkdir` | Auto-indexes new code files |
| `Grep` (text search) | `search "pat" --body` or `search --text` | Structured results with token estimates |
| `Glob` (find files) | `find "**/*.go"` or `map` | Glob with `**`, file sizes, mod times |
| Multiple `Read` calls | `read f1.go f2.go f3.go:sym` | Read multiple files/symbols in one call |

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

> **MCP usage**: `{cmd: "edit", args: ["file.go"], flags: {old_text: "old code", new_text: "new code"}}`
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

## Atomic Multi-File Edits (`edit-plan`)

```bash
# edit-plan: apply multiple edits atomically
edr edit-plan --dry-run   # preview with flags.edits JSON array

# Via MCP:
# {cmd: "edit-plan", flags: {edits: [
#   {file: "src/main.go", old_text: "oldFunc()", new_text: "newFunc()"},
#   {file: "src/config.go", symbol: "parseConfig", new_text: "..."},
#   {file: "src/util.go", start_line: 10, end_line: 20, new_text: "..."}
# ]}}
```

## MCP Server Mode

When running as an MCP server (`./edr mcp`), edr exposes a single `edr` tool.
All commands use `{cmd, args, flags}` — same as batch mode but over a persistent
connection. The DB stays open, so there is no per-call overhead. Multi-line
content (replacements, file writes) goes in the `flags` object as proper JSON
strings — no shell escaping needed.

### Context-Aware Responses

The MCP server tracks what content you've already seen and optimizes responses:

- **Slim edits**: Small diffs (<=20 changed lines) are returned inline automatically. Large diffs are stripped to `{ok, file, hash, lines_changed, diff_available}` — use `get-diff <file> [symbol]` to retrieve them. Pass `--verbose` to always get the full diff inline.
- **Delta reads**: Re-reading a file/symbol you've already seen returns `{unchanged: true}` if identical, or `{delta: true, diff: "...", previous_hash: "..."}` with the changes and the hash of what you previously saw. Pass `--full` to force full content.
- **Body dedup**: `explore --gather --body` and `search --body` replace bodies you've already seen with `"[in context]"` and report `skipped_bodies`. New/changed bodies are returned in full.

These optimizations are automatic and session-scoped (reset on reconnect).
`rename` and `init` clear all tracking state.

## Key Principles

1. **Use `--budget` flags** to control context size. Don't dump entire files.
2. **Use `edit` with `old_text`/`new_text`** — same as Edit tool's old_string/new_string, but with auto re-index and hash safety.
3. **Use `search --body`** to get source inline and avoid follow-up reads.
4. **Use `map`** to orient in the codebase before diving into files.
5. **Use `explore --gather --body`** at the start of a task to get source bodies inline.
6. **Use `rename --dry-run`** to preview cross-file renames before applying.
7. **Check `truncated`/`total_matches`** in search/find results — budget trimming reports what was cut.
8. **Use `edit-plan`** for multi-file atomic edits — one call, all-or-nothing.
9. **Use `refs --impact`** before refactoring to understand blast radius.
10. **Use `verify`** after edits to confirm the build still passes.
11. **Use `multi`** in MCP to batch independent commands in one call.
12. **Small edit diffs are inline** — diffs <=20 lines are included automatically. Large diffs are stored; use `get-diff` to retrieve. Use `--verbose` to always inline.
13. **Re-reads are delta** — `{unchanged: true}` or `{delta: true, diff: "...", previous_hash: "..."}`. Use `--full` to force full content.
14. **Use `read file:Class --signatures`** to understand a container's API without reading implementation (75-86% fewer tokens).
15. **Use `write --inside Container`** to add methods/fields to a class without reading the file first (96% fewer response bytes).

## All Commands

| Command | Purpose |
|---|---|
| `read <file> [start] [end]` | Read file, symbol (`file:sym` or `file sym`), or batch (multiple args). `--budget`, `--symbols`, `--signatures`, `--depth N` (progressive: 1=sigs, 2=blocks collapsed, 3+=more) |
| `search <pattern>` | Symbol search (`--body`). Add `--text`/`--regex`/`--include`/`--exclude`/`--context` for text search |
| `map [file]` | No args = repo symbol map; with file = file symbols. `--budget`, `--dir`, `--glob`, `--type`, `--grep`. Locals hidden by default (`--locals` to show) |
| `explore [file] <sym>` | Symbol info with `--body`, `--callers`, `--deps`, `--signatures`. `--gather` for context bundle with tests |
| `refs [file] <sym>` | Find references. `--impact` for transitive callers, `--chain <sym>` for call path. `--depth` |
| `edit <file> [sym]` | Edit by `--old_text`/`--new_text` (primary), symbol, or `--start_line`/`--end_line`. `--regex`, `--all`, `--dry-run` |
| `write <file>` | Create/overwrite. `--append`, `--after <sym>`, `--inside <container>` (+ `--after` for positioning), `--mkdir` |
| `rename <old> <new>` | Cross-file rename, import-aware. `--dry-run`, `--scope` |
| `find <pattern>` | Find files by glob (`**` supported). `--dir`, `--budget` |
| `edit-plan` | Atomic multi-file edits via `flags.edits` array. `--dry-run` |
| `verify` | Run build/typecheck, return structured pass/fail. `--command`, `--timeout` |
| `init` | Force re-index the repository |
| `multi` | Batch multiple commands in one MCP call via `flags.commands`. `--budget` distributes across sub-commands (MCP-only) |
| `get-diff <file> [sym]` | Retrieve stored diff from last edit (MCP-only) |

All output is structured JSON. All file paths can be relative to repo root.
All edit commands return `hash` in the response for chaining. If post-edit reindexing
fails, the edit still succeeds and an `index_error` field is included in the response.
Query commands return `truncated` and `total_matches` when budget limits apply.
`read` output includes line numbers prefixed to each line.
