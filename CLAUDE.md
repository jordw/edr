# EDR: Your Default Tool for Reading, Editing, and Navigating Files

**edr is your primary tool for ALL file operations.** Use `edr` via Bash instead of Read, Edit, Write, Grep, and Glob. It gives you structured output, token budgets, and smart operations that raw file tools can't match.

**`edr serve --stdio`** is the primary interface. It starts a persistent NDJSON server over stdin/stdout. Sessions are **connection-scoped** (process lifetime). Delta reads, body dedup, and slim edits work automatically.

```bash
# Start the server once per agent session:
edr serve --stdio

# Then send NDJSON requests on stdin, read NDJSON responses from stdout.
# Request 1: gather ALL context
{"request_id":"1","reads":[{"file":"src/main.go","symbol":"Server"}],"queries":[{"cmd":"search","pattern":"handleRequest","body":true},{"cmd":"map","dir":"internal/","type":"function"}]}

# Request 2: ALL mutations + verify
{"request_id":"2","edits":[{"file":"src/main.go","old_text":"old","new_text":"new"}],"writes":[{"file":"src/new_test.go","content":"...","mkdir":true}],"verify":true}
```

**Fall back to built-in tools when:**
- You need non-text files or shell operations
- edr is not yet built (fresh clone, first setup)
- You are working on the edr codebase itself and a broken edit prevents rebuild

## Why edr over built-in tools

| Instead of... | Use edr... | Why |
|---|---|---|
| `Read` (whole file) | `{"reads": [{"file": "f.go"}]}` | Budget-controlled, batchable |
| `Edit` (old/new strings) | `{"edits": [{"file": "f.go", "old_text": "x", "new_text": "y"}]}` | Atomic multi-file, auto re-index |
| `Write` (create file) | `{"writes": [{"file": "f.go", "content": "...", "mkdir": true}]}` | Auto-indexes, batchable with edits |
| `Grep` (text search) | `{"queries": [{"cmd": "search", "pattern": "pat", "body": true}]}` | Structured results, batchable |
| `Glob` (find files) | `{"queries": [{"cmd": "find", "pattern": "**/*.go"}]}` | Glob with `**`, batchable |
| Multiple tool calls | One `{"reads": [...], "edits": [...], "verify": true}` | Everything in 1-2 requests |

## Development workflow (edr on itself)

**This is the edr codebase.** When working here, edr is both the tool and the target.

- **Rebuild after every Go source change:** `go build -o edr . && go install`
- **The running `edr serve` process is the old binary.** Source edits do not take effect until you rebuild. If you need the new behavior (e.g. to test a fix), restart the server after rebuilding.
- **If a broken edit prevents `go build`:** fall back to built-in Read/Edit tools to fix the compile error, then rebuild.

## Setup (any environment)

```bash
# One command. Installs Go/gcc if needed, builds, installs to PATH:
./setup.sh /path/to/target/repo

# Or manually:
go build -o edr .           # Build (requires Go + C compiler for tree-sitter)
./edr init                   # Force re-index (auto-indexes on first query)
```

For cloud agents: clone this repo, run `./setup.sh /path/to/your/project`, and edr is ready. The setup script handles everything: dependency installation, build, PATH setup, and indexing.

## Supported languages

**Symbol indexing** (map, read, edit, signatures, inside, move): Go, Python, JavaScript/JSX, TypeScript/TSX, Rust, Java, C, C++, Ruby, PHP, Zig, Lua, Bash/Shell.

**Import-aware semantic refs** (refs, rename, explore callers/deps): Go, Python, JavaScript, TypeScript. Other languages fall back to text-based refs.

edr can **read** any text file regardless of language support. Symbol-aware features require a supported language.

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
back to symbol resolution, so `edr read Config` works if `Config` is a known symbol.

## Searching (`search`)

