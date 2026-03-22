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
edr run -- pytest
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

**Sessions.** edr tracks what the agent has already seen — files, symbols, search results, command output — and only shows what changed. Second read of an unchanged file: 0 tokens. `edr run -- make test` after a fix: only the diff from the previous run, unchanged lines collapsed. Same principle for builds, linters, any command. Zero config — sessions activate automatically.

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
| `run` | `edr run -- make test` — diffs against previous run |
| `status` | `edr status`, `edr status --focus "goal"` |
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

The baseline uses the tools agents actually have: whole-file `cat`, `grep -rn`, `find` + read. No symbol extraction, no batching. edr uses symbol reads, `--signatures`, `refs`, `map`, and batch flags.

| Repo | Lang | Files | Baseline | edr | Reduction |
|---|---|---|---|---|---|
| [urfave/cli](https://github.com/urfave/cli) | Go | ~70 | 192KB / 20 calls | 10KB / 9 calls | **95%** |
| [vitess/sqlparser](https://github.com/vitessio/vitess) | Go | ~70 | 314KB / 17 calls | 49KB / 9 calls | **85%** |
| [vitess/vtgate](https://github.com/vitessio/vitess) | Go | ~490 | 730KB / 19 calls | 21KB / 9 calls | **97%** |
| [pallets/click](https://github.com/pallets/click) | Python | ~17 | 290KB / 20 calls | 13KB / 9 calls | **96%** |
| [rails/thor](https://github.com/rails/thor) | Ruby | ~35 | 166KB / 20 calls | 12KB / 9 calls | **93%** |
| [reduxjs/redux-toolkit](https://github.com/reduxjs/redux-toolkit) | TS | ~190 | 182KB / 20 calls | 14KB / 9 calls | **92%** |
| [django/django](https://github.com/django/django) | Python | ~880 | 1,296KB / 20 calls | 25KB / 9 calls | **98%** |

Median reduction: **95%** across repos. edr loses on plain text search (structured JSON adds overhead vs raw grep — see breakdown below), but wins everywhere else. Call counts are summed across all 9 scenarios; each edr scenario is 1 call.

<details>
<summary>Per-scenario breakdown (urfave/cli)</summary>

| Scenario | Baseline | edr | Reduction |
|---|---|---|---|
| Understand a class API | 10,072B (whole file) | 1,486B (`--signatures`) | **85%** |
| Read a specific function | 15,307B (whole file) | 1,182B (symbol read) | **92%** |
| Find references | 67,101B / 4 calls | 179B / 1 call (`refs`) | **100%** |
| Search with context | 614B (grep -C3) | 1,027B (structured) | **-67%** |
| Orient in codebase | 11,481B / 2 calls | 2,673B / 1 call (`map`) | **77%** |
| Edit a function | 10,172B / 2 calls | 394B / 1 call (batch) | **96%** |
| Add method to a class | 10,072B / 2 calls | 249B / 1 call (`--inside`) | **98%** |
| Multi-file read | 30,794B / 3 calls | 3,295B / 1 call (batched) | **89%** |
| Explore a symbol | 41,285B / 4 calls | 72B / 1 call | **100%** |
| **Total** | **196,898B / 20 calls** | **10,557B / 9 calls** | **95%** |

</details>

Scenarios and methodology in [`bench/scenarios/`](bench/scenarios/). Reproduce: `bash bench/run_real_repo_benchmarks.sh` (~10 min). Regenerate tables: `bash bench/gen_readme_table.sh`.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Bug reports and PRs welcome on [GitHub](https://github.com/jordw/edr/issues).

## License

[MIT](LICENSE)
