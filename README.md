# edr

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

**Agents are bottlenecked on what context they have and can quickly gather.** Larger context windows and faster inference don't help when the agent is reading whole files to find one function, making sequential calls that could be batched, and re-processing output it's already seen.

edr gives agents the right context instead of all the context:

- **Symbol-level ops** — read one function, not the whole file.
- **Batching** — three reads and a search in one call, not four.
- **Sessions** — edr tracks what the agent has seen. Re-reads and re-runs return only what changed.

Works with any agent that can run shell commands. Fully local, no telemetry.

## Why less context = faster agents

Model speed keeps improving, but agents still bottleneck on the same thing: **how much text they have to process per step**.

An agent that `cat`s a 400-line file to read one function just added 15KB to its context. Multiply by every read, search, and test run in a task. The model has to attend over all of it — prefill cost is quadratic in context length, and every output token generated is slower because it attends over a larger KV cache. Trimming a few milliseconds off inference doesn't help when the input is 10× larger than it needs to be.

edr attacks this directly:
- **Fewer tokens in** → faster responses. Read a function (1KB) instead of a file (15KB). The model processes 15× less input.
- **Fewer round-trips** → less wall-clock time. Batch three reads into one call instead of three sequential ones. Each round-trip has overhead — parsing, scheduling, output rendering — that adds up fast.
- **Sessions eliminate redundancy** → edr tracks what the agent has seen. Re-read a file after an edit? Only the changed lines come back. Re-run tests? Just the diff. The agent stays focused on what actually changed.

**95% median context reduction** across real repos. That translates directly to faster responses — less prefill, smaller KV cache, fewer round-trips — and the agent stays coherent longer because it isn't burning context on files it doesn't need.

This scales. A 3,000-file monorepo gets the same tight reads and batched edits as a 50-file project.

## Example

Add a `retries` parameter to a scheduler class. Without edr: 6 calls, ~59KB of context. With edr:

```bash
# 1. Symbol-level: read three signatures + search, one call
edr -r src/scheduler.py:Scheduler --sig \
    -r src/config.py:parse_config \
    -r src/worker.py:Worker --sig \
    -s "retry"

# 2. Batched edit: two files, auto-verifies build
edr -e src/scheduler.py --old "def run(self):" --new "def run(self, retries=3):" \
    -e src/config.py --old '"timeout": 30' --new '"timeout": 30, "retries": 3'

# 3. Sessions: run tests, fix a bug, run again — only the diff comes back
edr delta -- pytest
# → [no changes, 80 lines]  or just the diff of what changed
```
3 calls. Re-reads of files already in context: zero tokens.

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

> `CGO_ENABLED=1` is required — tree-sitter grammars are C libraries. You need `gcc` and `g++` installed.

**Cloud agents and CI** (installs Go/gcc if needed, builds, indexes):

```bash
git clone https://github.com/jordw/edr.git && ./edr/setup.sh
```

</details>

The index lives in `.edr/` at the repo root and rebuilds automatically if deleted.

## How it works

