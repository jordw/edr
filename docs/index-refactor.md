# Index subsystem refactor — agent briefing

## What this is

edr has three index stacks — `idx/` (trigram + symbol postings + import/ref graph), `scope/` (per-file results), `session/` (per-session state) — that each re-implement overlapping primitives: repo walk, staleness detection, atomic writes, dirty tracking, status reporting. Stale-index bugs keep recurring (phantom symbols survived file deletion) because the logic is scattered. Separately, scope v2 persists as a 181 MB gzipped gob that decodes whole per CLI invocation — a net regression on kubernetes.

Two independent projects:

1. **Shared primitives refactor.** Extract `staleness/`, `walk/`, `atomic/`, `status/` from the three stacks. Closes the phantom-symbols bug class and unifies diverging semantics.
2. **Scope persistence rewrite.** Replace scopes whole-blob gob with an SSTable-based store. Sorted keys, block-level compression, sparse block index, lazy per-block decode. Built on top of the shared primitives.

Part 1 lands first. Part 2 depends on Part 1 but stands on its own scope-specific justification (181 MB → <150 MB, ~4 s → <50 ms cold query).

`idx/format.go`s posting byte layout stays hand-written. Postings are varint bitmap-like structures tuned for AND/OR intersection; they dont fit the SSTable block shape scope needs. idx adopts the shared primitives without changing its storage.

## Current state

### `internal/idx/`

Trigram + symbol postings + import/ref graph. Battle-tested byte-packed format. Hand-written Marshal/Unmarshal in `format.go`. Own staleness (git-index mtime stamp + `StatChanges` walk), dirty tracking (`.edr/trigram.dirty`), atomic write (`atomicWrite`), walker (`WalkRepoFiles`), status rendering (inline in `dispatch_index.go`).

### `internal/scope/store/`

v2: gzipped gob of an interned index. Kubernetes: 181 MB disk, ~4 s cold query. `encode.go` is an in-flight SSTable attempt — the shape is right, but it isnt built on shared primitives and its block/index layout needs rework. Part 2 replaces it with `internal/sstable/` and migrates scope onto that.

### `internal/session/`

Per-session metadata, checkpoint/restore, delta reads (hash-based), op log. Own atomic JSON write, own status rendering. Hash-based delta-reads dont fit `staleness/`; session only joins Part 1 for `atomic/` and `status/`.

### Stability issues the two projects close

- **Phantom symbols** (index retains entries for deleted files). Addressed in Part 1 by `staleness.Check` returning `Deleted`; consumers prune.
- **scope.bin size** on large repos. Addressed in Part 2 by block-compressed SSTable with an interned string pool for paths/names.
- **Diverging staleness semantics.** Addressed in Part 1 by making per-file mtime+size canonical. Git-index mtime survives as an internal fast-path inside `staleness/`, not a public API.

---

## Part 1 — Shared primitives refactor

Four packages. Each lands with every named consumer migrated before the next starts.

### 1.1 `internal/staleness/`

