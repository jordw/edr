# edr

`edr` is a Go CLI for code navigation and editing aimed at coding agents. It builds a local symbol index for a repository, exposes structured JSON output, and supports three usage modes:

- CLI commands for one-off reads and edits
- `batch` mode for multiple JSONL requests over one process
- `mcp` mode for long-lived Model Context Protocol integrations

The tool is self-contained. It uses a local SQLite database under `.edr/` and does not depend on any external service.

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
./edr symbols internal/edit/edit.go
./edr read-file setup.sh 1 60 --budget 300
./edr read-symbol cmd/root.go openAndEnsureIndex --budget 250
./edr expand cmd/root.go openAndEnsureIndex --body --deps --budget 600
./edr gather internal/edit/edit.go ReplaceSpan --budget 700
./edr search Replace --budget 200
./edr search-text "TODO|FIXME" --regex --budget 200
./edr find-files "**/*.go" --budget 300
```

Editing:

```bash
./edr replace-text CLAUDE.md "old text" "new text"
./edr replace-lines cmd/root.go 31 62
./edr replace-span cmd/root.go 100 150
./edr replace-symbol cmd/root.go openAndEnsureIndex
./edr smart-edit cmd/root.go openAndEnsureIndex
./edr rename-symbol oldName newName --dry-run
./edr diff-preview cmd/root.go openAndEnsureIndex
```

Commands that modify file content read replacement content from stdin unless a command-specific flag is provided.

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
- Full command-oriented guidance also exists in `CLAUDE.md`.
