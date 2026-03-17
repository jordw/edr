# edr

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

**Coding agents waste most of their context window reading entire files to find one function.** edr gives agents symbol-level file operations and batched calls so they read less and stay under budget longer.

Works with any agent that can run shell commands. Fully local, no telemetry.

## Example

Add a `retries` parameter to a scheduler class. Without edr, agents read whole files to find individual symbols — 6 calls, ~59KB of context. With edr:

```bash
# Read three symbols + search, one call
edr -r src/scheduler.py:Scheduler --sig \
    -r src/config.py:parse_config \
    -r src/worker.py:Worker --sig \
    -s "retry"

# Edit two files, auto-verifies build
edr -e src/scheduler.py --old "def run(self):" --new "def run(self, retries=3):" \
    -e src/config.py --old '"timeout": 30' --new '"timeout": 30, "retries": 3'
```
2 calls, ~8KB of context.

## Benchmarks

We run 9 scenarios (read a symbol, find refs, orient in codebase, edit a function, etc.) against real repos and measure tool response bytes — the raw amount of text that enters the agent's context window.

The baseline uses the tools agents actually have: whole-file `cat`, `grep -rn`, `find` + read. No symbol extraction, no batching. edr uses symbol reads, `--signatures`, `refs`, `map`, and batch flags.

