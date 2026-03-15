# Changelog

## v0.1.0 — 2026-03-09

Initial open source release.

### Features

- **Batch CLI** (`edr do`) — batches reads, queries, edits, writes, renames, and verification in one call
- **Symbol-aware reads** — read specific functions/classes instead of entire files
- **Progressive disclosure** — `--signatures` (API only, 75-86% smaller), `--depth` (collapse nesting levels)
- **Structured search** — symbol search with scoring and body snippets, text search with grouping
- **Semantic references** — import-aware `refs` with transitive impact analysis (Go, Python, JS, TS)
- **Smart edits** — old_text/new_text, symbol replacement, line-range, regex, move, atomic multi-file batches
- **Write inside containers** — add fields/methods to classes/structs without reading the file first
- **Cross-file rename** — import-aware, scoped, with dry-run preview
- **Session optimizations** — delta reads, body dedup, slim edit responses
- **Budget control** — cap any response to N tokens
- **Session tracing** — `bench-session` scores session efficiency
- **13 languages** — Go, Python, JS/JSX, TS/TSX, Rust, Java, C, C++, Ruby, PHP, Zig, Lua, Bash
- **Benchmarks** — 91-98% context savings across 6 real-world repos vs. raw file tools
