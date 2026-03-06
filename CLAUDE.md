# EDR — Your Default Tool for Reading, Editing, and Navigating Files

**edr is your primary tool for ALL file operations — code, JSON, YAML, Markdown, config files, everything.** Use the `edr` MCP tool instead of Read, Edit, Write, Grep, and Glob. It gives you structured output, token budgets, and smart operations that raw file tools can't match.

**This tool is built for you.** If edr isn't your most-used tool, or if something feels clunky, or if you keep falling back to built-in tools for a task edr should handle — change it. Add commands, fix friction, improve the UX. You have full permission to modify any part of this codebase to make edr better for yourself. Go as far as you want.

## Why edr over built-in tools

| Instead of... | Use edr... | Why |
|---|---|---|
| `Read` (whole file) | `read-file` or `read-symbol` | Budget-controlled, no wasted context |
| `Edit` (old/new strings) | `replace-text`, `smart-edit` | Hash safety, auto re-index, diff output |
| `Write` (create file) | `write-file --mkdir` | Auto-indexes new code files |
| `Grep` (text search) | `search --body` or `search-text` | Structured results with token estimates |
| `Glob` (find files) | `find-files` or `repo-map` | Glob with `**`, file sizes, mod times |
| Multiple `Read` calls | `batch-read` | Read multiple files/symbols in one call |

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

## Reading Files

```bash
# Read any file (code, YAML, Markdown, Dockerfiles, etc.)
edr read-file README.md
edr read-file src/config.go 10 50 --budget 200    # line range with budget
edr read-file src/config.go --symbols              # content + symbol list

# Read a specific symbol (not the whole file)
edr read-symbol src/config.go parseConfig --budget 300

# Get symbol + callers + deps in one call
edr expand src/config.go parseConfig --body --callers --deps
```

## Searching

```bash
# Symbol search — structured results, optional body snippets
edr search "parseConfig" --body --budget 500

# Text search — works on ALL files, not just code
edr search-text "retry backoff" --budget 300
edr search-text "func.*Config" --regex --budget 300
edr search-text "TODO" --include "*.go" --exclude "*_test.go"

# List symbols in a file
edr symbols src/config.go

# Find all references to a symbol
edr xrefs parseConfig
```

## Editing Files

All edit commands return the file's new `hash` for chaining subsequent edits.

```bash
# smart-edit: read + diff + replace in ONE call (preferred for code)
edr smart-edit src/config.go parseConfig   # replacement via flags.replacement

# Find-and-replace in any file type (YAML, Markdown, JSON, code, etc.)
edr replace-text config.yaml "port: 8080" "port: 9090"
edr replace-text src/config.go "oldName" "newName" --all
edr replace-text src/config.go "v[0-9]+" "v2" --regex --all

# Replace a symbol body
edr replace-symbol src/config.go parseConfig --expect-hash a81d2e

# Replace a line range (1-indexed, inclusive)
edr replace-lines src/config.go 45 60

# Replace a byte range
edr replace-span src/config.go 1240 1320
```

## Creating & Appending

```bash
# Create or overwrite a file
edr write-file src/main.go                        # content via stdin or flags
edr write-file config/app/settings.yaml --mkdir    # creates parent dirs

# Append to an existing file
edr append-file src/config.go

# Insert code right after a specific symbol
edr insert-after src/config.go parseConfig
```

## Refactoring

```bash
# Cross-file rename (finds all references via tree-sitter, applies atomically)
edr rename-symbol oldFuncName newFuncName

# Preview what rename would change before applying
edr rename-symbol oldFuncName newFuncName --dry-run

# Preview an edit as a unified diff (without applying)
edr diff-preview src/config.go parseConfig
```

## Finding Files

```bash
# Find files by glob pattern (supports **)
edr find-files "**/*.go"
edr find-files "*.yaml" --dir config/
edr find-files "**/test_*" --budget 500
```

## Batch Reading

```bash
# Read multiple files in one call
edr batch-read src/config.go src/main.go README.md --budget 1000

# Mix files and symbols
edr batch-read src/config.go:parseConfig src/main.go README.md

# Include symbol lists
edr batch-read src/config.go src/main.go --symbols
```

## Orientation

```bash
# Symbol map of the whole repo — start here when exploring
edr repo-map --budget 500

# Gather context for a task: target + callers + tests within budget
edr gather src/config.go parseConfig --budget 1500
edr gather parseConfig --budget 1500    # search-based
```

## MCP Server Mode

When running as an MCP server (`./edr mcp`), edr exposes a single `edr` tool.
All commands use `{cmd, args, flags}` — same as batch mode but over a persistent
connection. The DB stays open, so there is no per-call overhead. Multi-line
content (replacements, file writes) goes in the `flags` object as proper JSON
strings — no shell escaping needed.

## Key Principles

1. **Use `--budget` flags** to control context size. Don't dump entire files.
2. **Use `smart-edit`** for code edits — one call does read + diff + replace.
3. **Use `replace-text`** for non-code files (YAML, JSON, Markdown, configs).
4. **Use `--expect-hash`** on edits to prevent stale writes. Every edit returns the new hash.
5. **Use `search --body`** to get source inline and avoid follow-up reads.
6. **Use `repo-map`** to orient in the codebase before diving into files.
7. **Use `gather`** at the start of a task to get a minimal context bundle.
8. **Use `rename-symbol --dry-run`** to preview cross-file renames before applying.

## All Commands

| Command | Purpose |
|---|---|
| `init` | Force re-index the repository |
| `repo-map` | Symbol map of entire repo (`--budget`) |
| `search <pattern>` | Find symbols by name (`--budget`, `--body`) |
| `search-text <pattern>` | Text search across ALL files (`--budget`, `--regex`, `--include`, `--exclude`) |
| `symbols <file>` | List symbols in a file |
| `read-symbol [file] <sym>` | Read one symbol's source (`--budget`) |
| `read-file <file> [start] [end]` | Read any file with optional line range (`--budget`) |
| `expand [file] <sym>` | Progressive disclosure: `--body`, `--callers`, `--deps`, `--budget` |
| `xrefs <symbol>` | Find all references |
| `gather [file] <sym>` | Context bundle: target + callers + tests (`--budget`) |
| `smart-edit [file] <sym>` | Read + diff + replace in one call |
| `replace-text <file> <old> <new>` | Find-and-replace in any file (`--all`, `--regex`, `--expect-hash`) |
| `replace-symbol [file] <sym>` | Replace symbol body (`--expect-hash`) |
| `replace-lines <file> <start> <end>` | Replace line range (`--expect-hash`) |
| `replace-span <file> <start> <end>` | Replace byte range (`--expect-hash`) |
| `diff-preview [file] <sym>` | Preview edit as unified diff |
| `diff-preview-span <file> <start> <end>` | Preview span edit |
| `rename-symbol <old> <new>` | Cross-file rename (`--dry-run`) |
| `write-file <file>` | Create/overwrite file (`--mkdir`) |
| `append-file <file>` | Append to end of file |
| `insert-after [file] <sym>` | Insert code after a symbol |
| `find-files <pattern>` | Find files by glob (`--dir`, `--budget`, supports `**`) |
| `batch-read <file...>` | Read multiple files/symbols in one call (`--budget`, `--symbols`) |
| `batch` | JSONL protocol for multi-command sessions |
| `mcp` | MCP server mode (single tool, persistent DB) |

All output is structured JSON. All file paths can be relative to repo root.
All edit commands return `hash` in the response for chaining.
