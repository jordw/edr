# edr — Development Guide

edr is a CLI tool that replaces built-in agent file operations with context-efficient alternatives. The global instructions (installed by `edr setup`) tell you how to *use* edr; this file tells you how to *develop* it.

## Self-hosting: use the homebrew binary

Use `/opt/homebrew/bin/edr` (the installed release) for all file operations while developing. This survives build breakage — if `go build` fails, edr still works. Only use `go install` when testing features you just built.

```
go build -o edr . && go install    # after every Go source change
go test ./internal/... ./cmd/      # after every change — fix failures before moving on
```

After changing agent instructions (`internal/setup/instructions/*.md`): rebuild, install, `edr setup --force`, then check token budget: `go test ./cmd/ -run TestSpec_InstructionQuality`.

## Architecture

```
CLI args → cmd/ (Cobra) → cmdspec (flag validation) → dispatch (routing) → internal/* (logic) → output (rendering) → session (post-processing)
```

**cmd/**: Cobra command wiring. `commands.go` has standalone commands, `batch.go`/`batch_cmd.go` handle batch mode (`-f` focus, `-o` orient, `-e` edit; aliases `-r`, `-s`, `-m`, `-w` still work). `commands.go` also builds `edr status` results (`buildNextResult`, `computeCurrentItems`, `computeFixItems`).

**internal/cmdspec/**: Canonical command registry. Every command, flag, category, and alias lives here. CLI flags, dispatch routing, and session behavior all derive from this registry.

**internal/dispatch/**: Command logic. `Dispatch()` routes command names to handlers (`runReadUnified`, `runSmartEdit`, `runSearchUnified`, etc.). This is where args are parsed, symbols are resolved, and results are constructed.

**internal/index/**: Pure-Go regex-based symbol extraction, on-demand symbol store, signature extraction. `store.go` defines the `SymbolStore` interface. `ondemand.go` implements it by parsing files on demand (default). `regex.go` contains regex patterns for symbol extraction. `languages.go:GetLangConfig` maps file extensions to parse rules. `signatures.go` extracts one-line signatures per language. `parser.go` defines `SymbolInfo`.

**internal/edit/**: Span-based edits with `Transaction` (TOCTOU guard: revalidates file hash before writing). `diff.go` produces unified diffs.

**internal/idx/**: Trigram index — binary format, build/query/staleness, `ReadHeader` for fast 44-byte checks. `format.go` has Marshal/Unmarshal, `index.go` has BuildFullFromWalk/Query/IsComplete.

**internal/output/**: `plain.go` renders the transport format: JSON header (first line) → raw body → `---` between batch ops → optional `{"verify":...}` trailer. Every command has a `plain*` renderer function.

**internal/session/**: File-backed sessions (`~/.edr/repos/<key>/sessions/<id>.json`). Delta reads (hash-based), body dedup, op log, assumption tracking, build state, checkpoint/restore. `PostProcess()` handles response optimization.

**internal/setup/**: `edr setup` installer. Injects agent instructions into global configs (~/.claude, .cursorrules, etc.). Instructions are in `instructions/*.md`, token-capped at 850.

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

1. Add regex patterns for the language in `internal/index/regex.go`
2. Add `LangConfig` entry in `internal/index/languages.go:GetLangConfig`
3. Add signature extractor in `internal/index/signatures.go`
4. Add test case in `internal/index/languages_test.go:TestAllLanguagesParse`

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
go build -o edr .                  # pure Go, no C compiler needed
go install
edr setup /path/to/target/repo     # inject agent instructions
```

Test corpus (for scope dogfood + eval harness):

```bash
./scripts/eval/setup.sh            # clones ~30 GB into $REPO_BASE (default: parent of edr)
./scripts/eval/setup.sh --skip-index      # clone-only
./scripts/eval/setup.sh --repo pytorch    # single repo
```

Dogfood a single repo once cloned:

```bash
EDR_SCOPE_DOGFOOD_DIR=/Users/jordw/Documents/GitHub/pytorch \
  go test ./internal/scope/store/ -run TestDogfood_ImportGraph_AllLanguages -v
```
