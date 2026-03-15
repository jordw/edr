# edr: the editor for agents

[![CI](https://github.com/jordw/edr/actions/workflows/ci.yml/badge.svg)](https://github.com/jordw/edr/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.25+-00ADD8.svg)](https://go.dev)

edr gives coding agents symbol-aware file operations, batched commands, and session tracking. Across 6 real-world repos, it reduces context consumption by 91-98% compared to the default tools that agent frameworks provide.

## Example

Adding a retry parameter to a scheduler. Without edr, 8 tool calls:

```
Read src/scheduler.py              # whole file to find one class
Grep "retry" src/                  # file:line list, no structure
Read src/config.py                 # whole file to find one function
Read src/worker.py                 # whole file for the API shape
Read src/retry.py                  # whole file for one call site
Edit src/scheduler.py              # old_text/new_text, no verification
Read src/scheduler.py              # re-read to confirm the edit
Run "python -m pytest"             # separate verify step
```

With edr, 2 calls:

```bash
# Gather: signatures, symbol reads, and search in one call
edr -r src/scheduler.py:Scheduler --sig \
    -r src/config.py:parse_config \
    -r src/worker.py:Worker --sig \
    -s "retry"

# Mutate: two edits, a new file, and verification in one call
edr -e src/scheduler.py --old "def run(self):" --new "def run(self, retries=3):" \
    -e src/config.py --old '"timeout": 30' --new '"timeout": 30, "retries": 3' \
    -w tests/test_retry.py --content "import pytest\n..." --mkdir
```

## Install

**Requirements:** macOS or Linux. The install script downloads a pre-built binary. Building from source requires Go 1.25+ and a C/C++ compiler (for tree-sitter grammars).

Run this in your project directory:

```bash
curl -fsSL https://raw.githubusercontent.com/jordw/edr/main/install.sh | sh
```

This installs the binary, indexes your project, and writes agent instructions to your agent's config file (`CLAUDE.md` for Claude Code, `.cursorrules` for Cursor, `AGENTS.md` for Codex). You can also write them manually; this repo's [CLAUDE.md](CLAUDE.md) is a complete working example.

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

edr stores its index in `.edr/` at the repo root. Add `.edr/` to your `.gitignore`. The index rebuilds automatically if deleted.

## How it works

edr parses source files with [tree-sitter](https://tree-sitter.github.io/tree-sitter/) and stores symbols (functions, classes, structs, methods) in a SQLite index. This makes every operation symbol-aware:

- **Reads** target a single function or class instead of a whole file
- **Signatures** return a container's API without implementation bodies (75-85% fewer tokens)
- **Searches** return structured JSON with symbol context, not `file:line` lists
- **Edits** re-index the modified file immediately and run verification automatically
- **Writes** can insert into a class or struct without reading the file first

**Sessions** track what content the agent has already seen. Set the `EDR_SESSION` environment variable and edr persists state across CLI calls. Re-reading an unchanged file returns `{unchanged: true}`. Symbol bodies already in context are replaced with `[in context]`. Without `EDR_SESSION`, each call is stateless.

**Batch flags** (`-r`, `-s`, `-e`, `-w`) combine multiple operations into a single CLI call. A typical workflow is two calls: one to gather context, one to apply all mutations.

## Benchmarks

6 real-world repos, 9 scenarios each. Baselines model how agents typically use built-in tools: `Read` returns whole files, `Grep` returns unstructured lines, edits require a read-then-write cycle. All numbers are median response bytes across 3 iterations. Scenarios defined in [`bench/scenarios/`](bench/scenarios/).

| Repo | Language | Baseline | edr | Reduction |
|---|---|---|---|---|
| [urfave/cli](https://github.com/urfave/cli) | Go | 322KB / 24 calls | 21KB / 9 calls | **93%** |
| [vitess/sqlparser](https://github.com/vitessio/vitess) | Go | 660KB / 21 calls | 16KB / 9 calls | **98%** |
| [vitess/vtgate](https://github.com/vitessio/vitess) | Go | 929KB / 23 calls | 32KB / 9 calls | **97%** |
| [pallets/click](https://github.com/pallets/click) | Python | 455KB / 24 calls | 21KB / 9 calls | **95%** |
| [rails/thor](https://github.com/rails/thor) | Ruby | 234KB / 24 calls | 15KB / 9 calls | **94%** |
| [reduxjs/redux-toolkit](https://github.com/reduxjs/redux-toolkit) | TypeScript | 245KB / 24 calls | 21KB / 9 calls | **91%** |

<details>
<summary>Per-scenario breakdown (urfave/cli)</summary>

| Workflow | Baseline | edr | Reduction |
|---|---|---|---|
| Understand a class API | 21,941B (read whole file) | 3,698B (`--signatures`) | **83%** |
| Read a specific function | 21,927B (read whole file) | 1,955B (symbol read) | **91%** |
| Find refs | 83,997B / 4 calls (`grep` + read matched files) | 1,055B / 1 call (`refs`) | **99%** |
| Search with context | 4,634B (`grep -C3`) | 4,153B (`search --text --context 3`) | **10%** |
| Orient in codebase | 65,238B / 5 calls (glob + reads) | 2,154B / 1 call (`map`) | **97%** |
| Edit a function | 25,164B / 3 calls (read + edit + verify) | 481B / 1 call (inline diff) | **98%** |
| Add method to a class | 22,141B / 2 calls (read + edit) | 161B / 1 call (`--inside`) | **99%** |
| Multi-file read | 42,465B / 3 calls | 2,639B / 1 call (batched + budget) | **94%** |
| Explore a symbol | 42,536B / 4 calls (grep + reads) | 5,437B / 1 call (body + callers + deps) | **87%** |
| **Total** | **330,043B / 24 calls** | **21,733B / 9 calls** | **93%** |

</details>

Reproduce: `bash bench/run_real_repo_benchmarks.sh` (clones repos to `/tmp`, ~5 min).

## CLI reference

| Command | What it does |
|---|---|
| `edr read file:Symbol` | Read a specific symbol (function, class, struct) |
| `edr read file:Class --signatures` | Container API without implementation bodies |
| `edr read file --depth N` | Progressive disclosure: collapse nesting below level N |
| `edr search "pattern" --body` | Symbol search with optional body snippets |
| `edr search "pattern" --text` | Text search (like grep, structured output) |
| `edr map` | Symbol overview of the repo or a directory |
| `edr explore Symbol --gather --body` | Symbol body + callers + deps in one call |
| `edr refs Symbol --impact` | Transitive impact analysis before refactoring |
| `edr edit file --old_text x --new_text y` | Edit with auto re-index and verification |
| `edr write file --inside Class` | Add a method or field without reading the file |
| `edr rename old new --dry-run` | Cross-file, import-aware rename with preview |
| `edr find "**/*.go"` | Find files by glob pattern |
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
- **Indexing cost.** First `edr init` takes a few seconds on small repos, longer on large ones. Incremental re-indexing after edits is fast.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for development setup, project structure, and guidelines. Bug reports and pull requests welcome on [GitHub](https://github.com/jordw/edr/issues).

## License

[MIT](LICENSE)
