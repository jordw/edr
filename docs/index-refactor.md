# Index subsystem refactor — agent briefing

## Why this exists

edr has three index stacks — `idx/` (trigram + symbol postings + import/ref graph), `scope/` (per-file results), `session/` (per-session state) — each re-implementing the same primitives: repo walk, staleness detection, atomic writes, dirty tracking, string interning, record storage. Stale-index bugs keep recurring (phantom symbols survived file deletion) because the logic is scattered. Scope v2 regressed on kubernetes — 981 MB gob shrank to 181 MB gzipped, but per-query latency rose because the whole blob decodes on every CLI invocation.

The goal is one set of index primitives, split into two families:

- **Cross-cutting primitives** used by every index: `staleness/`, `walk/`, `status/`.
- **Record-store primitives** used by indexes whose shape is keyed-per-file structured records: `blob/`, `builder/`. Scope uses these first; cross-file DeclID lands on them next (record shape sketched in §3 — validates the API before blob ships).

`idx/format.go`s trigram + symbol posting byte layout stays hand-written. Postings are varint bitmap-like structures tuned for AND/OR intersection; per-record gzip is the wrong shape. `idx/` migrates onto `staleness/`, `walk/`, `status/` — it does not migrate onto `blob/`. This is a deliberate split between posting-shaped and record-shaped storage, not a backlog deferral.

## Current state

### Index 1 — `internal/idx/`

Trigram + symbol postings + import graph + ref graph. Battle-tested byte-packed format. Hand-written Marshal/Unmarshal in `format.go`. **The byte layout does not change.**

- Staleness: git-index mtime stamp at `.edr/trigram.mtime` + `StatChanges` walk. Diverges from scope.
- Dirty tracking: `MarkDirty`/`ClearDirty`/`IsDirty` via `.edr/trigram.dirty`.
- Atomic write: unexported `atomicWrite`.
- Builder: `BuildFullFromWalk` / `BuildFullFromWalkWithImports`.

Migrates to: `staleness.Check` (canonical per-file mtime+size; git-index mtime becomes an internal fast-path, not a separate source of truth), `staleness.Tracker` (dirty file), `walk.Walker`, `status.Reporter`.

### Index 2 — `internal/scope/store/`

In flux. v2: gzipped gob of an interned index. Kubernetes: 181 MB disk, ~4 s cold query. In-progress v3 SSTable in `encode.go` — **delete it and redo on top of `blob/`.**

Migrates to: every primitive. Scope is the first full consumer of the new stack.

### Index 3 — `internal/session/`

Per-session metadata, checkpoint/restore, delta reads (hash-based), op log. Mutation-heavy small JSON. **Does not join `blob/`** — shape does not fit. Adopts `status.Reporter` only. Staleness is not a fit either — sessions delta-read is hash-based by design.

### Stability issues the refactor closes

- **Phantom symbols** (index retains entries for deleted files). Fixed by `staleness.Check` returning `Deleted` and consumers pruning. Scope prunes on rebuild; idx prunes in-place on next mark-dirty cycle. No ad-hoc patches.
- **scope.bin size** on large repos. Fixed by per-record storage + interned string table. Target: kubernetes scope < 200 MB.
- **Diverging staleness semantics.** Fixed by making per-file mtime+size canonical. Git-index mtime survives as an internal fast-path inside `staleness/`, not a public API.

## Components, in build order

Each component lands in its own PR with acceptance criteria met across **all named consumers** before the next starts. A primitive without a second consumer does not ship.

### 1. `internal/staleness/` — freshness + dirty tracking

```go
type Entry struct {
    Path  string
    Mtime int64
    Size  int64  // catches silent-replace with same mtime
}

type Snapshot struct {
    Taken   int64
    Entries map[string]Entry
}

type Diff struct {
    Added, Modified, Deleted []string
}

type WalkFn func(root string, fn func(path string) error) error

func Capture(root string, walk WalkFn) *Snapshot
func IsFresh(root string, e Entry) bool
func Check(root string, snap *Snapshot, walk WalkFn) *Diff

type Tracker struct { ... } // file-backed at edrDir/<name>.dirty
func OpenTracker(edrDir, name string) *Tracker
func (t *Tracker) Mark(paths ...string) // O_APPEND write, no lock
func (t *Tracker) Dirty() []string      // dedup on read
func (t *Tracker) Clear()
```