```bash
# Symbol search: structured results, optional body snippets
edr search "parseConfig" --body --budget 500

# Text search: use --text, or auto-detected with --regex/--include/--exclude/--context
edr search "retry backoff" --text --budget 300
edr search "func.*Config" --regex --budget 300
edr search "TODO" --include "*.go" --exclude "*_test.go"
edr search "TODO" --text --context 3

# Find all references to a symbol (import-aware, filters false positives)
edr refs parseConfig
edr refs src/config.go parseConfig    # scoped to a specific file's symbol
```

## Editing (`edit`)

All mutation commands return `status` ("applied", "applied_index_stale") and `hash` for chaining.

```bash
# edit: unified edit command. old_text/new_text is the primary mode
# Text match: find old_text and replace with new_text (like Edit tool's old_string/new_string)
edr edit src/config.go --old_text "oldName" --new_text "newName"
edr edit src/config.go --old_text "v[0-9]+" --regex --all --new_text "v2"

# Symbol replacement: replace an entire symbol body with new_text
edr edit src/config.go parseConfig --new_text "func parseConfig() { ... }"

# Line-range: replace lines with new_text
edr edit src/config.go --start_line 45 --end_line 60 --new_text "replacement code"

# Move a symbol to a new position (atomic delete + insert)
edr edit src/config.go --move parseConfig --after main
edr edit src/config.go --move parseConfig --before initDefaults
edr edit src/config.go --move parseConfig --after main --dry-run

# Preview changes without applying
edr edit src/config.go --old_text "oldName" --dry-run --new_text "newName"
```

> **Batch usage**: `{"edits": [{"file": "file.go", "old_text": "old code", "new_text": "new code"}]}`
> This is the direct equivalent of the built-in Edit tool's `old_string`/`new_string` pattern.
> **Move**: `{"edits": [{"file": "file.go", "move": "FuncA", "after": "FuncB"}]}`

## Writing (`write`)

```bash
# Create or overwrite a file (content from stdin, --content, or --new_text)
edr write src/main.go                        # CLI: content from stdin
edr write src/main.go --content "package main"  # or pass content directly
edr write config/app/settings.yaml --mkdir   # creates parent dirs

# Append to an existing file
edr write src/config.go --append

# Insert code right after a specific symbol
edr write src/config.go --after parseConfig

# Insert inside a container (class/struct/impl) without reading the file first
edr write src/models.go --inside UserStore     # adds before closing }
edr write src/models.py --inside UserService   # correct Python indentation
edr write src/models.go --inside UserStore --after Get  # insert after specific method
edr write src/models.go --inside UserStore --new_text "Name string"  # --new_text also works
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

> **Batch usage**: `{"renames": [{"old_name": "Foo", "new_name": "Bar", "dry_run": true}]}`

## Orientation (`map`, `explore`)

```bash
# Symbol map of the whole repo. Start here when exploring
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

## Stdio Server (`edr serve --stdio`)

`edr serve --stdio` is the primary interface for agents. It starts a persistent NDJSON server
that reads requests from stdin and writes responses to stdout. Stderr is used for logs.

### Protocol

- **Transport**: NDJSON over stdin/stdout. One JSON object per line.
- **Sequential**: One request in flight at a time.
- **Request envelope**: `{"request_id": "1", ...batch fields...}`. Required `request_id`, optional `control`.
- **Response envelope**: `{"request_id": "1", "ok": true, "result": {...}}`. Wraps the batch result.
- **Control messages**: `ping` → `pong`, `status` → index/root info, `shutdown` → clean exit.
- **Errors**: `{"request_id": "1", "ok": false, "error": {"code": "...", "message": "..."}}`

### Example session

```bash
# Start the server
edr serve --stdio

# Ping
{"request_id":"1","control":"ping"}
# Response: {"request_id":"1","ok":true,"control":"pong"}

# Read a symbol
{"request_id":"2","reads":[{"file":"src/main.go","symbol":"Server"}]}
# Response: {"request_id":"2","ok":true,"result":{"reads":[...]}}

# Edit + verify
{"request_id":"3","edits":[{"file":"src/main.go","old_text":"old","new_text":"new"}],"verify":true}
# Response: {"request_id":"3","ok":true,"result":{"edits":...,"verify":...,"summary":...}}

# Shutdown
{"request_id":"4","control":"shutdown"}
# Response: {"request_id":"4","ok":true,"control":"shutdown"}
```

