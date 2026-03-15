# edr: the editor for agents

[![CI](https://github.com/jordw/edr/actions/workflows/ci.yml/badge.svg)](https://github.com/jordw/edr/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.25+-00ADD8.svg)](https://go.dev)

Coding agents waste most of their context on content they never use. They read entire files to find one function, grep then re-read every match, and edit files one at a time with no verification.

edr indexes your codebase by symbol so agents can read exactly what they need. It batches multiple operations into single requests and tracks what the agent has already seen, so repeated reads return diffs instead of full content.

## Before and after

Adding a retry parameter to a scheduler. Without edr, 8 tool calls:

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

## How it works

edr uses [tree-sitter](https://tree-sitter.github.io/tree-sitter/) to parse source files and extract symbols (functions, classes, structs, methods) with their byte ranges into a SQLite index. Reads, searches, and edits are all symbol-aware: you can read a single function, search within symbol boundaries, or edit a symbol by name.

`edr serve --stdio` starts a persistent NDJSON server over stdin/stdout. Each request can batch any combination of reads, queries, edits, writes, renames, and verification. The server tracks what content the agent has already seen within the connection:

- Re-reading a file returns `{unchanged: true}` or a diff of what changed
- Symbol bodies already in context are replaced with `[in context]`
- Small edit diffs are inlined automatically

Individual CLI commands (`edr read`, `edr search`, etc.) also work standalone without the server.

## Benchmarks

Each baseline reproduces how agents interact with standard tool-calling interfaces: `Read` returns whole files, `Grep` returns unstructured line matches, edits require a separate read-then-write. All numbers are median bytes of structured output across 3 iterations. Scenarios defined in [`bench/scenarios/`](bench/scenarios/).

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

## Install

For cloud agents and CI, the setup script installs Go and gcc if needed, builds edr, and adds it to PATH:

```bash
git clone https://github.com/jordw/edr.git
./edr/setup.sh /path/to/your/project
```

If you already have Go 1.25+ and a C compiler (required for tree-sitter grammars):

```bash
go install github.com/jordw/edr@latest
```

edr stores its index in `.edr/` at the repo root. Add `.edr/` to your `.gitignore`. The index rebuilds automatically if deleted.

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

## Comparison

| Tool | What it does well | What edr adds | What edr lacks |
|---|---|---|---|
| **ripgrep** | Fast text search, zero setup | Symbol-scoped results, batching, sessions | Requires indexing; ripgrep needs nothing |
| **ctags** | Mature symbol indexing, wide editor support | Reads, edits, sessions on top of the index | Fewer languages than ctags |
| **LSP** | Deep per-language semantics, refactoring | Single binary across 13 languages, no per-language server | No type info, weaker refactoring |
| **Built-in agent tools** | No setup, always available | Symbol awareness, batching, context tracking | Build dependency (Go + C compiler) |

## Supported languages

**Full symbol indexing** (map, read, edit, signatures, inside, move):
Go, Python, JavaScript/JSX, TypeScript/TSX, Rust, Java, C, C++, Ruby, PHP, Zig, Lua, Bash/Shell

**Import-aware semantic refs** (refs, rename, explore callers/deps):
Go, Python, JavaScript, TypeScript. Other languages fall back to text-based references.

edr can read and edit any text file regardless of language support.

## Limitations

- **C compiler required.** Tree-sitter grammars need CGO. The setup script handles this, but it is a real dependency.
- **Semantic refs are partial.** Import-aware reference tracking covers Go, Python, JS, and TS. Other languages use text matching, which can produce false positives.
- **Tree-sitter, not LSP.** The index captures structure (functions, classes, types) but not full type information. It will not catch everything a language server would.
- **Indexing cost.** First `edr init` takes a few seconds on small repos, longer on large ones. Incremental re-indexing after edits is fast.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for development setup, project structure, and guidelines. Bug reports and pull requests welcome on [GitHub](https://github.com/jordw/edr/issues).

## License

[MIT](LICENSE)
