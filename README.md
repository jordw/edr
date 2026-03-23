# edr - faster, more accurate agents

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

**edr makes coding agents faster and more accurate.** It replaces built-in file tools with symbol-aware operations, batched calls, and session tracking — so agents find the right code on the first try, make fewer mistakes, and finish tasks in less time.

- **Precise reads.** Read one function instead of the file around it. Agents see structure, not noise.
- **Fewer round-trips.** Batch reads, searches, edits, and writes into one call. Less orchestration, more progress.
- **No repeated work.** Re-reads return only what changed. Re-runs show only new output. Zero config.

Works with any agent that can run shell commands. Fully local, no telemetry.

## Install

```bash
brew install jordw/tap/edr
edr setup
```

`edr setup` indexes your project, adds `.edr/` to `.gitignore`, and installs agent instructions to your global config (`~/.claude/CLAUDE.md`, `~/.cursor/rules/edr.mdc`, or `~/.codex/AGENTS.md`). Instructions auto-update when edr is rebuilt. They teach the agent to use edr instead of built-in file tools.

<details>
<summary>Other install methods</summary>

**Pre-built binary** ([review the script first](install.sh)):

```bash
curl -fsSL https://raw.githubusercontent.com/jordw/edr/main/install.sh | sh
```

**From source** (requires Go 1.24+ and a C/C++ compiler for tree-sitter):

```bash
CGO_ENABLED=1 go install github.com/jordw/edr@latest
edr setup
```

> `CGO_ENABLED=1` is required. Tree-sitter grammars are C libraries. You need `gcc` and `g++` installed.

**Cloud agents and CI** (installs Go/gcc if needed, builds, indexes):

```bash
git clone https://github.com/jordw/edr.git && ./edr/setup.sh
```

</details>

The index lives in `.edr/` at the repo root and rebuilds automatically if deleted.

## Example

Read a function, find its callers, edit it:

```bash
# Without edr: grep to find it, read the range, grep for callers, read each caller
# 5+ tool calls, ~25KB of context

# With edr: 3 calls, ~3KB
edr -r src/scheduler.py:run               # just the function (not the file)
edr -q refs run --callers                  # callers, import-resolved
edr -e src/scheduler.py \
    --old "def run(self):" \
    --new "def run(self, retries=3):"      # auto re-indexes, verifies build
```

**Batched:** gather everything in one call, mutate in one call:

```bash
# 1. Read three APIs + search, one call
edr -r src/scheduler.py:Scheduler --sig \
    -r src/config.py:parse_config \
    -r src/worker.py:Worker --sig \
    -s "retry"

# 2. Edit two files, auto-verifies build
edr -e src/scheduler.py --old "def run(self):" --new "def run(self, retries=3):" \
    -e src/config.py --old '"timeout": 30' --new '"timeout": 30, "retries": 3'

# 3. Re-run tests - only the diff comes back
edr delta -- pytest
```

## How it works

