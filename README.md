# edr: the editor for agents

[![CI](https://github.com/jordw/edr/actions/workflows/ci.yml/badge.svg)](https://github.com/jordw/edr/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.25+-00ADD8.svg)](https://go.dev)

edr is not an agent — it's a tool that agents use. It gives AI coding agents (Claude Code, Cursor, Codex, and others) symbol-aware file operations, batched commands, and session tracking — replacing the default Read, Grep, and Edit tools with structured, budget-controlled alternatives. Fully local — no network calls, no telemetry.

## Example

Adding a retry parameter to a scheduler. Without edr, agents typically make several tool calls — reading whole files to find a single class, grepping for unstructured file:line lists, re-reading after edits to confirm:

```
Read src/scheduler.py              # 400-line file to find one class
Grep "retry" src/                  # file:line list, no structure
Read src/config.py                 # whole file to find one function
Read src/worker.py                 # whole file for the API shape
Edit src/scheduler.py              # old_text/new_text, no verification
Read src/scheduler.py              # re-read to confirm the edit
```

With edr, 2 calls — symbol-targeted reads, batched operations, and built-in verification:

```bash
# Gather: signatures, symbol reads, and search in one call
edr -r src/scheduler.py:Scheduler --sig \
    -r src/config.py:parse_config \
    -r src/worker.py:Worker --sig \
    -s "retry"

# Mutate: two edits, a new file, and verification in one call
edr -e src/scheduler.py --old "def run(self):" --new "def run(self, retries=3):" \
    -e src/config.py --old '"timeout": 30' --new '"timeout": 30, "retries": 3' \
    -w tests/test_retry.py --content "import pytest\n..."
```

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

edr parses source files with [tree-sitter](https://tree-sitter.github.io/tree-sitter/) and stores symbols (functions, classes, structs, methods) in a SQLite index. This makes every operation symbol-aware:

- **Reads** target a single function or class instead of a whole file
- **Signatures** return a container's API without implementation bodies (up to 85% fewer tokens)
- **Searches** return structured JSON with symbol context, not `file:line` lists
- **Edits** re-index the modified file immediately and run verification automatically
- **Writes** can insert into a class or struct without reading the file first

**Sessions** track what content the agent has already seen. The agent instructions written by `edr setup` tell the agent to set `EDR_SESSION` to a unique ID at the start of each conversation (e.g., `export EDR_SESSION=$(uuidgen)`). edr then persists state across CLI calls: re-reading an unchanged file returns `{unchanged: true}`, and symbol bodies already in context are replaced with `[in context]`. Without `EDR_SESSION`, each call is stateless.

**Batch flags** (`-r`, `-s`, `-e`, `-w`) combine multiple operations into a single CLI call. A typical workflow is two calls: one to gather context, one to apply all mutations.

## Benchmarks

9 workflows across 7 repos, from small libraries to Django (1500+ files). edr scenarios and baselines are defined side-by-side in the same auditable files: [`bench/scenarios/`](bench/scenarios/). All scenarios are deterministic — results are identical across runs.

**What the baseline measures.** Each baseline uses the built-in tools agents are given by default: `Read` (whole file), `Grep` (file:line output), and `Edit` (no verification). This is what agents do *without custom tooling or instructions* — they read whole files because they have no way to target a symbol, grep returns flat text because there's no index, and edits don't auto-verify because the tool doesn't support it. Agents *can* be instructed to use line ranges and batch calls, but the default tools don't offer symbol reads, signatures, `--inside`, or session dedup — those capabilities don't exist without something like edr. For reference lookups (refs, explore), the baseline counts grep output plus reading *every* matched file — no artificial cap.

The metric is **response bytes** (a proxy for context/token consumption), not wall time. Edit benchmarks run real edits with verification, not dry-runs.

| Repo | Language | Files | Baseline | edr | Reduction |
|---|---|---|---|---|---|
| [urfave/cli](https://github.com/urfave/cli) | Go | ~70 | 315KB / 24 calls | 16KB / 9 calls | **95%** |
| [vitess/sqlparser](https://github.com/vitessio/vitess) | Go | ~70 | 675KB / 21 calls | 17KB / 9 calls | **97%** |
| [vitess/vtgate](https://github.com/vitessio/vitess) | Go | ~490 | 976KB / 24 calls | 34KB / 9 calls | **97%** |
| [pallets/click](https://github.com/pallets/click) | Python | ~17 | 490KB / 25 calls | 20KB / 9 calls | **96%** |
| [rails/thor](https://github.com/rails/thor) | Ruby | ~35 | 239KB / 24 calls | 19KB / 9 calls | **92%** |
| [reduxjs/redux-toolkit](https://github.com/reduxjs/redux-toolkit) | TypeScript | ~190 | 266KB / 25 calls | 28KB / 9 calls | **89%** |
| [django/django](https://github.com/django/django) | Python | ~880 | 2,468KB / 33 calls | 35KB / 9 calls | **99%** |

<details>
<summary>Per-scenario breakdown (urfave/cli)</summary>

The biggest wins come from operations that have no built-in equivalent (signatures, symbol reads, `--inside`, `refs`). Text search can go negative — edr's structured JSON adds overhead when grep output is already small.

| Workflow | Baseline | edr | Reduction |
|---|---|---|---|
| Understand a class API | 13,019B (whole file) | 1,592B (`--signatures`) | **88%** |
| Read a specific function | 19,290B (whole file) | 1,463B (symbol read) | **92%** |
| Find references | 86,463B / 4 calls (grep + read all matches) | 865B / 1 call (`refs`) | **99%** |
| Search with context | 614B (grep -C3) | 2,816B (search --text --context 3) | **-359%** |
| Orient in codebase | 65,581B / 5 calls (glob + reads) | 2,134B / 1 call (`map`) | **97%** |
| Edit a function | 26,038B / 3 calls (read + edit + verify) | 47B / 1 call (edit + auto-verify) | **100%** |
| Add method to a class | 13,019B / 2 calls (read + edit) | 184B / 1 call (`--inside`) | **99%** |
| Multi-file read | 39,397B / 3 calls | 2,606B / 1 call (batched + budget) | **93%** |
| Explore a symbol | 51,967B / 4 calls (grep + reads) | 4,562B / 1 call (body + callers + deps) | **91%** |
| **Total** | **315,388B / 24 calls** | **16,269B / 9 calls** | **95%** |

</details>

Reproduce: `bash bench/run_real_repo_benchmarks.sh` (clones repos to `/tmp`, ~10 min).

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
| `edr edit file --old "x" --new "y"` | Edit with auto re-index and verification |
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
- **Indexing cost.** First `edr init` takes 1-3s on small repos, ~30s on large ones (e.g., vitess at 1.5M LOC). The index is typically 1-5MB. Incremental re-indexing after edits is fast (~50ms per file).

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for development setup, project structure, and guidelines. Bug reports and pull requests welcome on [GitHub](https://github.com/jordw/edr/issues).

## Changelog

See [CHANGELOG.md](CHANGELOG.md).

## License

[MIT](LICENSE)
