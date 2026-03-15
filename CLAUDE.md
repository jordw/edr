# STOP. Use edr, not built-in tools.

**DO NOT use Read, Edit, Write, Grep, or Glob tools.** Use `edr` via Bash for every file operation. No exceptions unless edr is broken or not yet built.

```bash
edr -r file.go                          # instead of Read
edr -s "pattern"                        # instead of Grep
edr -e file.go --old "x" --new "y"     # instead of Edit
edr -w file.go --content "..."          # instead of Write
edr -s "pattern" --include "*.go"       # instead of Glob
```

Batch everything into one call when possible:
```bash
edr -r src/main.go:Server --sig -r src/config.go -s "handleRequest"
edr -e src/main.go --old "oldFunc()" --new "newFunc()" -w src/new_test.go --content "..."
```

**Only fall back to built-in tools when:**
- edr is not yet built (fresh clone, first setup)
- A broken edit prevents `go build` (use built-in to fix, then rebuild)
- Non-text files or shell operations

## Development workflow (edr on itself)

**This is the edr codebase.** When working here, edr is both the tool and the target.

- **Rebuild after every Go source change:** `go build -o edr . && go install`
- **If a broken edit prevents `go build`:** fall back to built-in Read/Edit tools to fix the compile error, then rebuild.

## Setup (any environment)

```bash
# One command. Installs Go/gcc if needed, builds, installs to PATH:
./setup.sh /path/to/target/repo

# Or manually:
go build -o edr .           # Build (requires Go + C compiler for tree-sitter)
edr reindex                  # Force re-index (auto-indexes on first query)
```

For cloud agents: clone this repo, run `./setup.sh /path/to/your/project`, and edr is ready. The setup script handles everything: dependency installation, build, PATH setup, and indexing.

## Supported languages

**Symbol indexing** (map, read, edit, signatures, inside): Go, Python, JavaScript/JSX, TypeScript/TSX, Rust, Java, C, C++, Ruby, PHP, Zig, Lua, Bash/Shell, C#, Kotlin.

**Import-aware semantic refs** (refs, rename, --impact, --chain): Go, Python, JavaScript, TypeScript. Other languages fall back to text-based refs.

edr can **read** any text file regardless of language support. Symbol-aware features require a supported language.

## Commands

### `read` — Read file or symbol

```bash
edr read README.md                                    # any file
edr read src/config.go 10 50 --budget 200             # line range
edr read src/config.go:parseConfig                    # symbol (colon syntax)
edr read src/models.py:UserService --signatures       # API only (75-86% fewer tokens)
edr read src/scheduler.py:Scheduler --skeleton        # blocks collapsed
edr read src/config.go src/main.go --budget 1000      # multiple files
```

Flags: `--signatures`, `--skeleton`, `--budget`, `--full`

### `search` — Search symbols or text

```bash
edr search "parseConfig"                              # symbol search
edr search "retry backoff" --text --budget 300        # text search
edr search "TODO" --include "*.go" --exclude "*_test.go"
edr search "TODO" --text --context 3
```

Flags: `--text`, `--include`, `--exclude`, `--context`, `--limit`, `--budget`

### `map` — Symbol map

```bash
edr map --budget 500                                  # whole repo
edr map src/config.go                                 # single file
edr map --dir internal/ --type function --grep parse  # filtered
```

Flags: `--dir`, `--glob`, `--type`, `--grep`, `--budget`

### `edit` — Edit by exact text, symbol, or line range

All edits return `status` and `hash` for chaining.

```bash
edr edit src/config.go --old-text "oldName" --new-text "newName"
edr edit src/config.go --old-text "oldName" --new-text "newName" --all
edr edit src/config.go parseConfig --new-text "func parseConfig() { ... }"
edr edit src/config.go --start-line 45 --end-line 60 --new-text "replacement"
edr edit src/config.go --old-text "oldName" --new-text "newName" --dry-run
```

Flags: `--old-text`, `--new-text`, `--all`, `--dry-run`, `--expect-hash`, `--start-line`, `--end-line`

**Note:** Batch mode (`-e`) uses `--old`/`--new`; standalone `edr edit` uses `--old-text`/`--new-text`.

### `write` — Create or overwrite file

