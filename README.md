# edr: the editor for agents

[![CI](https://github.com/jordw/edr/actions/workflows/ci.yml/badge.svg)](https://github.com/jordw/edr/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.25+-00ADD8.svg)](https://go.dev)

Coding agents waste most of their context window reading whole files to find one function, re-reading every grep match to get structure, and editing without verification. edr fixes this.

edr indexes your codebase by symbol using [tree-sitter](https://tree-sitter.github.io/tree-sitter/) and stores the result in SQLite. Agents read individual functions instead of whole files, batch multiple operations into single requests, and get diffs instead of full content on repeated reads.

## Before and after

Adding a retry parameter to a scheduler takes 8 tool calls without edr:

```
Read src/scheduler.py              # 35KB whole file for one class
Grep "retry" src/                  # file:line list, no structure
Read src/config.py                 # 22KB whole file for one function
Read src/worker.py                 # 18KB whole file for the API shape
Read src/retry.py                  # 12KB whole file for one call site
Edit src/scheduler.py              # old_text/new_text, no verification
Read src/scheduler.py              # re-read to confirm the edit took
Run "python -m pytest"             # separate verify step
```

With edr, 2 requests over `edr serve --stdio`:

```jsonc
// Request 1: gather exactly what's needed
{"request_id":"1","reads":[
  {"file":"src/scheduler.py","symbol":"Scheduler","signatures":true},
  {"file":"src/config.py","symbol":"parse_config"},
  {"file":"src/worker.py","symbol":"Worker","signatures":true}
],"queries":[
  {"cmd":"refs","symbol":"Scheduler.run"},
  {"cmd":"search","pattern":"retry","body":true}
]}

// Request 2: edit + write + verify in one call
{"request_id":"2","edits":[
  {"file":"src/scheduler.py","old_text":"def run(self):","new_text":"def run(self, retries=3):"},
  {"file":"src/config.py","old_text":"\"timeout\": 30","new_text":"\"timeout\": 30, \"retries\": 3"}
],"writes":[
  {"file":"tests/test_retry.py","content":"import pytest\n...","mkdir":true}
],"verify":true}
```

## Install

Run this in your project directory:

```bash
curl -fsSL https://raw.githubusercontent.com/jordw/edr/main/install.sh | sh
```

This installs the binary, indexes your project, and injects agent instructions. One command, done.

Or with Homebrew:

```bash
brew install jordw/tap/edr
edr setup .
```

<details>
<summary>Other install methods</summary>

**From source** (requires Go 1.25+ and a C/C++ compiler for tree-sitter):

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

### Agent setup

To make an agent use edr, add instructions to whatever file your agent reads at startup (e.g., `CLAUDE.md`, `.cursorrules`, `AGENTS.md`). At minimum:

```markdown
Use `edr serve --stdio` for all file operations instead of built-in Read/Edit/Grep/Glob tools.
Start the server once, then send NDJSON requests on stdin.

# Read a symbol
{"request_id":"1","reads":[{"file":"src/main.go","symbol":"Server"}]}

# Edit + verify
{"request_id":"2","edits":[{"file":"src/main.go","old_text":"old","new_text":"new"}],"verify":true}
```

This repo's own [CLAUDE.md](CLAUDE.md) is a complete working example with the full command reference.

## How it works

edr parses source files with tree-sitter and extracts symbols (functions, classes, structs, methods) into a SQLite index. Reads, searches, and edits are all symbol-aware: you can read a single function, search within symbol boundaries, or replace a symbol by name.

`edr serve --stdio` starts a persistent NDJSON server over stdin/stdout. Each request can batch reads, edits, queries, and verification into a single call. The server tracks what content the agent has already seen within the connection:

- Re-reading a file returns `{unchanged: true}` or a diff of what changed
- Symbol bodies already in context are replaced with `[in context]`
- Small edit diffs are inlined automatically

Individual CLI commands (`edr read`, `edr search`, etc.) also work standalone without the server.

## Benchmarks

Baselines model how agents use standard tool-calling interfaces: `Read` returns whole files, `Grep` returns unstructured line matches, edits require a separate read-then-write cycle. All numbers are median response bytes across 3 iterations. Scenarios defined in [`bench/scenarios/`](bench/scenarios/).

6 real-world repos, 9 scenarios each:

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
| `edr edit file --old_text x --new_text y` | Edit with inline diff, auto re-index |
| `edr write file --inside Class` | Add a method/field without reading the file |
| `edr rename old new --dry-run` | Cross-file, import-aware rename with preview |
| `edr find "**/*.go"` | Find files by glob pattern |
| `edr verify` | Run build/test checks (auto-detects Go/npm/Cargo) |
| `edr serve --stdio` | Persistent NDJSON server for batch operations |
| `edr init` | Build or rebuild the symbol index |

All output is structured JSON. Token budgets (`--budget N`) cap any response to N tokens.

## Supported languages

**Full symbol indexing** (map, read, edit, signatures, inside, move):
Go, Python, JavaScript/JSX, TypeScript/TSX, Rust, Java, C, C++, Ruby, PHP, Zig, Lua, Bash/Shell

**Import-aware semantic refs** (refs, rename, explore callers/deps):
Go, Python, JavaScript, TypeScript. Other languages fall back to text-based references.

**Symbol indexing, less tested** (same features as above, fewer real-world miles):
C#, Scala, Kotlin, Elixir, HTML, CSS, JSON, YAML, TOML, Markdown, Protocol Buffers, SQL, HCL/Terraform, Dockerfile

edr can read and edit any text file regardless of language support.

## Comparison

| Tool | Overlap with edr | Key difference |
|---|---|---|
| **ripgrep** | Text search | edr adds symbol scoping, batching, sessions; ripgrep needs no index |
| **ctags** | Symbol indexing | edr adds reads, edits, sessions on top of the index; ctags has broader editor integration |
| **LSP** | Semantic analysis, refactoring | edr is a single binary with no per-language server setup; LSP has full type information |
| **Built-in agent tools** | Read, edit, grep, glob | edr adds symbol awareness, batching, context tracking; built-in tools need no build step |

## Limitations

- **C/C++ compilers required.** Tree-sitter grammars need CGO. The setup script handles this, but it is a real dependency.
- **Semantic refs are partial.** Import-aware reference tracking covers Go, Python, JS, and TS. Other languages use text matching, which can produce false positives.
- **Tree-sitter, not LSP.** The index captures structure (functions, classes, types) but not full type information. It will not catch everything a language server would.
- **Indexing cost.** First `edr init` takes a few seconds on small repos, longer on large ones. Incremental re-indexing after edits is fast.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for development setup, project structure, and guidelines. Bug reports and pull requests welcome on [GitHub](https://github.com/jordw/edr/issues).

## License

[MIT](LICENSE)
