# edr

Code navigation and editing CLI for coding agents. Builds a local tree-sitter symbol index, returns structured JSON, and minimizes token usage through progressive disclosure.

## Quick Start

```bash
go build -o edr .
./edr init                    # index the repo
./edr map --budget 500        # see what's here
./edr read src/main.go:main   # read a symbol
./edr mcp                     # start MCP server
```

Bootstrap for another repo:

```bash
./setup.sh /path/to/target/repo   # builds, installs, configures MCP, indexes
```

Requires Go 1.25+, a C compiler (for tree-sitter), and write access to create `.edr/`.

## What It Does

**Read** — files, symbols, line ranges, or batches with budget control:
```bash
edr read src/config.go:Scheduler --signatures   # API stubs only (85% smaller)
edr read src/config.go --depth 2                # blocks collapsed
edr read a.go b.go c.go:main --budget 500       # batch with budget
```

**Search** — symbols or text with optional source bodies:
```bash
edr search parseConfig --body --budget 300
edr search "TODO" --text --include "*.go" --context 3
```

**Edit** — text match, symbol replacement, or line range:
```bash
edr edit f.go --old_text "old" --new_text "new"
edr edit f.go parseConfig --new_text "func parseConfig() {}"
edr rename oldName newName --dry-run
```

**Write** — create files or insert into containers without reading first:
```bash
edr write src/models.go --inside UserStore --content "NewField int"
```

**Navigate** — map, explore, references, call chains:
```bash
edr map --dir internal/ --type function
edr explore parseConfig --gather --body --budget 700
edr refs Dispatch --chain editOK
edr refs parseConfig --impact --depth 3
```

**Verify** — run build checks:
```bash
edr verify                                # auto-detect
edr verify --command "go test ./..."
```

All commands return structured JSON. Edit commands return a `hash` for chaining. Query commands include `truncated`/`total_matches` when budget-limited.

## MCP Server

`edr mcp` exposes 13 typed tools over JSON-RPC: `edr_read`, `edr_edit`, `edr_write`, `edr_search`, `edr_map`, `edr_explore`, `edr_refs`, `edr_find`, `edr_rename`, `edr_verify`, `edr_init`, `edr_diff`, `edr_plan`.

```
edr_read(files: ["src/main.go:Config"], signatures: true, budget: 300)
edr_edit(file: "f.go", old_text: "old", new_text: "new")
edr_plan(reads: [{file: "a.go"}], edits: [{file: "a.go", old_text: "x", new_text: "y"}])
```

Session-aware optimizations:
- **Delta reads** — re-reads return `{unchanged: true}` or a diff
- **Slim edits** — small diffs inline; large diffs stored (retrieve with `edr_diff`)
- **Body dedup** — previously-seen bodies replaced with `[in context]`

## Token Savings

Compared to raw Read/Edit/Grep on a mixed-language test corpus:

| Scenario | Savings |
|---|---|
| Understand class API (`--signatures`) | **91%** |
| Orient in codebase (`map`) | **94%** |
| Edit a function | **97%** |
| Add method to class (`--inside`) | **99%** |
| Multi-file read with budget | **91%** |
| **Overall (9 workflows, 20→9 calls)** | **89%** |

<details>
<summary>Run benchmarks</summary>

```bash
bash bench/native_comparison.sh     # vs native tools
bash bench/workflow_benchmark.sh    # progressive vs traditional
bash bench/insert_benchmark.sh      # --inside vs read+write
go test -bench=. -count=5 ./bench/  # Go benchmarks
```
</details>

## Supported Languages

Go, Python, JavaScript/JSX, TypeScript/TSX, Rust, Java, C/H, Ruby

## Project Structure

```
cmd/           CLI, batch mode, MCP server
internal/
  index/       Tree-sitter parsing, SQLite index
  search/      Symbol and text search
  edit/        File edits, transactions, diffing
  dispatch/    Command routing (shared by CLI, batch, MCP)
  gather/      Context collection with budgets
  session/     MCP session state (deltas, dedup)
  output/      Structured JSON formatting
```

## Notes

- Index stored in `.edr/` (not committed). SQLite WAL mode with two-layer writer lock.
- `.gitignore` patterns respected. Case-insensitive symbol resolution as fallback.
- Edits reindex immediately; failures return `index_error` but don't block the edit.
- Full agent-oriented docs in `CLAUDE.md`.