Concurrency: Tracker is append-only. `O_APPEND` writes are atomic for payloads under PIPE_BUF on local FS; paths are well under. Readers dedup. No lock. This matches the parallel-agent use case.

Mandatory test cases (each represents a bug class):

- mtime change → stale
- size change, same mtime → stale (silent-replace)
- file deleted → `Diff.Deleted`
- file created → `Diff.Added`
- permission-only change, content identical → NOT stale
- symlink target change → stale (follow symlinks)
- rapid writes within same mtime tick → documented limitation
- snapshot round-trip `Capture → save → load → Check` matches
- 8 goroutines concurrent `Mark`, no lost writes, final `Dirty()` = union

Acceptance:
- `scope/store.ResultFor` uses `staleness.IsFresh`.
- `idx.Staleness` delegates to `staleness.Check`; git-index mtime shortcut moves into staleness internals.
- Phantom-symbols regression test in `internal/idx/` that would fail without `Diff.Deleted`.

### 2. `internal/walk/` — one repo walker

```go
type Walker struct { ... }
func New(root string) *Walker
func (w *Walker) WithExts(exts ...string) *Walker
func (w *Walker) WithParallelism(n int) *Walker
func (w *Walker) Walk(fn func(path string) error) error
```

Inherits gitignore + binary filtering from existing `WalkRepoFiles`. Declarative config at call site.

Acceptance:
- `scope.Build` uses `walk.Walker`.
- `idx.BuildFullFromWalk` uses `walk.Walker`.
- `dispatch_index.go:hasImportPatterns` uses `walk.Walker` with `.WithExts(...)`.
- `internal/index.WalkRepoFiles` removed.

### 3. `internal/blob/` — seekable record store

Storage primitive for record-shaped indexes. Scope migrates first; cross-file DeclID lands on blob without API change.

**Second-consumer sketch (DeclID).** One record per declaration ID, body = `{file_idx u32, line u32, kind u8, name_idx u32, sig_bytes []byte}`. Keys are content-hashed DeclIDs (stable across incremental builds). Same Writer/Reader surface as scope — validates the keyed-lookup use case beyond a single consumer.

On-disk layout:

```
[magic: 4B][version: uint32]
[records: back-to-back; each = [uint32 size][gzip(body)]]
[string table: [uint32 byteLen][gob-encoded []string]]
[offset table: [uint32 count][count × (keyIdx u32, mtime i64, offset u64, size u32)]]
[trailer: strings_off u64, index_off u64, magic 4B, version u32]
```

```go
type Writer struct { ... }
func NewWriter(path string, magic []byte, version uint32) (*Writer, error)
func (w *Writer) Intern(s string) uint32
func (w *Writer) AppendRecord(key string, mtime int64, body []byte) error
func (w *Writer) Finalize() error // writes tables, trailer, atomic rename

type Reader struct { ... }
func Open(path string, magic []byte, version uint32) (*Reader, error)
func (r *Reader) String(idx uint32) string
func (r *Reader) Record(key string) (body []byte, mtime int64, ok bool)
func (r *Reader) Keys() []string
func (r *Reader) ForEach(fn func(key string, body []byte, mtime int64) error) error
```

Design decisions, locked:

