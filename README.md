# edr: the editor for agents

[![CI](https://github.com/jordw/edr/actions/workflows/ci.yml/badge.svg)](https://github.com/jordw/edr/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.25+-00ADD8.svg)](https://go.dev)

edr gives AI coding agents symbol-aware file operations. Instead of reading whole files, agents read single functions. Instead of grepping for text, they query an index. Instead of making six tool calls to understand and edit a class, they make two.

It works with Claude Code, Cursor, Codex, and any agent that runs CLI tools. Fully local — no network calls, no telemetry.

## Example

An agent adding a retry parameter to a scheduler. The left column is what agents do with their default tools; the right is the same task with edr:

| Without edr (6 tool calls) | With edr (2 calls) |
|---|---|
| `Read src/scheduler.py` — 400-line file, need one class | `edr -r src/scheduler.py:Scheduler --sig` — just the API |
| `Grep "retry" src/` — flat file:line list | `-r src/config.py:parse_config` — just the function |
| `Read src/config.py` — whole file for one function | `-r src/worker.py:Worker --sig` — just the API |
| `Read src/worker.py` — whole file for the API shape | `-s "retry"` — structured search |
| `Edit src/scheduler.py` — no verification | `edr -e src/scheduler.py --old "def run(self):" --new "def run(self, retries=3):"` — edit + auto-verify |
| `Read src/scheduler.py` — re-read to confirm | `-e src/config.py --old '"timeout": 30' --new '"timeout": 30, "retries": 3'` — batched |

The first edr call gathers all context; the second applies all mutations and runs verification. Both are single CLI invocations using batch flags (`-r` read, `-s` search, `-e` edit).

## Install

**Requirements:** macOS or Linux. The install script downloads a pre-built binary. Building from source requires Go 1.25+ and a C/C++ compiler (for tree-sitter grammars).

Run this in your project directory ([review the script first](install.sh)):

```bash
curl -fsSL https://raw.githubusercontent.com/jordw/edr/main/install.sh | sh
```

This installs the binary, indexes your project, adds `.edr/` to `.gitignore`, and appends agent instructions to your agent's config file (`CLAUDE.md` for Claude Code, `.cursorrules` for Cursor, `AGENTS.md` for Codex). Existing content is preserved. You can also write them manually; this repo's [CLAUDE.md](CLAUDE.md) is a complete working example.

Or with Homebrew:

```bash
brew install jordw/tap/edr
edr setup .
```

<details>
<summary>Other install methods</summary>

**From source** (requires Go 1.25+ and a C/C++ compiler):

```bash
go install github.com/jordw/edr@latest
edr setup /path/to/your/project
```

**Cloud agents and CI** (installs Go/gcc if needed, builds, indexes):

```bash
git clone https://github.com/jordw/edr.git && ./edr/setup.sh
```

</details>

edr stores its index in `.edr/` at the repo root. `edr setup` adds `.edr/` to `.gitignore` automatically. The index rebuilds automatically if deleted.

## How it works

edr builds a symbol index using [tree-sitter](https://tree-sitter.github.io/tree-sitter/) and SQLite. Three things make this useful for agents:

**Symbol-level access.** Every operation knows about functions, classes, structs, and methods — not just files and lines. `edr read file.py:ClassName --signatures` returns the class API without implementation bodies (up to 85% fewer tokens). `edr write file.py --inside ClassName` inserts a method without reading the file first. Edits re-index the modified file immediately and run verification automatically.

**Sessions.** edr tracks what the agent has already seen. Set `EDR_SESSION` to a unique ID at conversation start, and edr persists state across CLI calls: re-reading an unchanged file returns `{unchanged: true}`, and symbol bodies already in context are replaced with `[in context]`. Without `EDR_SESSION`, each call is stateless.

**Batching.** Flags `-r`, `-s`, `-e`, `-w` combine reads, searches, edits, and writes into a single CLI call. A typical workflow is two calls: one to gather context, one to apply all mutations.

## Benchmarks

9 workflows across 7 repos, from small libraries to Django (880 files). The baseline models the **worst-case native workflow**: whole-file reads (no line ranges), reading every grep-matched file, no caching of prior context. Real agents sometimes do better — an experienced agent might grep then read only relevant files — but built-in tools lack symbol reads, signatures, `--inside`, and session dedup, so the baseline reflects what those tools force agents into for these operations. The metric is **response bytes** (file content for native, structured JSON for edr — JSON overhead means edr can lose on small results, as shown in the search scenario). All scenarios are defined in [`bench/scenarios/`](bench/scenarios/) and are reproducible.

| Repo | Language | Files | Baseline | edr | Reduction |
|---|---|---|---|---|---|
| [urfave/cli](https://github.com/urfave/cli) | Go | ~70 | 315KB / 24 calls | 16KB / 9 calls | **95%** |
| [vitess/sqlparser](https://github.com/vitessio/vitess) | Go | ~70 | 675KB / 21 calls | 16KB / 9 calls | **98%** |
| [vitess/vtgate](https://github.com/vitessio/vitess) | Go | ~490 | 976KB / 24 calls | 33KB / 9 calls | **97%** |
| [pallets/click](https://github.com/pallets/click) | Python | ~17 | 490KB / 25 calls | 19KB / 9 calls | **96%** |
| [rails/thor](https://github.com/rails/thor) | Ruby | ~35 | 239KB / 24 calls | 16KB / 9 calls | **93%** |
| [reduxjs/redux-toolkit](https://github.com/reduxjs/redux-toolkit) | TypeScript | ~190 | 266KB / 25 calls | 23KB / 9 calls | **91%** |
| [django/django](https://github.com/django/django) | Python | ~880 | 2,468KB / 33 calls | 30KB / 9 calls | **99%** |

<details>
<summary>Per-scenario breakdown (urfave/cli)</summary>

The biggest wins come from operations that have no built-in equivalent (signatures, symbol reads, `--inside`, `refs`). Text search can go negative — edr's structured JSON adds overhead when grep output is already small.

| Workflow | Baseline | edr | Reduction |
|---|---|---|---|
| Understand a class API | 13,019B (whole file) | 1,592B (`--signatures`) | **88%** |
| Read a specific function | 19,290B (whole file) | 1,463B (symbol read) | **92%** |
| Find references | 86,463B / 4 calls (grep + read all matches) | 865B / 1 call (`refs`) | **99%** |
| Search with context | 614B (grep -C3) | 1,812B (search --text --context 3) | **-195%** |
| Orient in codebase | 65,581B / 5 calls (glob + reads) | 2,235B / 1 call (`map`) | **97%** |
| Edit a function | 26,038B / 3 calls (read + edit + re-read) | 680B / 1 call (batch edit) | **97%** |
| Add method to a class | 13,019B / 2 calls (read + edit) | 184B / 1 call (`--inside`) | **99%** |
| Multi-file read | 39,397B / 3 calls | 2,606B / 1 call (batched + budget) | **93%** |
| Explore a symbol | 51,967B / 4 calls (grep + reads) | 4,562B / 1 call (body + callers + deps) | **91%** |
| **Total** | **315,388B / 24 calls** | **15,999B / 9 calls** | **95%** |

</details>

Reproduce: `bash bench/run_real_repo_benchmarks.sh` (clones repos to `/tmp`, ~10 min).

## CLI reference

**Reading and navigation:**

| Command | What it does |
|---|---|
| `edr read file:Symbol` | Read a specific function, class, or struct |
| `edr read file:Class --signatures` | Container API without implementation bodies |
| `edr read file --depth N` | Progressive disclosure: collapse nesting below level N |
| `edr map` | Symbol overview of the repo or a directory |
| `edr explore Symbol --body --callers --deps` | Symbol body + callers + dependencies in one call |
| `edr refs Symbol` | Find all references (import-aware for Go/Python/JS/TS) |
| `edr find "**/*.go"` | Find files by glob pattern |

**Searching:**

| Command | What it does |
|---|---|
| `edr search "pattern"` | Symbol search (matches function/class names) |
| `edr search "pattern" --text` | Text search (like grep, structured output) |

**Editing:**

| Command | What it does |
|---|---|
| `edr edit file --old "x" --new "y"` | Edit with auto re-index and verification |
| `edr write file --inside Class` | Add a method or field without reading the file |
| `edr rename old new --dry-run` | Cross-file, import-aware rename with preview |

**Maintenance:**

| Command | What it does |
|---|---|
| `edr verify` | Run build/test checks (auto-detects Go/npm/Cargo) |
| `edr init` | Build or rebuild the symbol index |

Batch flags (`-r`, `-s`, `-e`, `-w`) combine multiple operations in one call. All output is structured JSON.

## Supported languages

edr can read and edit any text file. Symbol-aware features require a supported language.

**Full symbol indexing** (map, read, edit, signatures, inside, move):
Go, Python, JavaScript/JSX, TypeScript/TSX, Rust, Java, C, C++, Ruby, PHP, Zig, Lua, Bash/Shell, C#, Kotlin

**Import-aware semantic refs** (refs, rename, explore callers/deps):
Go, Python, JavaScript, TypeScript. Other languages fall back to text-based references.

## Limitations

- **macOS and Linux only.** Windows is not supported.
- **C/C++ compiler required for building from source.** Tree-sitter grammars need CGO. The install script downloads pre-built binaries; `setup.sh` handles compiler installation for source builds.
- **Semantic refs are partial.** Import-aware reference tracking covers Go, Python, JS, and TS. Other languages use text matching, which produces false positives.
- **Tree-sitter, not LSP.** The index captures structure (functions, classes, types) but not full type information. It will not catch everything a language server would.
- **Indexing cost.** First `edr init` takes 1-3s on small repos, ~30s on large ones (e.g., vitess at 1.5M LOC). The index is typically 1-5MB. Incremental re-indexing after edits is fast (~50ms per file).

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for development setup, project structure, and guidelines. Bug reports and pull requests welcome on [GitHub](https://github.com/jordw/edr/issues).

## Changelog

See [CHANGELOG.md](CHANGELOG.md).

## License

[MIT](LICENSE)
