# edr - faster, more accurate agents

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

**edr makes coding agents faster and more accurate.** It replaces built-in file tools with symbol-aware operations, batched calls, and session tracking — so agents find the right code on the first try, make fewer mistakes, and finish tasks in less time.

- **Precise reads.** Focus on one function instead of the file around it. Agents see structure, not noise.
- **Fewer round-trips.** Batch focus, orient, and edit into one call. Less orchestration, more progress.
- **No repeated work.** Re-reads return only what changed. Zero config.

Works with any agent that can run shell commands. Fully local, no telemetry.

## Install

```bash
brew install jordw/tap/edr
edr setup
```

`edr setup` installs agent instructions to your global config (`~/.claude/CLAUDE.md`, `~/.cursor/rules/edr.mdc`, or `~/.codex/AGENTS.md`). Instructions auto-update when edr is rebuilt. They teach the agent to use edr instead of built-in file tools. Session data is stored in `~/.edr/repos/`, not in the project directory.

<details>
<summary>Other install methods</summary>

**Pre-built binary** ([review the script first](install.sh)):

```bash
curl -fsSL https://raw.githubusercontent.com/jordw/edr/main/install.sh | sh
```

**From source** (requires Go 1.24+):

```bash
go install github.com/jordw/edr@latest
edr setup
```

**Cloud agents and CI** (installs Go if needed, builds):

```bash
git clone https://github.com/jordw/edr.git && ./edr/setup.sh
```

</details>

## Example

Focus on a function, orient in the project, edit it:

```bash
# Without edr: grep to find it, read the range, grep for callers, read each caller
# 5+ tool calls, ~25KB of context

# With edr: 3 calls, ~3KB
edr focus src/scheduler.py:run             # just the function (not the file)
edr orient --dir src/                      # structural overview
edr edit src/scheduler.py \
    --old "def run(self):" \
    --new "def run(self, retries=3):"      # auto-verifies build
```

**Batched:** gather everything in one call, mutate in one call:

```bash
# 1. Focus on three APIs + orient, one call
edr -f src/scheduler.py:Scheduler --sig \
    -f src/config.py:parse_config \
    -f src/worker.py:Worker --sig \
    -o --dir src/

# 2. Edit two files, auto-verifies build
edr -e src/scheduler.py --old "def run(self):" --new "def run(self, retries=3):" \
    -e src/config.py --old '"timeout": 30' --new '"timeout": 30, "retries": 3'
```

## How it works

edr parses files on demand with pure-Go regex-based symbol extraction — no pre-built index, no setup step, no staleness, no CGO dependency. This gives agents three capabilities they don't have with raw file tools:

**Symbol-level operations.** Focus on one function instead of a 400-line file — the agent sees exactly what it needs, makes better decisions, and doesn't get confused by surrounding code. Get a class API with `--signatures` to understand structure before diving in. `--expand` includes dep signatures inline. Scope edits to a symbol with `--in Symbol` so they can't match the wrong code. Edits auto-verify the build (Go, Node, Rust, Make).

**Batching.** `-f`, `-o`, `-e` combine focus, orient, and edit in one CLI call. One call to gather context, one to apply mutations. Fewer round-trips means faster task completion.

**Sessions.** edr tracks what the agent has already seen and only returns what changed. Second read of an unchanged file: zero output. Zero config.

## Commands

### Primary commands

| Command | Description |
|---|---|
| `orient [path]` | Structural overview of a directory or project (replaces `map`) |
| `focus file[:Symbol]` | Read file or symbol with context (replaces `read`) |
| `edit file` | Edit, write, create files + auto-verify (absorbs `write`) |
| `status` | Session state, build state |
| `undo` | Revert last edit/write (auto-checkpointed) |
| `setup` | Install agent instructions |

Old command names (`map`, `read`, `write`, `search`, `rename`, `verify`, `reset`) still work for backward compatibility.

### Batch flags

`-f` focus, `-o` orient, `-e` edit. Modifier flags follow each op. Plan what you need, then combine into one call:

```bash
edr -f file[:Symbol]              # Focus on file or symbol
edr -f file:Class --sig           # Signatures only (no bodies)
edr -o --dir src/                 # Structural overview of a directory
edr -e file --old "x" --new "y"   # Edit with auto-verify
edr -e file --content "..." --inside Class  # Write (via edit)
```

Combine freely. One call to gather context, one to mutate:

```bash
edr -f f.go --sig -f g.go:Func --expand -o --dir cmd/
edr -f f.go:Sym -e f.go --old "x" --new "y" -f f.go:Sym   # post-edit read
edr -e f.go --old "a" --new "b" -e g.go --old "c" --new "d"
edr -e f.go --content "..." --mkdir
```

Aliases `-r` (focus), `-m` (orient), `-s` (search), `-w` (write) still work.

