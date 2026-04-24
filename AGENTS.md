# edr — Development Guide

edr is a CLI tool that replaces built-in agent file operations with context-efficient alternatives. The global instructions (installed by `edr setup`) tell you how to *use* edr; this file tells you how to *develop* it.

## Self-hosting warning

edr is both the tool and the target. If you break the build, you lose your tools. Every edit/write is auto-checkpointed; use `edr undo` to revert. If a broken edit prevents `go build`, fall back to standard shell tools (`cat`, `sed`) to fix the compile error, then rebuild.

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

**cmd/**: Cobra command wiring. `commands.go` has standalone commands, `batch.go`/`batch_cmd.go` handle batch mode (`-f`, `-o`, `-s`, `-e`, `-w`).

**internal/cmdspec/**: Canonical command registry. Every command, flag, category, and alias lives here. CLI flags, dispatch routing, and session behavior all derive from this registry.

**internal/dispatch/**: Command logic. `Dispatch()` routes command names to handlers (`runReadUnified`, `runSmartEdit`, `runSearchUnified`, etc.). This is where args are parsed, symbols are resolved, and results are constructed.

**internal/index/**: Pure-Go, lexer-based symbol parsing; on-demand symbol store; signature extraction. `store.go` defines the `SymbolStore` interface. `ondemand.go` implements it by parsing files on demand and consulting the optional trigram/symbol index when available. `langs.go` maps file extensions to parser IDs. `parse_*.go` files implement language parsers. `symbol.go` defines `SymbolInfo`. Import extraction and resolution support cross-file search/refactor heuristics.

**internal/idx/**: Optional persistent trigram, symbol, import, and reference indexes. `edr index` builds them; normal commands use them opportunistically and stat-check dirty files so reads still see current content.

**internal/scope/**: Scope and binding builders used for rename safety and `refs-to`. Mutating scope-aware operations are admitted per language in dispatch, with name-match fallback where the scope builder is not mature enough.

**internal/edit/**: Span-based edits with `Transaction` (TOCTOU guard: revalidates file hash before writing). `diff.go` produces unified diffs.

**internal/search/**: Symbol search and text search (ripgrep-style).

**internal/output/**: `plain.go` renders the transport format: JSON header (first line) → raw body → `---` between batch ops → optional `{"verify":...}` trailer. Every command has a `plain*` renderer function.

**internal/session/**: File-backed sessions under the per-repo edr data directory. Delta reads (hash-based), body dedup, op log, assumption tracking, build state, checkpoint/restore. `PostProcess()` handles response optimization.

**internal/setup/**: `edr setup` installer. Injects agent instructions into global configs (`~/.claude/CLAUDE.md`, `~/.codex/AGENTS.md`, `~/.cursor/rules/edr.mdc`). Instructions are in `instructions/*.md`, token-capped by spec tests.

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

1. Add the extension mapping in `internal/index/langs.go`
2. Add a lexer-based parser in `internal/index/parse_<lang>.go`
3. Add adapters in `internal/index/handwritten_adapt.go` for symbols and imports
4. Add a scope builder in `internal/scope/<lang>/` only if rename or `refs-to` needs binding-aware behavior
5. Add or update parser, language, spec, and rename/ref tests before admitting the language for mutating scope-aware operations

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
./setup.sh /path/to/target/repo    # installs Go if needed, builds
# Or manually:
go build -o edr .                  # requires Go 1.24+
go install
edr setup /path/to/target/repo     # inject agent instructions
```