edr parses your codebase with [tree-sitter](https://tree-sitter.github.io/tree-sitter/) and stores symbols in a SQLite index. This gives agents three capabilities they don't have with raw file tools:

**Symbol-level operations.** Read one function instead of a 400-line file. Get a class API with `--signatures` (85% fewer tokens). Add a method with `--inside ClassName` without reading the file. Scope edits to a symbol with `--in Symbol` to avoid false matches. Use `--fuzzy` for whitespace-tolerant matching. Edits re-index immediately and auto-verify the build (Go, Node, Rust, Make).

**Batching.** `-r`, `-s`, `-e`, `-w` combine reads, searches, edits, and writes in one CLI call. One call to gather context, one to apply mutations.

**Sessions.** edr tracks what the agent has already seen — files, symbols, search results, command output — and only shows what changed. Second read of an unchanged file: 0 tokens. `edr delta -- make test` after a fix: only the diff from the previous run, unchanged lines collapsed. Same principle for builds, linters, any command. Zero config — sessions activate automatically.

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
| `read` | `edr read file:Symbol`, `--signatures`, `--lines 10:50` |
| `search` | `edr search "pattern" --text`, `--in file:Symbol`, `--regex` |
| `map` | `edr map`, `edr map --dir src/ --type function --lang go --grep pat` |
| `edit` | `edr edit file --old "x" --new "y"`, `--fuzzy`, `--in Symbol`, `--delete` |
| `write` | `edr write file --inside Class --content "..."`, `--after Symbol`, `--append` |
| `refs` | `edr refs Symbol`, `--impact`, `--callers`, `--deps`, `--chain target` |
| `rename` | `edr rename old new --dry-run` |
| `verify` | `edr verify`, `edr verify --level test` |
| `delta` | `edr delta -- make test` — shows only what changed |
| `context` | `edr context`, `edr context --focus "goal"` |
| `checkpoint` | `edr checkpoint`, `--restore cp_1`, `--list`, `--diff cp_1` |
| `reset` | `edr reset`, `--index`, `--session` |
| `setup` | `edr setup`, `edr setup --force` |

Output uses plain mode: one JSON header line followed by raw-text body.

## Languages

edr reads and edits any text file. Symbol-aware features (symbol reads, `--signatures`, `refs`, `map`) require a supported language:

**Symbol indexing:** Go, Python, JavaScript/JSX, TypeScript/TSX, Rust, Java, C, C++, Ruby, PHP, Swift, Scala, Zig, Lua, Bash/Shell, C#, Kotlin

**Import-aware refs:** Go, Python, JavaScript, TypeScript, Java, Kotlin, Scala, C#, PHP, Swift (others fall back to text matching)

## Limitations

- **Tree-sitter, not LSP.** Fast, no build step, works on broken code, zero config. The tradeoff: no type information. Refs use import-path matching, not type resolution, so cross-package references may produce false positives. For agent workloads — read, edit, search — structural parsing is enough.
- **macOS and Linux only.** Windows is not planned.
- **C/C++ compiler required** when building from source (tree-sitter grammars). Homebrew and the install script use pre-built binaries.
- **First index: under 1s** on small repos, ~15s on large ones (vitess, 3200 files). Incremental re-index after edits: ~12ms/file.

## Benchmarks

We run 9 scenarios (read a symbol, find refs, orient in codebase, edit a function, etc.) against real repos and measure tool response bytes — the raw amount of text that enters the agent's context window.

The baseline models a skilled agent using Claude Code's built-in tools: `Grep` to find symbols before reading, `Read` with line ranges (not whole files), `Edit`/`Write` confirmations. Orient reads 3 files after globbing; refs follows up on 3 grep matches. edr uses symbol reads, `--signatures`, `refs`, `map`, and batch flags.

| Repo | Lang | Files | Baseline | edr | Reduction |
|---|---|---|---|---|---|
| [urfave/cli](https://github.com/urfave/cli) | Go | ~70 | 248KB / 25 calls | 8KB / 9 calls | **97%** |
| [vitess/sqlparser](https://github.com/vitessio/vitess) | Go | ~70 | 516KB / 22 calls | 8KB / 9 calls | **98%** |
| [vitess/vtgate](https://github.com/vitessio/vitess) | Go | ~490 | 743KB / 24 calls | 18KB / 9 calls | **98%** |
| [pallets/click](https://github.com/pallets/click) | Python | ~17 | 358KB / 25 calls | 9KB / 9 calls | **97%** |
| [rails/thor](https://github.com/rails/thor) | Ruby | ~35 | 200KB / 25 calls | 9KB / 9 calls | **96%** |
| [reduxjs/redux-toolkit](https://github.com/reduxjs/redux-toolkit) | TS | ~190 | 217KB / 25 calls | 10KB / 9 calls | **95%** |
| [django/django](https://github.com/django/django) | Python | ~880 | 1,416KB / 25 calls | 19KB / 9 calls | **99%** |

Median reduction: **97%** across repos. edr loses on plain text search (structured JSON adds overhead vs raw grep — see breakdown below), but wins everywhere else. Call counts are summed across all 9 scenarios; each edr scenario is 1 call.

<details>
<summary>Per-scenario breakdown (urfave/cli)</summary>

| Scenario | Baseline | edr | Reduction |
|---|---|---|---|
| Understand a class API | 13,019B (whole file) | 1,486B (`--signatures`) | **89%** |
| Read a specific function | 3,026B / 2 calls (grep + range read) | 1,182B (symbol read) | **61%** |
| Find references | 86,463B / 4 calls (grep + 3 reads) | 179B (`refs`) | **100%** |
| Search with context | 614B (grep -C3) | 1,027B (structured) | **-67%** |
| Orient in codebase | 52,470B / 4 calls (glob + 3 reads) | 393B (`map`) | **99%** |
| Edit a function | 1,403B / 3 calls (grep + range + edit) | 394B (batch) | **72%** |
| Add method to a class | 5,393B / 3 calls (grep + range + write) | 249B (`--inside`) | **95%** |
| Multi-file read | 39,397B / 3 calls | 3,340B (batched) | **92%** |
| Explore a symbol | 51,967B / 4 calls | 72B | **100%** |
| **Total** | **253,752B / 25 calls** | **8,322B / 9 calls** | **97%** |

</details>

Scenarios and methodology in [`bench/scenarios/`](bench/scenarios/). Reproduce: `bash bench/run_real_repo_benchmarks.sh` (~10 min). Regenerate tables: `bash bench/gen_readme_table.sh`.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Bug reports and PRs welcome on [GitHub](https://github.com/jordw/edr/issues).

## License

[MIT](LICENSE)