### Batch modifiers

**Focus** (after `-f`): `--sig`, `--skeleton`, `--full`, `--expand[=deps]`, `--symbols`, `--lines 10:50`, `--budget N`

**Orient** (after `-o`): `--dir`, `--lang`, `--grep`, `--glob`, `--type`, `--budget N`

**Edit** (after `-e`): `--old "x" --new "y"`, `--where Sym` (resolves file), `--in Sym` (scope), `--content "..."`, `--inside Class`, `--after Sym`, `--append`, `--mkdir`, `--all`, `--delete`, `--dry-run`, `--fuzzy`, `--read-back`, `--no-verify`, `@file` for metacharacters

Output uses plain mode: one JSON header line followed by raw-text body.

## Languages

edr reads and edits any text file. Symbol-aware features (symbol reads, `--signatures`, `map`) require a supported language:

**Symbol parsing:** Go, Python, JavaScript/JSX, TypeScript/TSX, Rust, Java, C, C++, Ruby

## Limitations

- **Regex-based, not LSP.** Fast, no build step, works on broken code, zero config. The tradeoff: no type information, no AST. Symbol extraction uses regex patterns tuned per language. Accurate for common patterns; may miss exotic syntax.
- **macOS and Linux only.** Windows is not planned.
- **Pure Go.** No CGO, no C compiler needed. Single ~6MB binary.

## Benchmarks

9 scenarios (read a symbol, orient in codebase, edit a function, etc.) against real repos. We measure tool response bytes — fewer bytes means less noise for the model to reason over, faster responses, and more accurate decisions.

The baseline models a skilled agent using Claude Code's built-in tools: `Grep` to find symbols before reading, `Read` with line ranges around grep matches (not whole files), `Edit`/`Write` confirmations. edr uses `focus` (symbol reads), `--signatures`, `orient`, and batch flags.

| Repo | Lang | Files | Baseline | edr | Reduction |
|---|---|---|---|---|---|
| [urfave/cli](https://github.com/urfave/cli) | Go | ~70 | 146KB / 25 calls | 27KB / 9 calls | **82%** |
| [vitess/sqlparser](https://github.com/vitessio/vitess) | Go | ~70 | 459KB / 22 calls | 29KB / 9 calls | **94%** |
| [vitess/vtgate](https://github.com/vitessio/vitess) | Go | ~490 | 433KB / 24 calls | 40KB / 9 calls | **91%** |
| [pallets/click](https://github.com/pallets/click) | Python | ~17 | 180KB / 25 calls | 30KB / 9 calls | **83%** |
| [rails/thor](https://github.com/rails/thor) | Ruby | ~35 | 157KB / 25 calls | 30KB / 9 calls | **81%** |
| [reduxjs/redux-toolkit](https://github.com/reduxjs/redux-toolkit) | TS | ~190 | 112KB / 25 calls | 26KB / 9 calls | **77%** |
| [django/django](https://github.com/django/django) | Python | ~880 | 1027KB / 25 calls | 40KB / 9 calls | **96%** |

Median reduction: **83%**. edr loses on plain text search (structured JSON adds overhead vs raw grep), but wins everywhere else. Biggest gains on structured operations (map, signatures). Call counts are summed across all 9 scenarios; each edr scenario is 1 call.

<details>
<summary>Per-scenario breakdown (urfave/cli)</summary>

| Scenario | Baseline | edr | Reduction |
|---|---|---|---|
| Understand a class API | 13,019B (whole file) | 1,486B (`--signatures`) | **89%** |
| Read a specific function | 3,026B / 2 calls (grep + range read) | 1,182B (symbol read) | **61%** |
| Find references | 9,086B / 4 calls (grep + 3 range reads) | 179B (search) | **98%** |
| Search with context | 614B (grep -C3) | 1,027B (structured) | **-67%** |
| Orient in codebase | 52,470B / 4 calls (glob + 3 reads) | 393B (`map`) | **99%** |
| Edit a function | 1,403B / 3 calls (grep + range + edit) | 394B (batch) | **72%** |
| Add method to a class | 5,393B / 3 calls (grep + range + write) | 249B (`--inside`) | **95%** |
| Multi-file read | 39,397B / 3 calls | 22,028B (batched) | **44%** |
| Explore a symbol | 25,006B / 4 calls (grep + 3 range reads) | 555B (`--expand`) | **98%** |
| **Total** | **149,414B / 25 calls** | **27,493B / 9 calls** | **82%** |

</details>

Scenarios and methodology in [`bench/scenarios/`](bench/scenarios/). Reproduce: `bash bench/run_real_repo_benchmarks.sh` (~10 min). Regenerate tables: `bash bench/gen_readme_table.sh`.

## License

[MIT](LICENSE)
