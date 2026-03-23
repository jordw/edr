# edr — Development Guide

edr is a CLI tool that replaces built-in agent file operations with context-efficient alternatives. The global instructions (installed by `edr setup`) tell you how to *use* edr; this file tells you how to *develop* it.

## Self-hosting warning

edr is both the tool and the target. If you break the build, you lose your tools. Use `edr checkpoint` before risky changes. If a broken edit prevents `go build`, fall back to built-in Read/Edit tools to fix the compile error, then rebuild.

## Build cycle

```
go build -o edr . && go install    # after every Go source change
go test ./internal/... ./cmd/      # after every change — fix failures before moving on
```

After changing agent instructions (`internal/setup/instructions/*.md`): rebuild, install, `edr setup --force`, then check token budget: `go test ./cmd/ -run TestSpec_InstructionQuality`.

## Architecture

```
CLI args → cmd/ (Cobra) → cmdspec (flag validation) → dispatch (routing) → internal/* (logic) → output (rendering) → session (post-processing)
```

**cmd/**: Cobra command wiring. `commands.go` has standalone commands, `batch.go`/`batch_cmd.go` handle batch mode (`-r`, `-s`, `-e`, `-w`). `commands.go` also builds `edr context` results (`buildNextResult`, `computeCurrentItems`, `computeFixItems`).

**internal/cmdspec/**: Canonical command registry. Every command, flag, category, and alias lives here. CLI flags, dispatch routing, and session behavior all derive from this registry.

**internal/dispatch/**: Command logic. `Dispatch()` routes command names to handlers (`runReadUnified`, `runSmartEdit`, `runSearchUnified`, etc.). This is where args are parsed, the index is queried, and results are constructed.

**internal/index/**: Tree-sitter parsing, SQLite index, symbol extraction, signature extraction. `languages.go:GetLangConfig` maps file extensions to grammar + parse rules. `signatures.go` extracts one-line signatures per language. `parser.go` defines `SymbolInfo`. Ref extraction and import resolution in `refs.go`/`resolve.go`.

**internal/edit/**: Span-based edits with `Transaction` (TOCTOU guard: revalidates file hash before writing). `diff.go` produces unified diffs.

**internal/search/**: Symbol search and text search (ripgrep-style).

**internal/output/**: `plain.go` renders the transport format: JSON header (first line) → raw body → `---` between batch ops → optional `{"verify":...}` trailer. Every command has a `plain*` renderer function.

**internal/session/**: File-backed sessions (`.edr/sessions/<id>.json`). Delta reads (hash-based), body dedup, op log, assumption tracking, build state, checkpoint/restore. `PostProcess()` handles response optimization.

**internal/setup/**: `edr setup` installer. Injects agent instructions into global configs (~/.claude, .cursorrules, etc.). Instructions are in `instructions/*.md`, token-capped at 600.

## Adding a new command

Touch these files (tests will fail if you miss one):

1. `internal/cmdspec/cmdspec.go` — add to `Registry` with name, flags, category
2. `internal/dispatch/dispatch.go` — add case in `Dispatch()`, implement handler
3. `cmd/commands.go` — add Cobra command, register in `init()`, wire flags
4. `internal/output/plain.go` — add `plain*` renderer, add case in `printPlain` switch
5. `internal/cmdspec/validate_test.go` — add to `expected` set in `TestPublicCommandSurface`
6. `cmd/opener_test.go` — add to `tested` map in `TestOpenerAlignment`
7. `cmd/spec_cli_test.go` — add to `expected` list in `TestSpec_HelpSurface`, write spec tests

## Adding a language

1. Vendor grammar C source in `internal/grammars/<lang>/` with Go binding wrapper
2. Add `LangConfig` entry in `internal/index/languages.go:GetLangConfig`
3. Add signature extractor in `internal/index/signatures.go`
4. Add import extractor in `internal/index/imports.go` (for semantic refs) or it falls back to text-based
5. Add test case in `internal/index/languages_test.go:TestAllLanguagesParse`

## Testing

Three kinds:

- **Unit tests** (`internal/*/`): test internal logic directly
- **Spec tests** (`cmd/spec_cli_test.go`): black-box subprocess tests against the transport contract. These build the binary, create temp repos, and validate JSON header + body output. Most important for catching regressions.
- **Benchmarks** (`bench/`): performance + correctness regression tests

```bash
go test ./internal/... ./cmd/             # all unit + spec tests
go test ./cmd/ -run TestSpec -v           # spec tests only
go test ./bench/ -bench . -benchmem       # benchmarks
go test ./bench/ -run TestCorrectness -v  # correctness tests
```

## Setup (for new machines)

```bash
./setup.sh /path/to/target/repo    # installs Go/gcc if needed, builds, indexes
# Or manually:
go build -o edr .                  # requires Go + C compiler for tree-sitter
go install
edr setup /path/to/target/repo     # inject instructions + index
```
