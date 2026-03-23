# Changelog

## v0.2.1 — 2026-03-22

### New commands

- **`edr status`** — session dashboard with op log, build state, external change detection, and pattern analysis. Replaces the old `next`/`status` commands.
- **`edr delta`** — command wrapper with diff-based output dedup. Only shows what changed between runs. `--reset` clears the baseline.
- **`edr undo`** — revert the last edit/write. Every mutation is auto-checkpointed onto a stack (cap 20).

### New edit capabilities

- **`--where` flag** — resolve symbol name to file+scope automatically, no file path needed.
- **`--in` flag** — scope edits within a symbol body.
- **`--delete`** — remove a symbol by name.
- **`--insert-at`** — insert text at a specific line.
- **`--move-after`** — move a symbol after another in the same file.
- **`--fuzzy`** — fuzzy match for old_text (whitespace-tolerant).
- **`--lines`** — constrain edit to a line range.
- **`--atomic`** — all-or-nothing batch edits (roll back on any failure).
- **`--hash`** — chain edits without re-reading by passing the previous edit's hash.
- **`@file` syntax** — pass old_text/new_text from files to avoid shell metacharacter issues.

### Languages

- **Swift and Scala added** — 18 languages now supported.

### Performance

- **Tree cache** — LRU cache for parsed tree-sitter trees. Symbol reads ~9x faster on repeated files.
- **Immediate reindexing** — edits and writes reindex under a writer lock instead of lazily. No more stale index after mutations.
- **Honest benchmarks** — baselines now model a skilled agent using range reads, not naive whole-file reads.

### Improvements

- **Plain transport format** — JSON header + raw body is now the default output format.
- **PPID-based session isolation** — auto-creates per-process sessions, detects PID reuse.
- **Stale session cleanup** — dead sessions, old run baselines, and stale PPID mappings cleaned from `.edr/`.
- **Rename `--dry-run`** — now shows full cross-file diff preview.
- **Cursor support** — `edr setup` writes instructions for Cursor in addition to Claude and Codex.
- **GitHub Pages site** — docs hosted at project site.
- **Agent instructions overhauled** — multiple rounds of rewriting for clarity, compliance, and token efficiency.
- **Always exit 0** for agent-facing commands; errors reported in JSON output.

### Bug fixes

- 23+ bugs fixed across the spec contract, session dedup, batch parity, flag normalization, and output consistency.
- Fixed concurrent edit races with batched SQLite writes.
- Fixed C/C++ rename missing call sites and `.h` prototype declarations.
- Fixed `--move-after` and `--delete` bypassing stdin requirement.

## v0.2.0 — 2026-03-15

### Breaking changes

- **12 data/markup grammars removed** — CSS, Dockerfile, Elixir, HCL/Terraform, HTML, JSON, Markdown, Protobuf, Scala, SQL, TOML, YAML. These formats don't benefit from symbol-based navigation. Files with these extensions still work via plain file read.

### Improvements

- **Binary size reduced 16%** — 44M to 37M by removing grammars that added noise rather than navigation value.
- **Grammar directory reduced 24%** — 196M to 149M of vendored C source.
- **16 languages supported** — Go, Python, JS/JSX, TS/TSX, C/H, C++, Rust, Java, Ruby, PHP, Zig, Lua, Bash, C#, Kotlin.

## v0.1.0 — 2026-03-09

Initial open source release.

### Features

- **Batch CLI** (`edr -r`, `-s`, `-e`, `-w`) — batches reads, searches, edits, writes, and verification in one call
- **Symbol-aware reads** — read specific functions/classes instead of entire files
- **Progressive disclosure** — `--signatures` (API only, 75-86% smaller), `--depth` (collapse nesting levels)
- **Structured search** — symbol search with scoring and body snippets, text search with grouping
- **Semantic references** — import-aware `refs` with transitive impact analysis (Go, Python, JS, TS)
- **Smart edits** — old_text/new_text, symbol replacement, line-range, regex, move, atomic multi-file batches
- **Write inside containers** — add fields/methods to classes/structs without reading the file first
- **Cross-file rename** — import-aware, scoped, with dry-run preview
- **Session optimizations** — body dedup for search results
- **Budget control** — cap any response to N tokens
- **Session tracing** — `bench-session` scores session efficiency
- **13 languages** — Go, Python, JS/JSX, TS/TSX, Rust, Java, C, C++, Ruby, PHP, Zig, Lua, Bash
- **Benchmarks** — 91-98% context savings across 6 real-world repos vs. raw file tools
