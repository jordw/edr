# Changelog

## v0.4.0 — 2026-04-12

### Semantic code editing

Three new operations that use the symbol index and import graph to make structural changes across files:

- **`edr rename file:Symbol --to NewName`** — Rename a symbol across all references. Uses import-graph-aware reference finding and word-boundary matching. Atomic multi-file writes via Transaction with TOCTOU hash guards and rollback. Supports `--dry-run` and `--verify`.
- **`edr extract file:Func --name NewFunc --lines N-M`** — Extract a line range from a function into a new function. De-indents extracted code, places the new function after the source, replaces the original lines with a call. `--call` overrides the replacement expression.
- **Cross-file `--move-after`** — `edr edit file:Func --move-after other.go:Target` now moves a symbol from one file to another. Both files are updated atomically in a single Transaction. Previously `--move-after` only worked within the same file.

### Agent instructions updated

All four instruction files (Claude, Cursor, Codex, generic) now document rename, extract, and cross-file move. Instruction token cap raised from 750 to 850.

### Eval coverage

- 11 new correctness tests (`bench/correctness_test.go`) covering rename, extract, and cross-file move with consistency checks (rename-then-read, move-then-read).
- 3 new scenario tests (`bench/scenario_test.go`) running rename/extract/move against the multi-language testdata repo (Go, Python, Java, Ruby, Rust, TypeScript, C).
- 10 new spec tests (`cmd/spec_cli_test.go`) for transport contract validation.
