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

## How it compares

Without edr, agents grep to find code, read line ranges, guess what's relevant, edit, then re-read to check. Each step is a separate tool call with context the agent has to filter.

With edr, `focus file:Symbol` returns the function body plus what it calls. `edit` returns the updated code plus build errors with the broken function included. The agent makes fewer decisions about what to read because edr front-loads the right context.

| Operation | What the agent gets |
|---|---|
| `focus file:Symbol` | Function body + dependency signatures (auto) |
| `edit --old X --new Y` | Diff + updated function body + build result + error context (auto) |
| `orient --dir src/` | Budget-controlled structural overview |
| Batch (`-f ... -f ... -e ...`) | Multiple operations in one call |
| Re-read unchanged file | `{unchanged: true}` (session dedup) |

Proper comparative evals (same tasks, with and without edr, measuring tool calls and success rate) are planned but not yet available.

## License

[MIT](LICENSE)
