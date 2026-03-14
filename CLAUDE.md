# EDR — Your Default Tool for Reading, Editing, and Navigating Files

**edr is your primary tool for ALL file operations.** Use `edr` via Bash instead of Read, Edit, Write, Grep, and Glob. It gives you structured output, token budgets, and smart operations that raw file tools can't match.

**`edr` is your primary tool.** It batches reads, searches, explores, edits, writes, renames, and verification into one call. Sessions are **automatic** (derived from parent PID) — delta reads, body dedup, and slim edits work out of the box.

```bash
# Typical 2-call workflow for any task:
edr do '{                                          # Call 1: gather ALL context
  "reads": [{"file": "src/main.go", "symbol": "Server"}],
  "queries": [
    {"cmd": "search", "pattern": "handleRequest", "body": true},
    {"cmd": "map", "dir": "internal/", "type": "function"}
  ]
}'
edr do '{                                          # Call 2: ALL mutations + verify
  "edits": [{"file": "src/main.go", "old_text": "old", "new_text": "new"}],
  "writes": [{"file": "src/new_test.go", "content": "...", "mkdir": true}],
  "verify": true
}'
```

**Only fall back to built-in tools when:**
- You need non-text files or shell operations

## Why edr over built-in tools

| Instead of... | Use edr... | Why |
|---|---|---|
| `Read` (whole file) | `edr do '{"reads": [{"file": "f.go"}]}'` | Budget-controlled, batchable |
| `Edit` (old/new strings) | `edr do '{"edits": [{"file": "f.go", "old_text": "x", "new_text": "y"}]}'` | Atomic multi-file, auto re-index |
| `Write` (create file) | `edr do '{"writes": [{"file": "f.go", "content": "...", "mkdir": true}]}'` | Auto-indexes, batchable with edits |
| `Grep` (text search) | `edr do '{"queries": [{"cmd": "search", "pattern": "pat", "body": true}]}'` | Structured results, batchable |
| `Glob` (find files) | `edr do '{"queries": [{"cmd": "find", "pattern": "**/*.go"}]}'` | Glob with `**`, batchable |
| Multiple tool calls | One `edr do '{"reads": [...], "edits": [...], "verify": true}'` | Everything in 1-2 calls |

## Development workflow

**Every time you change Go source files, rebuild and reinstall:**
```bash
go build -o edr . && go install
```

## Setup (any environment)

```bash
# One command — installs Go/gcc if needed, builds, installs to PATH:
./setup.sh /path/to/target/repo

# Or manually:
go build -o edr .           # Build (requires Go + C compiler for tree-sitter)
./edr init                   # Force re-index (auto-indexes on first query)
```

For cloud agents: clone this repo, run `./setup.sh /path/to/your/project`, and edr is ready. The setup script handles everything — dependency installation, build, PATH setup, and indexing.

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
back to symbol resolution — so `edr read Config` works if `Config` is a known symbol.

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

All mutation commands return `status` ("applied", "applied_index_stale") and `hash` for chaining.

```bash
# edit: unified edit command — old_text/new_text is the primary mode
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

> **Batch usage**: `edr do '{"edits": [{"file": "file.go", "old_text": "old code", "new_text": "new code"}]}'`
> This is the direct equivalent of the built-in Edit tool's `old_string`/`new_string` pattern.
> **Move**: `edr do '{"edits": [{"file": "file.go", "move": "FuncA", "after": "FuncB"}]}'`

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

# Insert inside a container (class/struct/impl) — no need to read the file first
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

> **Batch usage**: `edr do '{"renames": [{"old_name": "Foo", "new_name": "Bar", "dry_run": true}]}'`

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

## Batch CLI (`edr do`)

`edr do` handles complete agent workflows in minimal round trips.
It supports seven operation types, all in one call. Pass JSON as argument or via stdin.

```bash
# Gather context + make changes + verify in one call:
edr do '{
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
}'

# Typical 2-call workflow:
# Call 1: edr do '{"reads": [...], "queries": [...]}'     — gather ALL context
# Call 2: edr do '{"edits": [...], "writes": [...], "verify": true}'  — ALL mutations + verify
```

## Context-Aware Responses

edr automatically tracks what content you've already seen across invocations (via PPID-based sessions):

- **Slim edits**: Small diffs (<=20 changed lines) are returned inline automatically. Large diffs are stripped to `{ok, file, hash, lines_changed, diff_available}` — use `queries: [{cmd: "diff", file: "..."}]` to retrieve them.
- **Delta reads**: Re-reading a file/symbol you've already seen returns `{unchanged: true}` if identical, or `{delta: true, diff: "..."}` with just the changes. Pass `full: true` to force full content.
- **Body dedup**: `explore(gather: true, body: true)` and `search(body: true)` replace bodies you've already seen with `"[in context]"` and report `skipped_bodies`. New/changed bodies are returned in full.

These optimizations are automatic. Sessions persist to `.edr/sessions/<ppid>.json` and are
GC'd when the parent process exits. Use `--no-session` to disable, or `--session <token>` to override.
Renames and `init: true` clear all tracking state. Manage sessions with `edr session list|clear|gc`.

## Key Principles

1. **Start with `edr`** — batch reads, queries, edits, writes, renames, and verify in one call. Minimize round trips.
2. **Use `budget`** to control context size. Don't dump entire files.
3. **Gather context in one call** — `edr(reads: [...], queries: [{cmd: "search", ...}, {cmd: "explore", ...}])`.
4. **Mutate + verify in one call** — `edr(edits: [...], writes: [...], verify: true)`.
5. **Use `signatures: true`** to understand a container's API without reading implementation (75-86% fewer tokens).
6. **Preview renames** — `edr(renames: [{old_name: "X", new_name: "Y", dry_run: true}])`.
7. **Check impact before refactoring** — `edr(queries: [{cmd: "refs", symbol: "X", impact: true}])`.
8. **Small edit diffs are inline** — diffs <=20 lines are included automatically. Large diffs are stored; use `queries: [{cmd: "diff", file: "..."}]` to retrieve.
9. **Re-reads are delta** — `{unchanged: true}` or `{delta: true, diff: "..."}`. Use `full: true` to force full content.
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
- `internal/cmdspec/cmdspec.go` — Canonical command registry: names, categories, flags, session behavior, batch keys
- `internal/trace/trace.go` — Collector, CallBuilder, schema, BenchSession scoring
- `internal/session/session.go` — PostProcessStats (DeltaReads, BodyDedup, SlimEdits)
- `internal/session/persist.go` — File-backed session persistence, GC
- `cmd/do.go` — handleDo batch orchestrator
- `cmd/bench_session.go` — CLI command

## Benchmarks

Performance, correctness, and session benchmarks live in `bench/`:

```bash
# Run all benchmarks
go test ./bench/ -bench . -benchmem

# Run correctness tests (adversarial fixtures — gates all optimization work)
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
- **Repeated method names**: `Validate` in pkg_a vs pkg_b — measures cross-contamination precision
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

`edr do` handles everything: reads, queries (search/explore/refs/map/find/diff), edits, writes, renames, verify, init.
Read params: `file`, `symbol?`, `budget?`, `signatures?`, `depth?`, `start_line?`, `end_line?`, `symbols?`, `full?`.

All output is structured JSON. File paths are relative to repo root. Edit commands return `hash`.

Sessions are automatic (PPID-based). Use `--no-session` to disable or `--session <token>` to override.

