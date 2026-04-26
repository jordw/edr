# Index aux-file invalidation audit

Scope: every on-disk file under `edrDir` written by `internal/idx`,
`internal/scope`, or `internal/session`. Goal: confirm that
`PatchDirtyFiles` leaves the persisted index in a self-consistent state
after an incremental tick, i.e. no consumer reads symbol-ID-keyed or
file-ID-keyed data that references symbols/files the patch has
renumbered or dropped.

Context: a previous correctness bug had `PatchDirtyFiles` rewriting
`trigram.idx` (which renumbers symbol IDs via `rebuildSymbolTable`) but
leaving `refs.bin` and `import_graph.bin` on disk. Those auxiliary
indexes reference symbols/files by ID, so after a tick their edges
pointed at the wrong symbols or at deleted files. Regression tests in
`internal/idx/patch_invalidation_test.go` pin the invalidation
contract.

## Inventory

| File | What it stores | Written by | Read by | Keyed by | Patch-safe? |
|---|---|---|---|---|---|
| `trigram.idx` | Files, trigram postings, symbols, name-posting index | `idx/build.go`, `idx/patch.go` (`atomicio.WriteFile`) | `idx/query.go`, `idx/load.go`, `idx/format.go`, `idx/stat.go` | file ID + symbol ID | **Patched in place** — the patch *is* the rewrite |
| `popularity.bin` | Per-symbol uint16 popularity scores | `idx/popularity.go` (`atomicio.WriteFile`) | `dispatch/resolve_rank.go` | symbol-ID array (positional) | **Invalidated** (`patch.go:81`); `ReadPopularity` also returns nil when `n != numSymbols` as a belt-and-suspenders self-heal |
| `refs.bin` | V2 reference graph: `ForwardOffsets[symID]` → name hashes; `InvSymIDs` (inverted index) → caller symbol IDs | `idx/refgraph.go` (`atomicio.WriteFile`) | `dispatch/dispatch_read.go`, `dispatch/dispatch_explore.go`, `dispatch/dispatch_index.go` | symbol ID (both forward and inverted) | **Invalidated** (`patch.go:82`) |
| `import_graph.bin` | `Files []string`, `Edges []{Importer,Imported}` as indices into `Files`, `InboundCount` | `idx/importgraph.go` (`os.Create`) | `dispatch/resolve_rank.go`, `dispatch/dispatch_read.go`, `index/walk.go`, `idx/build.go` (incremental rebuild source) | file path (frozen list — ids are indices into the frozen `Files`) | **Invalidated** (`patch.go:83`) |
| `scope.bin` | Per-file scope Results (gob + per-record flate) with `Header.Records[relPath] = {Offset, Length, Mtime, Size, ...}` | `scope/store/store.go:Build` (`atomicio.WriteVia`) | `dispatch/dispatch_refs.go` via `ResultFor`/`AllResults` | relative path; each record self-heals via `staleness.IsFresh(root, {Path, Mtime, Size})` before decode | **Self-healing per record** — see note below |
| `trigram.dirty` | List of edit-marked file paths (edit-staleness) | `staleness/tracker.go` (`Mark`) | `idx/tick.go`, `idx/index.go`, `dispatch/dispatch_edit.go` | n/a (list of paths) | **Cleared** after successful patch (`patch.go:84`) |
| `root.txt` | Repo breadcrumb (absolute path) | `index/edrdir.go` | none (human-only) | n/a | Immutable |
| `last_cleanup` | Rate-limit marker for `cmd/cleanup.go` (mtime only) | `cmd/cleanup.go` | `cmd/cleanup.go` | n/a (mtime) | Orthogonal |
| `sessions/ppid_*` | Per-ppid session state pointer | `session/session.go` | `session/session.go` | pid | Orthogonal (session state, not index state) |
| `sessions/<id>.json` | Active session state (ops, body cache, assumptions) | `session/session.go` | `session/session.go` | session id | Orthogonal |
| `sessions/cp_*.json` | Checkpoint snapshots | `session/checkpoint.go` | `session/checkpoint.go` | timestamp | Orthogonal |

All three IDs-keyed auxiliary indexes (`popularity.bin`, `refs.bin`,
`import_graph.bin`) are now removed at the end of `PatchDirtyFiles`
alongside the main rewrite. The inventory above is the complete set
of writes found by an exhaustive grep of `os.Create`,
`atomicio.WriteFile`, `atomicio.WriteVia`, and `os.WriteFile` across
`internal/idx`, `internal/scope`, and `internal/session`, plus every
`filepath.Join(edrDir, ...)` construction.

## Bugs found

None. The audit confirms the invalidation set landed by the
previous commit is complete. No additional `os.Remove` calls are
needed in `patch.go`. No change to `patch.go` in this commit.

## Why each file is safe

- **`trigram.idx`** — rewritten by `PatchDirtyFiles` itself. Safe by
  construction: all ID references inside the file are consistent
  because `rebuildSymbolTable`/`rebuildTrigramMap` use the fresh IDs.
- **`popularity.bin`** — removed on every patch. Double belt-and-
  suspenders: `ReadPopularity` also rejects files whose symbol count
  doesn't match the current header, so even a forgotten invalidation
  would self-correct rather than serve wrong scores.
- **`refs.bin`** — removed on every patch. Both the forward offsets
  array (indexed by caller symbol ID) and `InvSymIDs` (packed callee
  symbol IDs) are symbol-ID-keyed and would point at wrong symbols
  after renumbering. No safe way to remap in place without parsing
  every caller file again.
- **`import_graph.bin`** — removed on every patch. The `Files` list
  is frozen; deleted files stay as phantom importers and newly-added
  files are absent. Edges are path-based indirectly (via positional
  ids into `Files`), so even a file-ID-preserving patch can't keep
  this consistent.
- **`scope.bin`** — self-heals per record. `ResultFor` runs a
  `staleness.IsFresh` check (path + mtime + size) before decoding,
  so a stale file's record is treated as "not in index" and callers
  fall back to a live parse. The cost is that deleted files leave
  phantom records on disk until the next full `edr index`; this is
  a space trade-off, not a correctness issue. `AllResults()`
  explicitly documents that it serves stale records — cross-file
  binding callers in `dispatch_refs.go` accept this.
- **`trigram.dirty`** — cleared at the end of `PatchDirtyFiles`.
  Contains edit-marked paths that were folded into the patch's
  dirty set; once the patch completes they are no longer dirty.
- **`root.txt`** — immutable breadcrumb, write-once.
- **`last_cleanup`** — unrelated rate-limit marker.
- **`sessions/*`** — per-session state, orthogonal to index state.
  Not touched by `PatchDirtyFiles` (correctly).

## Open questions / follow-ups

- **Phantom scope records for deleted files.** Not a correctness bug
  (the staleness check catches them), but scope.bin only shrinks on
  a full rebuild. If deletions accumulate between full rebuilds, the
  file grows monotonically. Follow-up: teach `PatchDirtyFiles` to
  prune deleted paths from scope.bin's header, or accept the monotone
  growth as a known trade-off (current behavior).
- **`AllResults()` serves stale records.** Deliberately. Any caller
  that needs precision is expected to re-parse via the scope builder
  directly. If we ever add a use site where stale data produces
  wrong-looking results in a user-visible report, we would need to
  add a per-record staleness filter at the iterator level.
- **Concurrent patches.** Not an aux-file audit concern per se, but
  two processes running `IncrementalTick` race on the main rewrite
  and the aux-file removals. Addressed separately in the tick-lock
  change landing alongside this audit.
