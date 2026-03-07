# edr

`edr` is a Go CLI for code navigation and editing aimed at coding agents. It builds a local symbol index for a repository, exposes structured JSON output, and supports three usage modes:

- CLI commands for one-off reads and edits
- `batch` mode for multiple JSONL requests over one process
- `mcp` mode for long-lived Model Context Protocol integrations

The tool is self-contained. It uses a local SQLite database under `.edr/` and does not depend on any external service. It respects `.gitignore` for file walking and supports concurrent access via WAL mode with busy timeouts.

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
./edr repo-map --budget 500
./edr search openAndEnsureIndex --body --budget 300
./edr read-symbol cmd/root.go openAndEnsureIndex --budget 250
./edr gather internal/edit/edit.go ReplaceSpan --budget 700
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

## Common Commands

Navigation and search:

```bash
./edr repo-map --budget 500
./edr repo-map --dir internal/ --type function       # filtered repo-map
./edr repo-map --glob "*.go" --grep Config           # glob + name filter
./edr symbols internal/edit/edit.go
./edr read-file setup.sh 1 60 --budget 300           # line-numbered output
./edr read-symbol cmd/root.go openAndEnsureIndex --budget 250
./edr expand cmd/root.go openAndEnsureIndex --body --deps --signatures --budget 600
./edr gather internal/edit/edit.go ReplaceSpan --budget 700 --body --signatures
./edr search Replace --budget 200
./edr search-text "TODO|FIXME" --regex --budget 200
./edr search-text "func Open" --context 3            # context lines around matches
./edr find-files "**/*.go" --budget 300
./edr batch-read cmd/root.go internal/output/output.go --budget 500
./edr xrefs openAndEnsureIndex                       # import-aware cross-references
```

Analysis:

```bash
./edr impact cmd/root.go openAndEnsureIndex --depth 3   # transitive callers
./edr call-chain Dispatch editOK                         # find call path A→B
./edr verify                                             # auto-detect build check
./edr verify --command "go test ./..." --timeout 60      # custom verification
```

Editing:

```bash
./edr replace-text CLAUDE.md "old text" "new text"
./edr replace-text src/main.go "v[0-9]+" "v2" --regex --all
./edr replace-lines cmd/root.go 31 62
./edr replace-span cmd/root.go 100 150
./edr replace-symbol cmd/root.go openAndEnsureIndex
./edr smart-edit cmd/root.go openAndEnsureIndex
./edr rename-symbol oldName newName --dry-run       # preview before applying
./edr diff-preview cmd/root.go openAndEnsureIndex
```

Atomic multi-file edits (via MCP or batch):

```json
{"cmd": "edit-plan", "flags": {"edits": [
  {"file": "src/config.go", "symbol": "parseConfig", "replacement": "..."},
  {"file": "src/main.go", "old_text": "oldFunc()", "new_text": "newFunc()"},
  {"file": "src/util.go", "start_line": 10, "end_line": 20, "replacement": "..."}
], "dry-run": true}}
```

Commands that modify file content read replacement content from stdin unless a command-specific flag is provided.

Query commands (`search`, `search-text`, `find-files`, `read-file`) include `truncated` and `total_matches` metadata when budget limits apply, so agents always know if results were cut short.

## Batch Mode

`batch` keeps the database open across multiple requests and reads one JSON object per line from stdin:

```bash
printf '%s\n' \
  '{"id":"1","cmd":"repo-map","args":[],"flags":{"budget":250}}' \
  '{"id":"2","cmd":"symbols","args":["cmd/root.go"],"flags":{}}' \
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

Batch multiple commands in one MCP call with `cmd: "multi"`:

```json
{
  "cmd": "multi",
  "flags": {
    "commands": [
      {"cmd": "read-symbol", "args": ["cmd/root.go", "openAndEnsureIndex"]},
      {"cmd": "symbols", "args": ["internal/edit/edit.go"]}
    ]
  }
}
```

The MCP server includes working-set dedup — if a read command returns identical content to a previous call in the same session, it returns `{"cached": true}` instead of re-sending the data. Edit commands automatically invalidate the cache for affected files.

## Repository Layout

- `main.go`: entrypoint
- `cmd/`: Cobra CLI commands, batch mode, MCP server
- `internal/index/`: indexing, symbol resolution, SQLite storage
- `internal/search/`: symbol and text search
- `internal/edit/`: file edits, transactions, diffing
- `internal/dispatch/`: shared command dispatch for batch and MCP
- `internal/gather/`: task-oriented context collection

## Notes

- The index lives in `.edr/` at the repository root and should not be committed.
- `read-file` and `search-text` work on any text file, not just indexed source files.
- `batch` and `mcp` reuse one database connection and are the best fit for long-lived agent sessions.
- `.gitignore` patterns are respected for file walking; falls back to a built-in ignore list when no `.gitignore` exists.
- Symbol resolution is case-insensitive as a fallback — `opendb` resolves to `OpenDB`.
- Duplicate symbol names (e.g. multiple `init` functions) include a `qualifier` field for disambiguation.
- Full command-oriented guidance also exists in `CLAUDE.md`.
