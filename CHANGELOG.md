# Changelog

## v0.2.0 — 2026-03-14

### Breaking changes

- **`edr do` replaced by `edr serve --stdio`** — persistent NDJSON stdio server replaces one-shot batch CLI. Sessions are now connection-scoped (process lifetime) instead of file-backed with PPID hacking. Requests use the same JSON shape but wrapped in a `{request_id, ...}` envelope.
- **Session persistence removed** — `--session`, `--no-session` flags, `edr session list/clear/gc` subcommands, and `.edr/sessions/` directory are all gone. Single commands use ephemeral in-memory sessions. Persistent sessions live for the `edr serve` process lifetime.
- **JSON-on-root removed** — `edr '{...}'` no longer routes to batch mode. Use `edr serve --stdio` instead.

### Features

- **Stdio server** (`edr serve --stdio`) — persistent NDJSON server with connection-scoped sessions. Control messages: `ping`/`pong`, `status`, `shutdown`.
- **Protocol envelope** — every request has `request_id` (required) and optional `control`; every response has `request_id`, `ok`, and optional `error`.

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