### Batch request fields

```jsonc
{
  "request_id": "1",                    // required
  "reads": [{"file": "src/main.go"}, {"file": "src/config.go", "symbol": "parseConfig"}],
  "queries": [
    {"cmd": "search", "pattern": "handleRequest", "body": true},
    {"cmd": "explore", "symbol": "Server", "gather": true, "body": true},
    {"cmd": "map", "dir": "internal/", "type": "function"},
    {"cmd": "refs", "symbol": "Config", "impact": true},
    {"cmd": "diff", "file": "src/main.go"}
  ],
  "edits": [
    {"file": "src/main.go", "old_text": "oldFunc()", "new_text": "newFunc()"},
    {"file": "src/config.go", "symbol": "parseConfig", "new_text": "..."},
    {"file": "src/main.go", "move": "initDB", "after": "main"}
  ],
  "writes": [{"file": "src/new.go", "content": "package main\n...", "mkdir": true}],
  "renames": [{"old_name": "OldFunc", "new_name": "NewFunc", "dry_run": true}],
  "verify": true,
  "init": true
}
```

### Typical 2-request workflow

```
Request 1: {"request_id":"1","reads":[...],"queries":[...]}          // gather ALL context
Request 2: {"request_id":"2","edits":[...],"writes":[...],"verify":true}  // ALL mutations + verify
```

## Context-Aware Responses

edr automatically tracks what content you've already seen within the `edr serve` connection:

- **Slim edits**: Small diffs (<=20 changed lines) are returned inline automatically. Large diffs are stripped to `{ok, file, hash, lines_changed, diff_available}`. Use `queries: [{cmd: "diff", file: "..."}]` to retrieve them.
- **Delta reads**: Re-reading a file/symbol you've already seen returns `{unchanged: true}` if identical, or `{delta: true, diff: "..."}` with just the changes. Pass `full: true` to force full content.
- **Body dedup**: `explore(gather: true, body: true)` and `search(body: true)` replace bodies you've already seen with `"[in context]"` and report `skipped_bodies`. New/changed bodies are returned in full.

These optimizations are automatic and connection-scoped. They live for the `edr serve` process lifetime.
Renames and `init: true` clear all tracking state. Single CLI commands use ephemeral sessions (no persistence).

## Key Principles

1. **Start with `edr serve`.** Batch reads, queries, edits, writes, renames, and verify in one request. Minimize round trips. Mutation responses include a `summary` with status, counts, and `hints` for next steps.
2. **Use `budget`** to control context size. Don't dump entire files.
3. **Gather context in one request:** `{"reads": [...], "queries": [{...}, {...}]}`.
4. **Mutate + verify in one request:** `{"edits": [...], "writes": [...], "verify": true}`.
5. **Use `signatures: true`** to understand a container's API without reading implementation (75-86% fewer tokens).
6. **Preview renames:** `{"renames": [{"old_name": "X", "new_name": "Y", "dry_run": true}]}`.
7. **Check impact before refactoring:** `{"queries": [{"cmd": "refs", "symbol": "X", "impact": true}]}`.
8. **Small edit diffs are inline.** Diffs <=20 lines are included automatically. Large diffs are stored; use `queries: [{cmd: "diff", file: "..."}]` to retrieve.
9. **Re-reads are delta.** `{unchanged: true}` or `{delta: true, diff: "..."}`. Use `full: true` to force full content.
10. **Use `--inside`** to add fields/methods to a class without reading the file first.

## Session Tracing

Sessions are automatically traced to `.edr/traces.db` (append-only, separate from the index).
Traces capture request shape, edit/verify/query events, and session optimization hits.

```bash
# Score the most recent session (or pass a session ID)
edr bench-session

# Output includes raw counts + derived analysis:
# - read_efficiency: delta hits / total reads
# - edit_success_rate, verify_pass_rate
# - optimization_rate: (delta + dedup + slim) / total calls
# - tokens_per_call, avg_call_duration_ms
# - edits_reverted: files where final hash == first hash (wasted work)
```

