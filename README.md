# edr

`edr` is a local code navigation and editing CLI built to save tokens during
agent-driven work. If you are using Codex or Claude Code, it gives the agent a
structured way to inspect code, search symbols, preview edits, and apply
changes without falling back to raw file dumps and ad hoc grep.

It indexes the current repo with tree-sitter, stores the index locally in
`.edr/`, and returns structured JSON so agent wrappers can make smaller, more
predictable calls.

## Why Use It

Use `edr` when you want an agent to:

- orient in an unfamiliar repo without reading full files
- read one symbol or a small batch of symbols instead of dumping everything
- search text and symbols with budget-aware output
- preview edits and renames before applying them
- bundle related reads or edits into one operation

Compared to shelling out to `cat`, `grep`, and manual patch logic, `edr`
provides a more consistent contract for code-oriented workflows.

## Quick Start

Build and try it on the current repo:

```bash
go build -o edr .
./edr init
./edr map --budget 400
./edr search Dispatch --body --budget 300
./edr read internal/dispatch/dispatch.go:Dispatch
```

Bootstrap another repo:

```bash
./setup.sh /path/to/target/repo
```

Requirements:

- Go 1.25+
- a C compiler for tree-sitter grammars
- write access to create `.edr/`

## Start Here As A Human

If you are evaluating `edr` for Codex or Claude Code, this is the shortest path
to understanding what it does well.

1. Orient in the repo:

```bash
edr map --budget 500
edr map --dir internal/ --type function --grep dispatch
```

2. Find and read targeted code:

```bash
edr search Dispatch --body --budget 300
edr read internal/dispatch/dispatch.go:Dispatch
edr read cmd/commands.go:dispatchWithSession cmd/mcp.go:routeTool --budget 600
```

3. Understand callers and impact:

```bash
edr refs Dispatch
edr refs dispatchCmd --chain Dispatch
edr explore Dispatch --gather --body --budget 700
```

4. Preview and apply an edit:

```bash
edr edit internal/dispatch/dispatch.go --old_text "unknown command" --new_text "unsupported command" --dry-run
edr rename oldName newName --dry-run
```

5. Verify after changes:

```bash
edr verify
edr verify --command "go test ./..."
```

## Core Commands

### Read

Read files, line ranges, symbols, or batches:

```bash
edr read README.md
edr read src/config.go 10 50 --budget 200
edr read src/config.go:parseConfig
edr read src/config.go parseConfig
edr read a.go b.go c.go:main --budget 500
edr read src/models.py:UserService --signatures
edr read src/scheduler.py --depth 2
```

Important behavior:

- `edr read <single-arg>` treats that argument as a file path
- if you only know a symbol name, start with `edr search <symbol>` or `edr explore [file] <symbol>`
- `--signatures` is most useful for container-like symbols
- `--depth` is for progressive disclosure rather than full source dumps

### Search

Search symbols or text:

```bash
edr search parseConfig --body --budget 300
edr search "TODO" --text --include "*.go" --context 3
edr search "func.*Config" --regex --budget 300
```

### Map / Explore / Refs

Navigate the indexed code graph:

```bash
edr map --budget 500
edr map internal/dispatch/dispatch.go
edr explore parseConfig --body --callers --deps
edr explore parseConfig --gather --body --budget 1500
edr refs parseConfig
edr refs parseConfig --impact --depth 3
edr refs main --chain parseConfig
```

### Edit / Rename / Write

Apply targeted changes with preview support:

```bash
edr edit f.go --old_text "old" --new_text "new"
edr edit f.go parseConfig --new_text "func parseConfig() {}"
edr edit f.go --start_line 45 --end_line 60 --new_text "replacement code"
edr edit f.go --old_text "old" --new_text "new" --dry-run

edr rename oldName newName --dry-run
edr rename oldName newName --scope "internal/**"

edr write src/main.go
edr write src/config.go --append
edr write src/config.go --after parseConfig
edr write src/models.py --inside UserService
```

Go-specific note:

- for Go structs, methods belong after the type, not inside it
- `write --inside Store` will reject that case and point you to `--after Store`

### Batch And Multi-Step Workflows

For grouped operations, prefer `batch` or MCP over many short-lived CLI
processes.

```bash
printf '{"id":"1","cmd":"map","args":["internal/dispatch/dispatch.go"],"flags":{}}\n' | edr batch
```

Atomic edit plans:

```bash
edr edit-plan --dry-run
```

`edit-plan --dry-run` currently previews per-edit diffs, not one final combined
patch per file.

## MCP Server

Run `edr` as an MCP server when you want a persistent indexed session for Codex
or Claude Code:

```bash
./edr mcp
```

Available tools:

- `edr_read`
- `edr_edit`
- `edr_write`
- `edr_search`
- `edr_map`
- `edr_explore`
- `edr_refs`
- `edr_find`
- `edr_rename`
- `edr_verify`
- `edr_init`
- `edr_diff`
- `edr_plan`

Example calls:

```text
edr_read(files: ["src/main.go:Config"], signatures: true, budget: 300)
edr_edit(file: "f.go", old_text: "old", new_text: "new")
edr_plan(reads: [{file: "a.go"}], edits: [{file: "a.go", old_text: "x", new_text: "y"}])
```

Session-aware behavior in MCP mode:

- delta reads can return `{unchanged: true}` or a diff
- small edit diffs are inlined automatically
- previously seen bodies can be replaced with `[in context]`

## Output And Caveats

All commands return structured JSON.

Current behavior worth knowing:

- edit commands return a `hash` for chaining
- `search` returns `total_matches`
- `find` returns `total_matched`
- separate short-lived CLI processes can still hit `SQLITE_BUSY` / `database is locked` under contention
- for grouped operations, `batch` or MCP is more reliable than many concurrent one-shot CLI invocations
- `.gitignore` patterns are respected
- edits reindex immediately; if reindexing fails, the edit still succeeds and may include `index_error`

## Supported Languages

Go, Python, JavaScript/JSX, TypeScript/TSX, Rust, Java, C/H, Ruby

## Benchmarks

Compared to raw Read/Edit/Grep on a mixed-language test corpus:

| Scenario | Savings |
|---|---|
| Understand class API (`--signatures`) | **91%** |
| Orient in codebase (`map`) | **94%** |
| Edit a function | **97%** |
| Add method to class (`--inside`) | **99%** |
| Multi-file read with budget | **91%** |
| **Overall (9 workflows, 20->9 calls)** | **89%** |

Run benchmarks:

```bash
bash bench/native_comparison.sh
bash bench/workflow_benchmark.sh
bash bench/insert_benchmark.sh
go test -bench=. -count=5 ./bench/
```

## Project Structure

```text
cmd/           CLI, batch mode, MCP server
internal/
  index/       tree-sitter parsing, SQLite index
  search/      symbol and text search
  edit/        file edits, transactions, diffing
  dispatch/    command routing shared by CLI, batch, and MCP
  gather/      context collection with budgets
  session/     MCP session state (deltas, dedup)
  output/      structured JSON formatting
```

## More Detail

For the longer agent-facing command reference, see [CLAUDE.md](CLAUDE.md).