Freshness + dirty tracking. Consumers: idx, scope.

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
func (t *Tracker) Mark(paths ...string) // O_APPEND, no lock
func (t *Tracker) Dirty() []string      // dedup on read
func (t *Tracker) Clear()
```

Concurrency: Tracker is append-only. `O_APPEND` writes are atomic for payloads under PIPE_BUF on local FS; paths are well under. Matches the parallel-agent use case.

Test cases (each represents a bug class): mtime change → stale; size change with same mtime → stale (silent-replace); file deleted → `Diff.Deleted`; file created → `Diff.Added`; permission-only change → not stale; symlink target change → stale; rapid writes within same mtime tick → documented limitation; snapshot round-trip `Capture → save → load → Check` matches; 8-goroutine concurrent `Mark`, final `Dirty()` = union.

Landing criteria:
- `scope/store.ResultFor` uses `staleness.IsFresh`.
- `idx.Staleness` delegates to `staleness.Check`; git-index mtime shortcut moves into staleness internals.
- Phantom-symbols regression test in `internal/idx/` that fails without `Diff.Deleted`.

### 1.2 `internal/walk/`

One repo walker. Consumers: idx, scope.

```go
type Walker struct { ... }
func New(root string) *Walker
func (w *Walker) WithExts(exts ...string) *Walker
func (w *Walker) WithParallelism(n int) *Walker
func (w *Walker) WithSorted(bool) *Walker            // required by sstable writers
func (w *Walker) Walk(fn func(path string) error) error
```

Inherits gitignore + binary filtering from existing `WalkRepoFiles`. Declarative config at call site. `WithSorted(true)` guarantees lexicographic path order for SSTable ingestion; default is unsorted for performance.

Landing criteria:
- `scope.Build` uses `walk.Walker` with `WithSorted(true)`.
- `idx.BuildFullFromWalk` uses `walk.Walker`.
- `dispatch_index.go:hasImportPatterns` uses `walk.Walker` with `.WithExts(...)`.
- `internal/index.WalkRepoFiles` removed.

### 1.3 `internal/atomic/`

Temp+rename with optional hash-revalidate before rename (TOCTOU guard). Consumers: idx, scope, session.

```go
func WriteFile(path string, body []byte) error
func WriteFileVerify(path string, body []byte, expectedHash string) error
```

May stay a helper file under an existing package if a full package feels like overkill — decide at PR time. The point is one implementation, not one import path.

Landing criteria:
- `idx.atomicWrite` replaced.
- `scope/store.Index.save` inline rename replaced.
- `session/` JSON write replaced.

### 1.4 `internal/status/`

Unified status reporting. Consumers: idx, scope, session.

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

Landing criteria:
- `idx/`, `scope/store/`, `session/` each implement `Reporter`.
- `cmd/commands.go:buildNextResult` / `computeCurrentItems` / `computeFixItems` delegate to `status.Aggregate`.
- `dispatch_index.go` ad-hoc status output removed.

### Part 1 shipping

Order: 1.1 → 1.2 → 1.3 → 1.4. Each PR stands alone — tests green, every named consumer migrated before the next starts. No bundled PRs.

---

## Part 2 — Scope persistence rewrite

Replaces scopes gzipped-gob store with an SSTable: sorted keys, block-level compression, sparse block index, lazy per-block decode. Built on Part 1 primitives (`walk`, `staleness`, `atomic`, `status`).

Why SSTable over an offset-table-of-records:
- Block compression packs small records (many scope Results are <1 KB) far better than per-record gzip, which has ~20 bytes of overhead per record.
- Sorted keys turn prefix / directory-scoped queries into a single seek + streaming read. Important for `refs-to`, `AllResults`, and future "scope under `cmd/`" queries.
- Sparse index scales — at 16 KB blocks with 500k files (~1 KB avg), the in-memory index is ~30k entries instead of 500k.
- Streaming build: records are gzipped block-by-block as they arrive in sorted order; peak memory is one block, not the whole corpus.

Targets:
- Kubernetes scope file < 150 MB (v2: 181 MB gzipped gob; win comes from block compression + string-pool interning of Ref.File, which alone was 377 MB of redundant strings pre-compression).
- `ResultFor` cold query < 50 ms (block seek + single block decode, vs ~4 s whole-blob decode).
- `refs-to` cold query on a single-file target < 200 ms.
- Prefix scan (all paths under a directory) streams at ≥ 500 MB/s decompressed.

### 2.1 `internal/sstable/`

On-disk layout:

```
[magic: 4B][version: uint32]
[blocks: sequence of blocks; each =
    [uint32 raw_size][uint32 compressed_size][gzip(block_body)]
  block_body =
    [[key_len u16][key bytes][mtime i64][rec_len u32][rec bytes]] × N
]
[string table: [uint32 byteLen][gob-encoded []string]]
[block index: [uint32 count][count ×
    [first_key_len u16][first_key bytes][block_offset u64][block_size u32]]]
[trailer: strings_off u64, index_off u64, magic 4B, version u32]
```

```go
type Writer struct { ... }
func NewWriter(path string, magic []byte, version uint32, blockSize int) (*Writer, error)
func (w *Writer) Intern(s string) uint32
func (w *Writer) Append(key string, mtime int64, body []byte) error // keys MUST be sorted ascending
func (w *Writer) Finalize() error

type Reader struct { ... }
func Open(path string, magic []byte, version uint32) (*Reader, error)
func (r *Reader) String(idx uint32) string
func (r *Reader) Get(key string) (body []byte, mtime int64, ok bool)
func (r *Reader) Scan(startKey string) *Iter      // range/prefix scans
func (r *Reader) ForEach(fn func(key string, body []byte, mtime int64) error) error

