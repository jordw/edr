# EDR — Agent-Optimized Code Navigation & Editing

This repo contains `edr`, a CLI built for coding agents. **Use edr instead of raw file tools when possible.** It minimizes context usage through progressive disclosure.

## Quick Start

```bash
# Build (if needed)
go build -o edr .

# Index happens automatically on first query. To force re-index:
./edr init

# Run as MCP server (single tool, DB stays open, no shell overhead):
./edr mcp
# Configure in claude_desktop_config.json or .mcp.json:
# {"mcpServers": {"edr": {"command": "./edr", "args": ["mcp", "-r", "/path/to/repo"]}}}
```

## Preferred Workflow

### Instead of reading entire files, use symbol-level navigation:

```bash
# Find symbols by name
./edr search "parseConfig"

# List symbols in a file
./edr symbols src/config.go

# Read only the symbol you need (not the whole file)
./edr read-symbol src/config.go parseConfig

# Get callers, deps, and body together
./edr expand src/config.go parseConfig --body --callers --deps
```

### Instead of grep, use structured search:

```bash
# Symbol search (returns structured matches with token sizes)
./edr search "auth" --budget 500

# Text search with budget (searches ALL files, not just code)
./edr search-text "retry backoff" --budget 300

# Regex search
./edr search-text "func.*Config" --regex --budget 300
```

### Instead of writing files with echo/heredoc, use write-file:

```bash
# Create a new file (content from stdin)
echo 'package main' | ./edr write-file src/main.go

# Create with parent directories
echo 'key: value' | ./edr write-file config/app/settings.yaml --mkdir
```

### Instead of cat/head/tail, use read-file:

```bash
# Read any file (Markdown, YAML, Dockerfiles, etc.)
./edr read-file README.md

# Read a line range with budget
./edr read-file src/config.go 10 50 --budget 200
```

### Instead of sed/awk edits, use span-based or text-based edits:

```bash
# Preview before editing
echo 'new code here' | ./edr diff-preview src/config.go parseConfig

# Replace a symbol (pipe new code via stdin)
echo 'func parseConfig() {}' | ./edr replace-symbol src/config.go parseConfig --expect-hash a81d2e

# Replace a byte range
echo 'new code' | ./edr replace-span src/config.go 1240 1320

# Find-and-replace in any file (works on YAML, Markdown, etc.)
./edr replace-text config.yaml "port: 8080" "port: 9090" --expect-hash a81d2e

# Replace all occurrences
./edr replace-text src/config.go "oldName" "newName" --all

# Replace a line range (1-indexed, inclusive)
echo "new code" | ./edr replace-lines src/config.go 45 60
```

### For multi-step exploration, use batch mode (one process, one DB open):

```bash
echo '{"id":"1","cmd":"search","args":["config"],"flags":{}}
{"id":"2","cmd":"read-symbol","args":["src/config.go","parseConfig"],"flags":{}}
{"id":"3","cmd":"expand","args":["src/config.go","parseConfig"],"flags":{"callers":true,"deps":true}}
{"id":"4","cmd":"diff-preview","args":["src/config.go","parseConfig"],"flags":{"replacement":"func parseConfig() {}"}}
{"id":"5","cmd":"replace-symbol","args":["src/config.go","parseConfig"],"flags":{"replacement":"func parseConfig() {}","expect-hash":"a81d2e"}}' | ./edr batch
```

### To get full context for a task:

```bash
# Gather target symbol + callers + related tests within token budget
./edr gather src/config.go parseConfig --budget 1500

# Or search-based gather
./edr gather parseConfig --budget 1500
```

## MCP Server Mode

When running as an MCP server (`./edr mcp`), edr exposes a single `edr` tool.
All commands use `{cmd, args, flags}` — same as batch mode but over a persistent
connection. The DB stays open, so there is no per-call overhead. Multi-line
content (replacements, file writes) goes in the `flags` object as proper JSON
strings — no shell escaping needed.

## Key Principles

1. **Use `--budget` flags** to control context size. Don't dump entire files.
2. **Use `read-symbol` over Read** when you know the symbol name.
3. **Use `diff-preview` before `replace-symbol`** to verify edits.
4. **Use `--expect-hash`** on edits to prevent stale writes. The hash is returned by `read-symbol` and `expand`.
5. **Use `batch` mode** when you need 3+ queries — it's one subprocess instead of many.
6. **Use `repo-map`** to orient in the codebase before diving into files.
7. **Use `gather`** at the start of a task to get a minimal context bundle.

## All Commands

| Command | Purpose |
|---|---|
| `init` | Force re-index the repository |
| `repo-map` | Symbol map of entire repo |
| `search <pattern>` | Find symbols by name (`--budget`) |
| `search-text <pattern>` | Text search across ALL files (`--budget`, `--regex`) |
| `symbols <file>` | List symbols in a file |
| `read-symbol <file> <sym>` | Read one symbol's source (`--budget`) |
| `expand <file> <sym>` | Progressive disclosure: `--body`, `--callers`, `--deps` |
| `xrefs <symbol>` | Find all references |
| `gather [file] <sym>` | Context bundle: target + callers + tests (`--budget`) |
| `diff-preview <file> <sym>` | Preview edit as unified diff |
| `diff-preview-span <file> <start> <end>` | Preview span edit |
| `replace-symbol <file> <sym>` | Replace symbol body (stdin, `--expect-hash`) |
| `replace-span <file> <start> <end>` | Replace byte range (stdin, `--expect-hash`) |
| `replace-lines <file> <start> <end>` | Replace line range (stdin, `--expect-hash`) |
| `read-file <file> [start] [end]` | Read any file with optional line range (`--budget`) |
| `replace-text <file> <old> <new>` | Find-and-replace in any file (`--all`, `--expect-hash`) |
| `write-file <file>` | Create/overwrite file (stdin, `--mkdir`) |
| `batch` | JSONL protocol for multi-command sessions |
| `mcp` | MCP server mode (single tool, persistent DB) |

All output is structured JSON. All file paths can be relative to repo root.