edr parses your codebase with [tree-sitter](https://tree-sitter.github.io/tree-sitter/) and stores symbols in a SQLite index. This gives agents three capabilities they don't have with raw file tools:

**Symbol-level operations.** Read one function instead of a 400-line file — the agent sees exactly what it needs, makes better decisions, and doesn't get confused by surrounding code. Get a class API with `--signatures` to understand structure before diving in. `--expand` includes dep signatures inline. Find callers with `edr refs --impact` before refactoring. Scope edits to a symbol with `--in Symbol` so they can't match the wrong code. `edr prepare Symbol` returns body, callers, deps, tests, and hash in one call. Edits re-index immediately and auto-verify the build (Go, Node, Rust, Make).

**Batching.** `-r`, `-s`, `-m`, `-q`, `-e`, `-w` combine reads, searches, maps, queries, edits, and writes in one CLI call. One call to gather context, one to apply mutations. Fewer round-trips means faster task completion.

**Sessions.** edr tracks what the agent has already seen and only returns what changed. Second read of an unchanged file: zero output. `edr delta -- make test` after a fix: only the new failures. Same principle for builds, linters, any command. Zero config.

## Commands

**Batch flags** — the primary interface. `-r` read, `-s` search, `-m` map, `-q` query (refs/prepare), `-e` edit, `-w` write. Modifier flags follow each op. Plan what you need, then combine into one call:

```bash
edr -r file[:Symbol]              # Read file or symbol
edr -r file:Class --sig           # Signatures only (no bodies)
edr -s "pattern"                  # Search symbols or text (--text)
edr -m --dir src/                 # Symbol map of a directory
edr -e file --old "x" --new "y"   # Edit with auto re-index + verify
edr -w file --inside Class        # Add method/field without reading
```

Combine freely. One call to gather context, one to mutate:

```bash
edr -r f.go --sig -r g.go:Func --expand -s "pattern" --text -m --dir cmd/
edr -q refs Sym --impact -q prepare f.go:Sym                # batch refs/prepare
edr -r f.go:Sym -e f.go --old "x" --new "y" -r f.go:Sym   # post-edit read
edr -e f.go --old "a" --new "b" -e g.go --old "c" --new "d"
edr -w f.go --content "..." --mkdir
```

### Batch modifiers

**Read** (after `-r`): `--sig`, `--skeleton`, `--full`, `--expand[=deps]`, `--symbols`, `--lines 10:50`, `--budget N`

**Search** (after `-s`): `--text`, `--regex`, `--context N`, `--in f.go:Sym`, `--include "*.go"`, `--limit N`

**Map** (after `-m`): `--dir`, `--lang`, `--grep`, `--glob`, `--type`, `--budget N`

**Query** (after `-q`): `--impact`, `--callers`, `--deps`, `--chain Sym`, `--depth N`, `--signatures`, `--body`, `--budget N`

**Edit** (after `-e`): `--old "x" --new "y"`, `--where Sym` (resolves file), `--in Sym` (scope), `--all`, `--delete`, `--dry-run`, `--fuzzy`, `--read-back`, `@file` for metacharacters

**Write** (after `-w`): `--content "..."`, `--inside Class`, `--after Sym`, `--append`, `--mkdir`

**Verify**: `-V` (auto after edits). `--command "cmd"`, `--level test`

### Standalone commands

| Command | Example |
|---|---|
| `map` | `edr map`, `edr map --dir src/ --type function --lang go --grep pat` |
| `prepare` | `edr prepare Symbol` — body, callers, deps, tests, hash in one call |
| `refs` | `edr refs Symbol`, `--impact`, `--callers`, `--deps`, `--chain target` |
| `rename` | `edr rename Old New --dry-run`, `edr rename --text "old" "new" --word --include "*.go"` |
| `verify` | `edr verify`, `edr verify --test`, `edr verify --command "cmd"` |
| `delta` | `edr delta -- make test` — shows only what changed. `--reset`, `--full` |
| `status` | `edr status`, `edr status --focus "goal"` |
| `undo` | `edr undo` — revert the last edit/write (auto-checkpointed) |
| `reset` | `edr reset`, `--index`, `--session` |
| `setup` | `edr setup`, `edr setup --force` |

Output uses plain mode: one JSON header line followed by raw-text body.

## Languages

edr reads and edits any text file. Symbol-aware features (symbol reads, `--signatures`, `refs`, `map`) require a supported language:

**Symbol indexing:** Go, Python, JavaScript/JSX, TypeScript/TSX, Rust, Java, C, C++, Ruby, PHP, Swift, Scala, Zig, Lua, Bash/Shell, C#, Kotlin

**Import-aware refs:** Go, Python, JavaScript, TypeScript, Java, Kotlin, Scala, C#, PHP, Swift (others fall back to text matching)

## Limitations

- **Tree-sitter, not LSP.** Fast, no build step, works on broken code, zero config. The tradeoff: no type information. Refs use import-path matching, not type resolution, so cross-package references may produce false positives. For agent workloads (read, edit, search) structural parsing is enough.
- **macOS and Linux only.** Windows is not planned.
- **C/C++ compiler required** when building from source (tree-sitter grammars). Homebrew and the install script use pre-built binaries.
- **First index: under 1s** on small repos, ~15s on large ones (vitess, 3200 files). Incremental re-index after edits: ~12ms/file.

## Benchmarks

9 scenarios (read a symbol, find refs, orient in codebase, edit a function, etc.) against real repos. We measure tool response bytes — fewer bytes means less noise for the model to reason over, faster responses, and more accurate decisions.

The baseline models a skilled agent using Claude Code's built-in tools: `Grep` to find symbols before reading, `Read` with line ranges around grep matches (not whole files), `Edit`/`Write` confirmations. edr uses symbol reads, `--signatures`, `refs`, `map`, and batch flags.

| Repo | Lang | Files | Baseline | edr | Reduction |
|---|---|---|---|---|---|
| [urfave/cli](https://github.com/urfave/cli) | Go | ~70 | 146KB / 25 calls | 27KB / 9 calls | **82%** |
| [vitess/sqlparser](https://github.com/vitessio/vitess) | Go | ~70 | 459KB / 22 calls | 29KB / 9 calls | **94%** |
| [vitess/vtgate](https://github.com/vitessio/vitess) | Go | ~490 | 433KB / 24 calls | 40KB / 9 calls | **91%** |
| [pallets/click](https://github.com/pallets/click) | Python | ~17 | 180KB / 25 calls | 30KB / 9 calls | **83%** |
| [rails/thor](https://github.com/rails/thor) | Ruby | ~35 | 157KB / 25 calls | 30KB / 9 calls | **81%** |
| [reduxjs/redux-toolkit](https://github.com/reduxjs/redux-toolkit) | TS | ~190 | 112KB / 25 calls | 26KB / 9 calls | **77%** |
| [django/django](https://github.com/django/django) | Python | ~880 | 1027KB / 25 calls | 40KB / 9 calls | **96%** |

Median reduction: **83%**. edr loses on plain text search (structured JSON adds overhead vs raw grep), but wins everywhere else. Biggest gains on structured operations (refs, map, signatures). Call counts are summed across all 9 scenarios; each edr scenario is 1 call.

<details>
<summary>Per-scenario breakdown (urfave/cli)</summary>

| Scenario | Baseline | edr | Reduction |
|---|---|---|---|
| Understand a class API | 13,019B (whole file) | 1,486B (`--signatures`) | **89%** |
| Read a specific function | 3,026B / 2 calls (grep + range read) | 1,182B (symbol read) | **61%** |
| Find references | 9,086B / 4 calls (grep + 3 range reads) | 179B (`refs`) | **98%** |
| Search with context | 614B (grep -C3) | 1,027B (structured) | **-67%** |
| Orient in codebase | 52,470B / 4 calls (glob + 3 reads) | 393B (`map`) | **99%** |
| Edit a function | 1,403B / 3 calls (grep + range + edit) | 394B (batch) | **72%** |
| Add method to a class | 5,393B / 3 calls (grep + range + write) | 249B (`--inside`) | **95%** |
| Multi-file read | 39,397B / 3 calls | 22,028B (batched) | **44%** |
| Explore a symbol | 25,006B / 4 calls (grep + 3 range reads) | 555B | **98%** |
| **Total** | **149,414B / 25 calls** | **27,493B / 9 calls** | **82%** |

</details>

Scenarios and methodology in [`bench/scenarios/`](bench/scenarios/). Reproduce: `bash bench/run_real_repo_benchmarks.sh` (~10 min). Regenerate tables: `bash bench/gen_readme_table.sh`.

## License

[MIT](LICENSE)