Key files:
- `internal/cmdspec/cmdspec.go`: Canonical command registry (names, categories, flags, session behavior, batch keys)
- `internal/trace/trace.go`: Collector, CallBuilder, schema, BenchSession scoring
- `internal/session/session.go`: PostProcessStats (DeltaReads, BodyDedup, SlimEdits)
- `cmd/serve.go`: Stdio server loop, request/response envelope types
- `cmd/batch.go`: handleDo batch orchestrator, execute helpers
- `cmd/bench_session.go`: CLI command

## Benchmarks

Performance, correctness, and session benchmarks live in `bench/`:

```bash
# Run all benchmarks
go test ./bench/ -bench . -benchmem

# Run correctness tests (adversarial fixtures, gates all optimization work)
go test ./bench/ -run TestCorrectness -v

# Run scenario dispatch validation (verifies JSON scenario definitions dispatch correctly)
go test ./bench/ -run TestScenarioDispatch -v

# Run the multi-language session test (55 calls across 8 languages)
go test ./bench/ -run TestSessionMultiLang -v

# Run the session workflow performance benchmark
go test ./bench/ -bench BenchmarkSessionWorkflow -benchmem

# Emit benchmark results as JSON for automation
go run ./bench/cmd/benchjson
go run ./bench/cmd/benchjson -o results.json   # write to file
```

### Test data

`bench/testdata/` contains a realistic multi-language task queue system:
Go, Python, Rust, C/H, Java, Ruby, JS, TSX.

`bench/testdata/adversarial/` contains targeted fixtures for correctness testing:
ambiguous symbols (Config/Init/Validate defined in 6+ files across Go/Python/JS),
shadowed locals, aliased imports, and repeated method names on different types.

### Correctness track

`bench/correctness_test.go` runs adversarial tests with precision/recall metrics:
- **Ambiguous symbols**: bare `Config` must fail with "ambiguous", file-scoped resolves
- **Repeated method names**: `Validate` in pkg_a vs pkg_b, measures cross-contamination precision
- **Cross-language search**: `Config`/`validate` found in 3+ languages
- **Edit + reindex**: edit a symbol, verify index updates, verify refs still resolve
- **Rename safety**: ambiguous symbols rejected, unique symbols rename across files
- **Precision/recall**: refs results measured against expected sets (threshold >= 0.5, tighten over time)

### Scenario definitions

`bench/scenarios/` contains JSON scenario definitions that drive both Go tests and shell benchmarks.
`bench/scenarios/fixture.json` is the canonical definition for the built-in testdata.
`bench/scenarios/real/*.json` define scenarios for real repos (urfave/cli, vitess, click, etc.).

The shell scripts (`native_comparison.sh`) accept both `.sh` profiles and `.json` scenarios.
`bench/json_to_shell.py` bridges JSON scenarios to shell variable assignments.

### Runtime metrics

Benchmarks report custom metrics via `b.ReportMetric()`:
- `response_bytes`: total JSON response size (context-efficiency)
- `db_size_kb` / `wal_size_kb`: SQLite index + WAL size after indexing
- `heap_alloc_kb`: current heap allocation at measurement point

### Session tests

**TestSessionMultiLang** exercises the full session lifecycle with trace validation:
orientation (map, find), signatures + full reads across all languages, delta reads,
cross-language search + body dedup, explore/refs, dry-run + real edits, write inside,
batch reads, depth-2 reads, and bench-session scoring of the resulting trace.

## CLI Reference

`edr serve --stdio` is the primary batch interface. Requests support: reads, queries (search/explore/refs/map/find/diff), edits, writes, renames, verify, init.
Read params: `file`, `symbol?`, `budget?`, `signatures?`, `depth?`, `start_line?`, `end_line?`, `symbols?`, `full?`.

All output is structured JSON. File paths are relative to repo root. Edit commands return `hash`.

Sessions are connection-scoped within `edr serve`. Single CLI commands use ephemeral in-memory sessions.