| Repo | Lang | Files | Baseline | edr | Reduction |
|---|---|---|---|---|---|
| [urfave/cli](https://github.com/urfave/cli) | Go | ~70 | 197KB / 20 calls | 16KB / 9 calls | **92%** |
| [vitess/sqlparser](https://github.com/vitessio/vitess) | Go | ~70 | 322KB / 17 calls | 16KB / 9 calls | **95%** |
| [vitess/vtgate](https://github.com/vitessio/vitess) | Go | ~490 | 747KB / 19 calls | 33KB / 9 calls | **96%** |
| [pallets/click](https://github.com/pallets/click) | Python | ~17 | 297KB / 20 calls | 19KB / 9 calls | **93%** |
| [rails/thor](https://github.com/rails/thor) | Ruby | ~35 | 170KB / 20 calls | 16KB / 9 calls | **91%** |
| [reduxjs/redux-toolkit](https://github.com/reduxjs/redux-toolkit) | TS | ~190 | 186KB / 20 calls | 23KB / 9 calls | **88%** |
| [django/django](https://github.com/django/django) | Python | ~880 | 1,328KB / 20 calls | 30KB / 9 calls | **98%** |

Median reduction: **93%** across repos. edr loses on plain text search (structured JSON adds overhead vs raw grep — see breakdown below), but wins everywhere else. Call counts are summed across all 9 scenarios; each edr scenario is 1 call.

<details>
<summary>Per-scenario breakdown (urfave/cli)</summary>

| Scenario | Baseline | edr | Reduction |
|---|---|---|---|
| Understand a class API | 10,072B (whole file) | 1,592B (`--signatures`) | **84%** |
| Read a specific function | 15,307B (whole file) | 1,463B (symbol read) | **90%** |
| Find references | 67,101B / 4 calls | 865B / 1 call (`refs`) | **99%** |
| Search with context | 614B (grep -C3) | 1,812B (structured) | **-195%** |
| Orient in codebase | 11,481B / 2 calls | 2,235B / 1 call (`map`) | **81%** |
| Edit a function | 10,172B / 2 calls | 680B / 1 call (batch) | **93%** |
| Add method to a class | 10,072B / 2 calls | 184B / 1 call (`--inside`) | **98%** |
| Multi-file read | 30,794B / 3 calls | 2,606B / 1 call (batched) | **92%** |
| Explore a symbol | 41,285B / 4 calls | 4,562B / 1 call | **89%** |
| **Total** | **196,898B / 20 calls** | **15,999B / 9 calls** | **92%** |

</details>

Scenarios and methodology in [`bench/scenarios/`](bench/scenarios/). Reproduce: `bash bench/run_real_repo_benchmarks.sh` (~10 min).

## Install

```bash
brew install jordw/tap/edr
edr setup .
```

`edr setup` indexes your project, adds `.edr/` to `.gitignore`, and appends agent instructions (~2KB) to your config (`CLAUDE.md`, `.cursorrules`, or `AGENTS.md`). Existing content is preserved. The instructions teach the agent to use edr instead of built-in file tools.

<details>
<summary>Other install methods</summary>

**Pre-built binary** ([review the script first](install.sh)):

```bash
curl -fsSL https://raw.githubusercontent.com/jordw/edr/main/install.sh | sh
```

**From source** (requires Go 1.25+ and a C/C++ compiler for tree-sitter):

```bash
CGO_ENABLED=1 go install github.com/jordw/edr@latest
edr setup /path/to/your/project
```

> `CGO_ENABLED=1` is required — tree-sitter grammars are C libraries. You need `gcc` and `g++` installed.

**Cloud agents and CI** (installs Go/gcc if needed, builds, indexes):

```bash
git clone https://github.com/jordw/edr.git && ./edr/setup.sh
```

</details>

The index lives in `.edr/` at the repo root and rebuilds automatically if deleted.

## How it works

edr parses your codebase with [tree-sitter](https://tree-sitter.github.io/tree-sitter/) and stores symbols in a SQLite index. This gives agents three capabilities they don't have with raw file tools:

**Symbol-level operations.** Read one function instead of a 400-line file. Get a class API with `--signatures` (85% fewer tokens). Add a method with `--inside ClassName` without reading the file. Edits re-index immediately and auto-verify the build.

**Batching.** `-r`, `-s`, `-e`, `-w` combine reads, searches, edits, and writes in one CLI call. One call to gather context, one to apply mutations.

**Sessions.** Set `EDR_SESSION` to a stable ID to enable cross-call dedup. Re-reading an unchanged file returns `{unchanged: true}`. Search results replace already-seen bodies with `[in context]`. Without a session, each call is independent.

## Commands

**Batch flags** — the primary interface:
```bash
edr -r file[:Symbol]              # Read file or symbol
edr -r file:Class --sig           # Signatures only (no bodies)
edr -s "pattern"                  # Search symbols or text (--text)
edr -e file --old "x" --new "y"   # Edit with auto re-index + verify
edr -w file --inside Class        # Add method/field without reading
```

Combine freely — one call to gather, one to mutate:
```bash
edr -r src/config.go:parseConfig --sig -r src/main.go:Server -s "handleRequest"
edr -e src/config.go --old "old" --new "new" -w src/new_test.go --content "..."
```

**Standalone commands:**

| Command | Example |
|---|---|
| `read` | `edr read file:Symbol`, `edr read file:Class --signatures` |
| `search` | `edr search "pattern" --text`, `edr search "pattern" --regex` |
| `map` | `edr map`, `edr map --dir src/ --type function --lang go` |
| `edit` | `edr edit file --old-text "x" --new-text "y"` |
| `write` | `edr write file --inside Class --content "..."`, `--append` |
| `refs` | `edr refs Symbol`, `edr refs Symbol --impact` |
| `rename` | `edr rename old new --dry-run` |
| `verify` | `edr verify`, `edr verify --level test` |
| `setup` | `edr setup .`, `edr setup --claude --force` |
| `reindex` | `edr reindex` |

All output is structured JSON.

## Languages

edr reads and edits any text file. Symbol-aware features (symbol reads, `--signatures`, `refs`, `map`) require a supported language:

**Symbol indexing:** Go, Python, JavaScript/JSX, TypeScript/TSX, Rust, Java, C, C++, Ruby, PHP, Zig, Lua, Bash/Shell, C#, Kotlin

**Import-aware refs:** Go, Python, JavaScript, TypeScript (others fall back to text matching)

## Limitations

- **Tree-sitter, not LSP.** Fast, no build step, works on broken code, zero config. The tradeoff: no type information. Refs use import-path matching, not type resolution, so cross-package references may produce false positives. For agent workloads — read, edit, search — structural parsing is enough.
- **macOS and Linux only.** Windows is not planned.
- **C/C++ compiler required** when building from source (tree-sitter grammars). Homebrew and the install script use pre-built binaries.
- **First index: 1-3s** on small repos, ~30s on large ones (vitess, 1.5M LOC). Incremental re-index after edits: ~50ms/file. Subsequent reads/searches add ~5-20ms of overhead vs raw `cat`/`grep` for tree-sitter parsing and SQLite lookup.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Bug reports and PRs welcome on [GitHub](https://github.com/jordw/edr/issues).

## License

[MIT](LICENSE)