- Trailer at EOF; records stream during write.
- Per-record gzip so single-record reads decompress exactly one record.
- String pool dedupes paths/names/kinds. Record bodies encode `u32` IDs, not raw strings — the 500 MB win on kubernetes depends on this, so scopes encoder calls `Intern()` and writes u32s into bodies.
- Magic + version validated at BOTH header and trailer.
- Open reads header + trailer + string table + offset table only; records fetched on demand via `ReadAt`.
- Concurrent writers: temp file named `<path>.<pid>.tmp`; atomic rename last-writer-wins. Readers see a consistent blob (old or new) at any instant.
- Deletion: blob is append-only. `builder/` triggers full rebuild when `len(Diff.Deleted) / len(records) > 0.1` or on explicit `edr index --rebuild`. Between rebuilds, callers stat-check file existence before returning a record.

Acceptance:
- Scope blob on kubernetes < 200 MB.
- `ResultFor` cold query < 50 ms (single seek + single gzip).
- `refs-to` cold query on a single-file target < 200 ms.
- `AllResults()` audit: no hot path decompresses every record. If one exists, it uses `ForEach` (streaming).
- DeclID record-shape sketch compiles against Writer/Reader before blob ships.

### 4. `internal/builder/` — walk + extract + write

Composes 1 + 2 + 3. Replaces `idx.BuildFullFromWalk` and `scopestore.Build`s parallel implementations.

```go
type ExtractorFn func(path string, data []byte) ([]byte, error)

type Spec struct {
    Walker     *walk.Walker
    Extractor  ExtractorFn
    BlobWriter *blob.Writer
    Tracker    *staleness.Tracker
    Deletions  []string // from staleness.Check; triggers rebuild if threshold exceeded
}

func Run(spec Spec) (int, error) // may return ErrRebuildRequired
```

No hooks, no middlewares, no plugin interface. If a consumer needs something weird, it writes its own loop against the primitives.

Acceptance:
- `scope.Build` is `builder.Run` + an `ExtractorFn`.
- `idx.BuildFullFromWalk` is NOT ported (posting-shaped, not record-shaped); uses `walk.Walker` + `staleness.Tracker` directly.

### 5. `internal/status/` — unified status reporting

```go
type Report struct {
    Name     string
    Exists   bool
    Files    int
    Bytes    int64
    Stale    bool
    Coverage float64
}

type Reporter interface {
    Status() Report
}

func Aggregate(reporters ...Reporter) []Report
```

Acceptance:
- `idx/`, `scope/store/`, `session/` each implement `Reporter`.
- `cmd/commands.go:buildNextResult` / `computeCurrentItems` / `computeFixItems` delegate to `status.Aggregate`.
- `dispatch_index.go` ad-hoc status output removed.

## Out of scope

- Rewriting `idx/format.go` posting byte layout.
- Daemonizing edr.
- Implementing cross-file DeclID resolution (only its record shape is sketched — enough to validate blobs API).
- Adding languages to the scope builder.

## Key files

- `internal/idx/format.go` — reference binary format; do not modify.
- `internal/idx/index.go` — existing build/query/staleness; migrates to new primitives.
- `internal/scope/store/store.go` — v2; migrates first.
- `internal/scope/store/encode.go` — v3 SSTable attempt. Delete.
- `internal/dispatch/dispatch_index.go` — current build orchestration.
- `internal/dispatch/dispatch_refs.go` — primary consumer of scope.
- `internal/session/` — adopts `status.Reporter` only.

## Pitfalls (non-negotiable)

- Inline strings in records cost 377 MB on kubernetes (Ref.File in v2s gob). Intern into the string pool, store `u32` IDs in bodies.
- Whole-blob decode is incompatible with fresh-process CLI. Per-record lazy decode is the only way persistence helps.
- mtime + size only; content hash is correct but too expensive. Document the 1-second mtime granularity as a known limitation.
- `staleness.Check` MUST produce `Deleted` and callers MUST prune. Phantom symbols came from skipping this.
- Posting storage (trigram/symbol) and record storage (scope/DeclID) are different shapes. Do not merge their byte layouts.

## Shipping

1 → 2 → 3 → 4 → 5. Each PR stands alone: tests green, acceptance criteria met across every named consumer. Scope, idx, and session migrate as the primitives they need land — no opportunistic deferral, no bundled PRs.