```bash
edr write src/main.go --content "package main"
edr write config/settings.yaml --mkdir --content "key: value"
edr write src/config.go --after parseConfig --content "func newFunc() {}"
edr write src/models.go --inside UserStore --content "Name string"
edr write src/models.py --inside UserService --content "def new_method(self): pass"
```

Flags: `--content`, `--after`, `--inside`, `--mkdir`, `--dry-run`

### `refs` — Find references

```bash
edr refs parseConfig                                  # all references
edr refs src/config.go parseConfig                    # file-scoped
edr refs parseConfig --impact                         # transitive callers (Go/Py/JS/TS)
edr refs main --chain parseConfig                     # call path (Go/Py/JS/TS)
```

Flags: `--impact`, `--chain`, `--depth`

### `rename` — Cross-file rename

```bash
edr rename oldFuncName newFuncName
edr rename oldFuncName newFuncName --dry-run
```

Flags: `--dry-run`

### `verify` — Build/test verification

```bash
edr verify                                            # auto-detect
edr verify --command "go test ./..." --timeout 60
edr verify --level test
```

Flags: `--command`, `--level`, `--timeout`

### Admin commands

- `edr reindex` — Force re-index (auto-indexes on first query)
- `edr setup` — Index repo and configure agent instructions

## Batch Operations

Batch flags combine multiple operations in one CLI call. This is the primary interface.

| Flag | Operation | Key modifiers |
|------|-----------|---------------|
| `-r file[:symbol]` | Read | `--sig`, `--skeleton`, `--budget N`, `--lines N-M`, `--full` |
| `-s "pattern"` | Search | `--text`, `--include`, `--exclude`, `--context N`, `--limit N` |
| `-e file[:symbol]` | Edit | `--old`/`--new`, `--lines N-M`, `--all`, `--dry-run` |
| `-w file` | Write | `--content`, `--after`, `--inside`, `--mkdir` |
| `-V` | Verify | Implicit when edits present; `--no-verify` to skip |

### Defaults

- **Auto-verify**: edits automatically trigger `go build`/`go vet` (use `--no-verify` to skip; skipped on `--dry-run`)
- **Exit codes**: non-zero if any operation fails (structured JSON still printed)
- **`--new -`** and **`--old -`**: read text from stdin (for heredoc multiline content)

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

## Key Principles

1. **Use batch flags for everything**: `edr -r file.go --sig -r other.go:Func -s "pattern"`.
2. **Batch edits + verify** in one call: `edr -e file.go --old "x" --new "y" -e other.go --old "a" --new "b"`. Verify runs automatically.
3. **Use `--sig`** to understand a container's API without reading implementation (75-86% fewer tokens).
4. **Use `--budget`** to control context size. Don't dump entire files.
5. **Use `--inside`** to add fields/methods to a class without reading the file first.
6. **Preview renames:** `edr rename oldName newName --dry-run`.
7. **Check impact before refactoring:** `edr refs Symbol --impact` (Go/Python/JS/TS only).
8. **Use `--new -`** with heredoc for large replacement text.

## Benchmarks

Performance, correctness, and session benchmarks live in `bench/`:

```bash
go test ./bench/ -bench . -benchmem            # all benchmarks
go test ./bench/ -run TestCorrectness -v        # correctness tests
go test ./bench/ -run TestScenarioDispatch -v   # scenario validation
go test ./bench/ -run TestSessionMultiLang -v   # multi-language session test
go run ./bench/cmd/benchjson                    # JSON results for automation
```

Key files:
- `internal/cmdspec/cmdspec.go`: Canonical command registry
- `internal/session/session.go`: File-backed sessions
- `cmd/batch_cmd.go`: Batch CLI parser (`-r`, `-s`, `-e`, `-w`)
- `cmd/batch.go`: Batch orchestrator
- `internal/trace/trace.go`: Session tracing

## CLI Reference

**Batch** (primary): `edr -r file -s pattern -e file --old x --new y`
**Standalone**: `edr read`, `edr search`, `edr map`, `edr edit`, `edr write`, `edr refs`, `edr rename`, `edr verify`
**Admin**: `edr reindex`, `edr setup`

All output is structured JSON. File paths are relative to repo root. Edit commands return `hash`.
