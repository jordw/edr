# EDR: Your Default Tool for Reading, Editing, and Navigating Files

**edr is your primary tool for ALL file operations.** Use `edr` via Bash instead of Read, Edit, Write, Grep, and Glob. It gives you structured output, token budgets, and smart operations that raw file tools can't match.

**`edr serve`** starts a background server. Then use **batch flags** (`-r`, `-s`, `-e`, `-w`) for all operations. Session optimizations (delta reads, body dedup, slim edits) work automatically across CLI calls.

```bash
# Start the server once per agent session:
edr serve

# Gather context (batch read + search in one call):
edr -r src/main.go:Server --sig -r src/config.go -s "handleRequest"

# Mutate + verify (auto-verifies when edits present):
edr -e src/main.go --old "oldFunc()" --new "newFunc()" -w src/new_test.go --content "..."
```

**Fall back to built-in tools when:**
- You need non-text files or shell operations
- edr is not yet built (fresh clone, first setup)
- You are working on the edr codebase itself and a broken edit prevents rebuild

## Why edr over built-in tools

| Instead of... | Use edr... | Why |
|---|---|---|
| `Read` (whole file) | `edr -r f.go` | Budget-controlled, batchable |
| `Edit` (old/new strings) | `edr -e f.go --old "x" --new "y"` | Atomic multi-file, auto re-index, auto-verify |
| `Write` (create file) | `edr -w f.go --content "..." --mkdir` | Auto-indexes, batchable with edits |
| `Grep` (text search) | `edr -s "pat"` | Structured results, body on by default |
| `Glob` (find files) | `edr find "**/*.go"` | Glob with `**`, structured output |
| Multiple tool calls | `edr -r f.go -s "pat" -e f.go --old "x" --new "y"` | Everything in one call |

## Development workflow (edr on itself)

**This is the edr codebase.** When working here, edr is both the tool and the target.

- **Rebuild after every Go source change:** `go build -o edr . && go install`
- **The running `edr serve` process is the old binary.** Source edits do not take effect until you rebuild. If you need the new behavior (e.g. to test a fix), run `edr serve --stop && go build -o edr . && edr serve`.
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

> **Batch**: `edr -e file.go --old "old code" --new "new code" -e other.go --old "x" --new "y"`
> Multiple edits in one call are atomic. Verify runs automatically.
> **Move**: `edr -e file.go --move FuncA --after FuncB`

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

> Rename is a standalone command (not batchable with `-e`/`-r`). Run `edr verify` after if needed.

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

## Server (`edr serve`)

`edr serve` starts a background daemon with a Unix socket (`.edr/serve.sock`).
All CLI commands automatically proxy through the server for session benefits.

```bash
edr serve          # daemonize, create socket, print PID
edr serve --stop   # send shutdown, clean up

# No server running? CLI commands fall back to ephemeral dispatch.
# Everything works either way — the server is a performance optimization.
```

## Batch Operations

Batch flags let you combine multiple operations in a single CLI call.
Operations are specified as ordered flags — modifier flags apply to the preceding operation.

| Flag | Operation | Key modifiers |
|------|-----------|---------------|
| `-r file[:symbol]` | Read | `--sig`, `--depth N`, `--budget N`, `--lines N-M`, `--full` |
| `-s "pattern"` | Search | `--no-body`, `--include`, `--exclude`, `--regex`, `--text` |
| `-e file[:symbol]` | Edit | `--old`/`--new`, `--lines N-M`, `--all`, `--move`, `--dry-run` |
| `-w file` | Write | `--content`, `--after`, `--inside`, `--mkdir`, `--append` |
| `-v` | Verify | Implicit when edits present; `--no-verify` to skip |

### Defaults

- **Search body on**: `-s` includes match bodies by default (use `--no-body` to suppress)
- **Auto-verify**: edits automatically trigger `go build`/`go vet` (use `--no-verify` to skip)
- **Exit codes**: non-zero if any operation fails (structured JSON still printed)
- **`--new -`**: reads replacement text from stdin (for heredoc large content)

### Examples

```bash
# Gather context: signatures + search in one call
edr -r src/config.go --sig -r src/main.go:Server -s "handleRequest"

# Multi-file edit with auto-verify
edr -e src/config.go --old "oldFunc()" --new "newFunc()" \
    -e src/main.go --old "oldFunc()" --new "newFunc()"

# Symbol replacement via heredoc
edr -e src/config.go:parseConfig --new - <<'EOF'
func parseConfig() (*Config, error) {
    // new implementation
}
EOF

# Write inside a struct + edit another file
edr -w src/models.go --inside UserStore --content "CreatedAt time.Time" \
    -e src/models.go --old "func New(" --new "func NewWithTimestamp("

# Read with line range, edit with line range
edr -r src/config.go --lines 10-50 -e src/config.go --lines 10-15 --new "replacement"
```

### Typical 2-call workflow

```bash
# Call 1: gather ALL context
edr -r src/config.go:parseConfig --sig -r src/main.go:Server -s "handleRequest"

# Call 2: ALL mutations (verify runs automatically)
edr -e src/config.go --old "old" --new "new" -w src/new_test.go --content "package main"
```

## Context-Aware Responses

When a server is running (`edr serve`), edr tracks what you've already seen across CLI calls:

- **Delta reads**: Re-reading a file/symbol returns `{unchanged: true}` if identical. Use `--full` to force full content.
- **Slim edits**: Small diffs (<=20 changed lines) are returned inline. Large diffs are stored; use `edr read --diff file` to retrieve.
- **Body dedup**: Search and explore replace previously-seen bodies with `"[in context]"`.

These optimizations are automatic and server-scoped. They persist for the server's lifetime.
Without a server, each CLI call uses an ephemeral session (no cross-call tracking).

## Key Principles

1. **Start with `edr serve`.** One-time setup. Then use batch flags for everything.
2. **Batch reads + searches** in one call: `edr -r file.go --sig -r other.go:Func -s "pattern"`.
3. **Batch edits + verify** in one call: `edr -e file.go --old "x" --new "y" -e other.go --old "a" --new "b"`. Verify runs automatically.
4. **Use `--sig`** to understand a container's API without reading implementation (75-86% fewer tokens).
5. **Use `--budget`** to control context size. Don't dump entire files.
6. **Use `--inside`** to add fields/methods to a class without reading the file first.
7. **Preview renames:** `edr rename oldName newName --dry-run`.
8. **Check impact before refactoring:** `edr refs Symbol --impact`.
9. **Re-reads are delta** (with server). `{unchanged: true}` saves tokens. Use `--full` to force.
10. **Use `--new -`** with heredoc for large replacement text.

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
- `cmd/serve.go`: Daemon server, socket listener, request/response envelope types
- `cmd/batch_cmd.go`: Batch CLI command (`-r`, `-s`, `-e`, `-w`) and ordered-flag parser
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

**Batch** (primary interface): `edr -r file -s pattern -e file --old x --new y` (or `edr batch ...`).
**Standalone commands**: `edr read`, `edr search`, `edr edit`, `edr write`, `edr map`, `edr find`, `edr refs`, `edr rename`, `edr explore`, `edr verify`, `edr init`.
**Server**: `edr serve` (start), `edr serve --stop` (stop).

All output is structured JSON. File paths are relative to repo root. Edit commands return `hash`.
Symbol reads include both `body` and `content` fields (use either). File reads use `content`.

Sessions are server-scoped (persist across CLI calls while server runs). Without a server, each call uses an ephemeral session.
