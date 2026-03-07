# edr



`edr` is a Go CLI for code navigation and editing aimed at coding agents. It builds a local symbol index for a repository, exposes structured JSON output, and supports three usage modes:

- CLI commands for one-off reads and edits
- `batch` mode for multiple JSONL requests over one process
- `mcp` mode for long-lived Model Context Protocol integrations

The tool is self-contained. It uses a local SQLite database under `.edr/` and does not depend on any external service. It respects `.gitignore` for file walking and supports concurrent access via WAL mode with a two-layer writer lock (in-process mutex + cross-process flock on `.edr/writer.lock`).

## Requirements

- Go `1.25.0` as specified in `go.mod`
- A C compiler for tree-sitter grammar builds
- Write access to the target repository so `edr` can create `.edr/index.db`

## Build, Vet, Test

```bash
go build -o edr .
go vet ./...
go test ./...
```

## Quick Start

Build and index the current repository:

```bash
go build -o edr .
./edr init
```

Inspect the repo:

```bash
./edr map --budget 500
./edr search openAndEnsureIndex --body --budget 300
./edr read cmd/root.go:openAndEnsureIndex --budget 250
./edr explore internal/edit/edit.go ReplaceSpan --gather --body --budget 700
```

Run as an MCP server:

```bash
./edr mcp
```

Install it for another repository with the bootstrap script:

```bash
./setup.sh /path/to/target/repo
```

`setup.sh` builds the binary, installs it to `~/.local/bin/edr`, writes `.mcp.json` in the target repo, and performs the initial index.

## Commands

edr has 14 primary commands.

### Reading & Navigation

```bash
./edr read setup.sh 1 60 --budget 300            # file with line range
./edr read cmd/root.go openAndEnsureIndex         # read a symbol
./edr read cmd/root.go:openAndEnsureIndex         # colon syntax
./edr read cmd/root.go internal/output/output.go  # batch read multiple files

# Progressive disclosure — read a file or symbol as a tree
./edr read src/models.py:UserService --signatures   # container API: method stubs, no bodies
./edr read src/scheduler.py _execute_task --depth 2  # method skeleton: control flow collapsed
./edr read src/scheduler.py _execute_task --depth 3  # expand one more nesting level
./edr read src/scheduler.py --depth 2                # whole file with blocks collapsed

./edr map --budget 500                            # repo-wide symbol map
./edr map internal/edit/edit.go                   # symbols in one file
./edr map --dir internal/ --type function         # filtered repo map
./edr search Replace --budget 200                 # symbol search
./edr search "TODO|FIXME" --regex --budget 200    # text search (auto-detected)
./edr search "func Open" --text --context 3       # text search with context
./edr find "**/*.go" --budget 300                 # find files by glob
```

### Exploring & References

```bash
./edr explore cmd/root.go openAndEnsureIndex --body --callers --deps --signatures
./edr explore openAndEnsureIndex --gather --body --budget 700    # context bundle
./edr refs openAndEnsureIndex                     # cross-references
./edr refs cmd/root.go openAndEnsureIndex --impact --depth 3    # transitive callers
./edr refs Dispatch --chain editOK                # find call path A→B
```

### Editing

```bash
./edr edit cmd/root.go openAndEnsureIndex         # replace symbol (stdin or --new_text)
./edr edit cmd/root.go --start_line 31 --end_line 62   # replace line range
./edr edit src/main.go --old_text "v[0-9]+" --regex --all --new_text "v2"  # find-and-replace
./edr edit src/main.go --old_text "old" --new_text "new" --dry-run       # preview without applying
./edr rename oldName newName --dry-run            # preview cross-file rename
./edr rename oldName newName --scope "internal/**"
```

### Writing

```bash
./edr write src/main.go                           # create/overwrite (stdin)
./edr write config/settings.yaml --mkdir          # create parent dirs
./edr write src/config.go --append                # append to file
./edr write src/config.go --after parseConfig     # insert after symbol

# Structural insertion — add code inside a container without reading the file
./edr write src/models.go --inside UserStore        # insert before closing }
./edr write src/models.py --inside UserService      # correct Python indentation
./edr write src/models.go --inside UserStore --after Get  # position after a method
```

### Analysis

```bash
./edr verify                                      # auto-detect build check
./edr verify --command "go test ./..." --timeout 60
```

### Atomic Multi-File Edits

```json
{"cmd": "edit-plan", "flags": {"edits": [
  {"file": "src/config.go", "symbol": "parseConfig", "replacement": "..."},
  {"file": "src/main.go", "old_text": "oldFunc()", "new_text": "newFunc()"},
  {"file": "src/util.go", "start_line": 10, "end_line": 20, "replacement": "..."}
], "dry-run": true}}
```

Commands that modify file content read replacement content from stdin unless a command-specific flag is provided.

Query commands (`search`, `find`, `read`) include `truncated` and `total_matches` metadata when budget limits apply, so agents always know if results were cut short.

## Batch Mode

`batch` keeps the database open across multiple requests and reads one JSON object per line from stdin:

```bash
printf '%s\n' \
  '{"id":"1","cmd":"map","args":[],"flags":{"budget":250}}' \
  '{"id":"2","cmd":"map","args":["cmd/root.go"],"flags":{}}' \
  | ./edr batch
```

This mode is a good fit for agents and scripts that want multiple sequential operations without reopening the database for each command.

## MCP Mode

`mcp` starts a JSON-RPC server over stdio and exposes a single `edr` tool. The tool accepts:

- `cmd`: command name
- `args`: positional arguments
- `flags`: command flags as JSON values

Example request payload:

```json
{
  "cmd": "search",
  "args": ["openAndEnsureIndex"],
  "flags": {
    "body": true,
    "budget": 200
  }
}
```

`multi` and `get-diff` are MCP-only commands (not available in CLI or batch mode).

Batch multiple commands in one MCP call with `cmd: "multi"`:

```json
{
  "cmd": "multi",
  "flags": {
    "commands": [
      {"cmd": "read", "args": ["cmd/root.go:openAndEnsureIndex"]},
      {"cmd": "map", "args": ["internal/edit/edit.go"]}
    ]
  }
}
```

The MCP server tracks what content the LLM has already seen and shapes responses accordingly:

- **Response dedup**: identical read results return `{"cached": true}`
- **Slim edits**: `edit` strips the diff from the response (returns `lines_changed` + `diff_available` instead). Use `get-diff <file> [symbol]` to retrieve it, or pass `--verbose` for inline diffs.
- **Delta reads**: re-reading a file/symbol returns `{unchanged: true}` if identical, or `{delta: true, diff: "..."}` with just the changes. Pass `--full` for full content.
- **Body dedup**: `explore --gather --body` and `search --body` replace previously-seen bodies with `"[in context]"` and report `skipped_bodies`.

All tracking is session-scoped and resets on reconnect. `rename` and `init` clear all state. Edit commands invalidate the cache for affected files.

## Repository Layout

- `main.go`: entrypoint
- `cmd/`: Cobra CLI commands, batch mode, MCP server
- `internal/index/`: indexing, symbol resolution, SQLite storage
- `internal/search/`: symbol and text search
- `internal/edit/`: file edits, transactions, diffing
- `internal/dispatch/`: shared command dispatch for batch and MCP
- `internal/gather/`: task-oriented context collection

## Benchmarks

edr vs built-in agent tools (Read, Edit, Grep, Glob) across 9 real workflows on a mixed-language test corpus (Go, Python, Java, Ruby, TypeScript, Rust, C — 934 lines total). "Native" bytes are simulated Read/Grep output sizes; edr bytes are actual structured JSON responses. Median of 5 runs on Apple M3 Pro.

| Scenario | Native tools | edr | Savings |
|---|---|---|---|
| Understand class API | 13,894B × 1 call | 1,137B × 1 call | **91%** |
| Read a specific symbol | 13,894B × 1 call | 2,155B × 1 call | **84%** |
| Search with context | 12,869B × 1 call | 4,377B × 1 call | **65%** |
| Orient in codebase | 36,457B × 5 calls | 2,125B × 1 call | **94%** |
| Edit a function | 27,988B × 3 calls | 589B × 1 call | **97%** |
| Add method to class | 14,094B × 2 calls | 125B × 1 call | **99%** |
| Multi-file read | 30,195B × 3 calls | 2,643B × 1 call | **91%** |
| Explore symbol (body+callers+deps) | 19,969B × 3 calls | 4,566B × 1 call | **77%** |
| **Total** | **169,535B × 20 calls** | **18,053B × 9 calls** | **89%** |

Find refs returns _more_ bytes (+161B) than raw grep on this small corpus — the value there is accuracy (import-aware filtering), not size.

<details>
<summary>Run benchmarks yourself</summary>

```bash
# Native tools comparison (shell, response bytes)
bash bench/native_comparison.sh

# Progressive disclosure vs traditional edr workflows
bash bench/workflow_benchmark.sh

# --inside vs read+write for adding code to containers
bash bench/insert_benchmark.sh

# Internal performance (Go testing.B, supports benchstat)
go test -bench=. -benchmem -count=5 ./bench/
```

All shell benchmarks accept `ITERS=N` for iteration count (default 5), include warmup, correctness verification, and emit a JSON summary for CI tracking.

</details>

## Token-Saving Features

edr is designed to minimize context usage for LLM agents:

- **`--signatures`**: Read a class/struct/impl as just method signatures — 75–86% fewer tokens than reading the full body. For Go files, receiver methods are grouped under their types.
- **`--depth N`**: Progressive disclosure via tree-sitter AST. `--depth 2` shows method bodies with control flow blocks collapsed to `...`. Each depth level reveals one more nesting layer.
- **`--inside`**: Add a method to a class without reading the file first — 96% fewer response bytes vs read+edit. Go structs are rejected (methods go outside with receivers).
- **Delta reads**: Re-reading a file returns only what changed (`{delta: true, diff: "..."}`).
- **Slim edits**: Small diffs inline automatically; large diffs stored for retrieval via `get-diff`.
- **Body dedup**: `search --body` and `explore --gather --body` replace already-seen bodies with `[in context]`.

## Notes

- The index lives in `.edr/` at the repository root and should not be committed. This directory contains `index.db` (SQLite) and `writer.lock` (cross-process flock).
- `read` and `search --text` work on any text file, not just indexed source files.
- Edit and write commands reindex the affected file immediately, so new/renamed symbols are queryable right away. If reindexing fails, the edit still succeeds and an `index_error` field is included in the response.
- `batch` and `mcp` reuse one database connection and are the best fit for long-lived agent sessions.
- `.gitignore` patterns are respected for file walking; falls back to a built-in ignore list when no `.gitignore` exists.
- Symbol resolution is case-insensitive as a fallback — `opendb` resolves to `OpenDB`.
- Duplicate symbol names (e.g. multiple `init` functions) include a `qualifier` field for disambiguation.
- Supported languages: Go, Python, JavaScript/JSX, TypeScript/TSX, Rust, Java, C/H, Ruby.
- Full command-oriented guidance also exists in `CLAUDE.md`.
