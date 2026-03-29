# Changelog

## v0.3.0 — 2026-03-29

### Breaking changes

- **Tree-sitter removed.** Symbol extraction now uses pure-Go regex patterns (`internal/index/regex.go`). No CGO, no C compiler required. Binary size drops from ~37MB to ~6MB.
- **`refs` command removed.** Use `edr search` with `--text` for finding references.
- **`prepare` command removed.** Use `edr read` with `--expand` for pre-edit context.
- **`delta` command removed.** Run shell commands directly; use `edr status` for session state.
- **`-q` batch flag removed.** Query mode for refs/prepare is no longer needed.
- **`rename --text` flag removed.** Rename is now always text-based (the default and only mode).

### Languages

- **7 language families supported** — Go, Python, TypeScript/JavaScript, Rust, Java, Ruby, C/C++. Regex-based extraction covers common declaration patterns.
- Removed: PHP, Zig, Lua, Bash, C#, Kotlin, Swift, Scala (regex patterns not yet written for these).

### Improvements

- **Pure Go build.** No CGO, no C compiler, no vendored grammar sources. `go install` just works.
- **~6MB binary** — down from ~37MB with tree-sitter grammars.
- **Simpler architecture.** `internal/index/` no longer depends on tree-sitter bindings. `regex.go` replaces `parser.go` for symbol extraction.

## v0.2.1 — 2026-03-22

### New commands

- **`edr status`** — session dashboard with op log, build state, external change detection, and pattern analysis. Replaces the old `next`/`status` commands.
- **`edr delta`** — command wrapper with diff-based output dedup. *(Removed in v0.3.0.)*
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

- **Swift and Scala added** — 18 languages supported. *(Reduced to 7 language families in v0.3.0.)*

### Performance

- **Tree cache** — LRU cache for parsed trees. *(Replaced by regex-based parsing in v0.3.0.)*
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

- **12 data/markup grammars removed** — CSS, Dockerfile, Elixir, HCL/Terraform, HTML, JSON, Markdown, Protobuf, Scala, SQL, TOML, YAML. *(All tree-sitter grammars removed in v0.3.0.)*

### Improvements

- **Binary size reduced 16%** — 44M to 37M. *(Further reduced to ~6MB in v0.3.0 by removing tree-sitter entirely.)*

## v0.1.0 — 2026-03-09

Initial open source release.

### Features

- **Batch CLI** (`edr -r`, `-s`, `-e`, `-w`) — batches reads, searches, edits, writes, and verification in one call
- **Symbol-aware reads** — read specific functions/classes instead of entire files
- **Progressive disclosure** — `--signatures` (API only, 75-86% smaller), `--depth` (collapse nesting levels)
- **Structured search** — symbol search with scoring and body snippets, text search with grouping
- **Semantic references** — import-aware `refs` with transitive impact analysis. *(Removed in v0.3.0.)*
- **Smart edits** — old_text/new_text, symbol replacement, line-range, regex, move, atomic multi-file batches
- **Write inside containers** — add fields/methods to classes/structs without reading the file first
- **Cross-file rename** — text-based, scoped, with dry-run preview
- **Session optimizations** — body dedup for search results
- **Budget control** — cap any response to N tokens
- **Session tracing** — `bench-session` scores session efficiency
- **13 languages** — Go, Python, JS/JSX, TS/TSX, Rust, Java, C, C++, Ruby, PHP, Zig, Lua, Bash. *(Reduced to 7 families in v0.3.0.)*
- **Benchmarks** — 91-98% context savings across 6 real-world repos vs. raw file tools