type Iter struct { ... }
func (it *Iter) Next() bool
func (it *Iter) Key() string
func (it *Iter) Value() (body []byte, mtime int64)
func (it *Iter) Err() error
```

Design decisions, locked:

- Block size default: 16 KB compressed target. Writer flushes the current block when its uncompressed size ≥ 16 KB or on explicit `Finalize`.
- Keys appended in strictly ascending order. Writer returns an error on out-of-order `Append`. Builder guarantees order via `walk.Walker.WithSorted(true)`.
- Per-block gzip. Decompression unit is one block; `Get` decodes one block and scans within it.
- String pool dedupes paths/names/kinds. Record bodies encode `u32` IDs, not raw strings — the 377 MB Ref.File win depends on this, so scopes encoder calls `Intern()` and writes u32s into bodies. Block compression is additive on top.
- Magic + version validated at BOTH header and trailer.
- `Open` reads header + trailer + block index + string table only (tens to hundreds of KB on kubernetes). Blocks fetched on demand via `ReadAt` and cached by `sync.Pool` for the process lifetime.
- Concurrent writers: temp file `<path>.<pid>.tmp`, atomic rename via `atomic.WriteFile`. Last-writer-wins; readers see a consistent file at any instant.
- Deletion: SSTable is immutable per file. `builder/` triggers a full rebuild when `len(Diff.Deleted) / len(records) > 0.1` or on explicit `edr index --rebuild`. Between rebuilds, callers stat-check file existence before returning a record.
- No LSM, no compaction, no multi-level. One file. Rebuild when the deletion threshold is crossed. The corpus is small enough and rebuild cheap enough (seconds on kubernetes with sorted streaming) that LSM complexity is not justified.

### 2.2 `internal/builder/` — walk + extract + write

Composes `walk.Walker` (sorted), `staleness.Tracker`, `sstable.Writer`. Replaces `scopestore.Build`s inline loop. Scope is the consumer; idx keeps its own build path because its output is posting-shaped, not SSTable-shaped.

```go
type ExtractorFn func(path string, data []byte) ([]byte, error)

type Spec struct {
    Walker      *walk.Walker     // must have WithSorted(true)
    Extractor   ExtractorFn
    TableWriter *sstable.Writer
    Tracker     *staleness.Tracker
    Deletions   []string         // triggers rebuild if threshold exceeded
}

func Run(spec Spec) (int, error) // may return ErrRebuildRequired
```

No hooks, no middlewares, no plugin interface.

### 2.3 Scope migration

- Delete the current `internal/scope/store/encode.go` (the in-flight SSTable attempt). The `sstable/` package supersedes it.
- Replace `Index.save` / `Load` with `sstable.Writer` / `sstable.Reader`.
- `ResultFor` becomes `Reader.Get` + record decode.
- `AllResults` becomes `Reader.ForEach` (streaming, one block decode at a time). Audit callers during migration — no hot path should need all records in memory at once.
- `scope.Build` becomes `builder.Run` + the extractor function, with the walker set to sorted output.

### Part 2 shipping

Order: 2.1 → 2.2 → 2.3. Lands after Part 1.1 and 1.2 are in.

---

## Out of scope

- Rewriting `idx/format.go` posting byte layout.
- Cross-file DeclID resolution.
- Daemonizing edr.
- LSM / compaction / multi-level SSTables.
- Adding languages to the scope builder.

## Key files

- `internal/idx/format.go` — reference binary format; do not modify.
- `internal/idx/index.go` — adopts Part 1 primitives.
- `internal/scope/store/store.go` — Part 2 target.
- `internal/scope/store/encode.go` — delete; `sstable/` supersedes it.
- `internal/dispatch/dispatch_index.go` — current build orchestration.
- `internal/dispatch/dispatch_refs.go` — primary consumer of scope.
- `internal/session/` — adopts `atomic` + `status`.

## Pitfalls

- Inline strings in records cost 377 MB on kubernetes (Ref.File in v2s gob). Intern into the string pool, store `u32` IDs in bodies. Do not rely on block compression alone — it helps, but per-path deduplication via interning is the structural win.
- Whole-blob decode is incompatible with fresh-process CLI. Per-block lazy decode is the only way persistence helps.
- mtime + size only; content hash is correct but too expensive. Document 1-second mtime granularity as a known limitation.
- `staleness.Check` MUST produce `Deleted` and callers MUST prune. Phantom symbols came from skipping this.
- Posting storage (trigram/symbol) and SSTable storage (scope) are different shapes. Do not merge their byte layouts.
- Keys must be strictly sorted at `Append` time. If the walker is ever configured without `WithSorted(true)`, the writer fails loudly — do not add silent buffer-and-sort.
