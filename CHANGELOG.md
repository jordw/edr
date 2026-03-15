# Changelog

## v0.3.0 — 2026-03-15

### Breaking changes

- **Symbol reads return `content` only** — the duplicate `body` field is removed. All reads (file and symbol) now use `content` consistently. Saves ~50% tokens on symbol reads.
- **`summary.hints` removed** — edit responses no longer include hints like "use verify:true to check build". Agents know features from CLAUDE.md.

### Fixes

- **Move `--before` dry-run preview** — dry-run now produces a single combined diff per file instead of per-edit diffs against the original. Also fixes `--before` insertion point to include declaration keywords (`type`, `func`, etc.) that tree-sitter doesn't include in StartByte.
- **PID safety** — `edr serve --stop` now shuts down via the Unix socket (`{"control":"shutdown"}`) instead of signaling a PID that may have been recycled by the OS. PID file cleanup is deferred so it runs even on panic. Stale PID files without sockets are cleaned up on next `edr serve`.

### Features

- **Per-caller sessions** — the server maintains independent sessions per caller PID (the agent's PPID). Multiple agents sharing one server get isolated delta reads and body dedup. Sessions for dead PIDs are garbage-collected every 30 seconds.
- **Dry-run skips verify** — `--dry-run` edits no longer run `go build ./...` since no files are changed.

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
