# edr — Development Guide

This is the edr codebase. edr is a CLI tool that replaces built-in agent file operations (Read, Edit, Write, Grep, Glob) with context-efficient alternatives.

## Development workflow

edr is both the tool and the target when working here.

- **Rebuild after every Go source change:** `go build -o edr . && go install`
- **If a broken edit prevents `go build`:** fall back to built-in Read/Edit tools to fix the compile error, then rebuild.

## Setup

```bash
# One command. Installs Go/gcc if needed, builds, installs to PATH:
./setup.sh /path/to/target/repo

# Or manually:
go build -o edr .           # Build (requires Go + C compiler for tree-sitter)
edr reindex                  # Force re-index (auto-indexes on first query)
```

For cloud agents: clone this repo, run `./setup.sh /path/to/your/project`, and edr is ready.

## Supported languages

**Symbol indexing** (map, read, edit, signatures, inside): Go, Python, JavaScript/JSX, TypeScript/TSX, Rust, Java, C, C++, Ruby, PHP, Zig, Lua, Bash/Shell, C#, Kotlin.

**Import-aware semantic refs** (refs, rename, --impact, --chain): Go, Python, JavaScript, TypeScript. Other languages fall back to text-based refs.

## Key files

- `internal/cmdspec/cmdspec.go`: Canonical command registry
- `internal/session/session.go`: File-backed sessions
- `cmd/batch_cmd.go`: Batch CLI parser (`-r`, `-s`, `-e`, `-w`)
- `cmd/batch.go`: Batch orchestrator
- `internal/trace/trace.go`: Session tracing
- `internal/setup/`: Global install, instruction injection, auto-update

## Benchmarks

```bash
go test ./bench/ -bench . -benchmem            # all benchmarks
go test ./bench/ -run TestCorrectness -v        # correctness tests
go test ./bench/ -run TestScenarioDispatch -v   # scenario validation
go test ./bench/ -run TestSessionMultiLang -v   # multi-language session test
go run ./bench/cmd/benchjson                    # JSON results for automation
```

## CLI surface

**Batch** (primary): `edr -r file -s pattern -e file --old x --new y`
**Standalone**: `edr read`, `edr search`, `edr map`, `edr edit`, `edr write`, `edr refs`, `edr rename`, `edr verify`
**Admin**: `edr reindex`, `edr setup`

All output is structured JSON. File paths are relative to repo root. Edit commands return `hash`.
