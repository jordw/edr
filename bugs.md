# Bugs to Fix

## Medium

### 12. Batch dry-run edit + read does not show post-edit state
Chained edit-then-read shows the diff but the following read still shows pre-edit content.

**Expected:** Either the read should reflect the staged dry-run output, or dry-run chaining should be explicitly documented as unsupported.

### 17. Remove remaining legacy `EDR_FORMAT=json` support
`EDR_FORMAT=json` still works and the codebase still references it. Should be removed as a cleanup.

## Feature Requests — Editing Experience

### 30. Fuzzy/forgiving `--old` matching
`smartEditMatch()` in `dispatch_edit.go` does exact `strings.Index(content, matchText)`. When it fails, `notFoundError()` already detects whitespace-normalized and indentation-trimmed near-matches (lines 440-475) — but only to produce error hints, not to actually apply the edit.

**What to build:** A `--fuzzy` flag (or make it default) that, when exact match fails, tries the same normalization cascade that `notFoundError` already uses — but instead of erroring, maps the normalized match back to the original byte offsets and applies the edit there.

**Key code paths:**
- `smartEditMatch()` (`dispatch_edit.go:260`) — add fuzzy fallback after `strings.Index` returns -1
- `normalizeWhitespace()` / `trimLines()` (`dispatch_edit.go`) — already exist, reuse for matching
- `findOriginalOffset()` (`dispatch_edit.go`) — maps normalized offset → original offset, already exists
- Must still enforce uniqueness: if fuzzy match hits multiple locations, return `ambiguousMatchError`
- Response should include `"match_kind": "fuzzy_whitespace"` or `"fuzzy_indentation"` so agents know it wasn't exact

**Edge cases:**
- `--all` + `--fuzzy`: probably disallow initially (ambiguous what "all fuzzy matches" means)
- `--in Symbol` + `--fuzzy`: should work — scope first, then fuzzy match within symbol body

**Priority:** High — the detection logic exists, just needs to apply instead of error.

### 31. Structural symbol edits: `--delete`, `--move`
`runSmartEdit()` already has a symbol mode (line 78-84 of `dispatch_edit.go`): when called as `edr edit file.go:Symbol`, it resolves the symbol via `resolveSymbolArgs()` → `db.GetSymbol()`, gets `StartByte`/`EndByte`, and calls `smartEditByteRange()`. This means **`--replace Symbol` already works** — it's just `edr edit file.go:FuncName --new "new body"`.

**What to build (incrementally):**

**Phase 1: `--delete Symbol`** — simplest, high value.
- In `runSmartEdit()`, if `new_text` is empty string AND targeting a symbol, treat as deletion
- Already works with `--new ""` but agents don't know that; add `--delete` as explicit intent
- Must handle trailing newlines: delete `StartByte` to `EndByte` + consume one trailing `\n` if present
- Response: `{"status":"applied","symbol":"FuncName","action":"deleted","hash":"..."}`

**Phase 2: `--move Symbol --after OtherSymbol`**
- Resolve both symbols via `resolveSymbolArgs()` — need source and target `StartByte`/`EndByte`
- Cut source bytes (including leading whitespace to start-of-line), insert after target's `EndByte`
- Must be same-file only (cross-file move is a different feature)
- Use `Transaction` — it already handles reverse-byte-order application, but both edits are in the same file, so offsets shift. Safer: compute new content in memory as a single operation
- New flag in `cmdspec.go`: `move_after` (string, symbol name)

**Phase 3: `--wrap` is low priority** — too many syntax variations, agents can do this with read+replace.

**Key code paths:**
- `runSmartEdit()` (`dispatch_edit.go:15`) — add `--delete` and `--move-after` branches
- `resolveSymbolArgs()` (`dispatch.go:25`) — already handles `file:Symbol` resolution
- `smartEditByteRange()` (`dispatch_edit.go:88`) — reuse for the actual edit application
- `cmdspec.go` — register new flags (`delete: bool`, `move_after: string`)
- `batch_cmd.go` — expose `--delete` and `--move-after` in batch `-e` parser

**Priority:** High for `--delete`, Medium for `--move`.

### 32. Multi-file atomic edits with rollback
`Transaction.Commit()` in `edit/transaction.go` already does multi-file atomic writes with rollback (Phase 2, lines 100-150). The problem is **upstream**: `handleDo()` in `cmd/batch.go` processes edits sequentially via `dispatchSequential()`, and each `-e` calls `commitEdits()` independently — so edit 2 of 4 failing leaves edits 1 already committed.

**What to build:** A `--atomic` flag on batch that collects all `resolvedEdit` structs first, validates all matches/hashes, then calls `commitEdits()` once with the full set.

**Key code paths:**
- `cmd/batch.go` → `handleDo()` — currently dispatches edits one by one; needs an "atomic batch" path
- `dispatch.go:commitEdits()` — already handles multi-file transactions, no changes needed
- `edit/transaction.go:Commit()` — already has rollback on partial failure, no changes needed
- `batch_cmd.go:parseBatchArgs()` — add `--atomic` flag to `batchState`

**Implementation:**
1. When `--atomic` is set, batch.go resolves all edits (finding match offsets, validating hashes) but defers `commitEdits()` until all resolve successfully
2. If any resolution fails (not_found, ambiguous), return error with no files modified
3. Pass all `resolvedEdit` structs to a single `commitEdits()` call
4. The existing `Transaction` handles the rest — it already groups by file, sorts reverse-byte-order, and rolls back on write failure

**Edge case:** Two edits in the same file where edit 2's `--old` text overlaps with edit 1's replacement. Resolution order matters. Resolve all matches against original content, then check for overlapping byte ranges.

**Priority:** Medium.

### 33. Diff preview in edit responses by default
`smartEditByteRange()` (`dispatch_edit.go:88`) already computes `diff` via `edit.DiffPreview()` and includes it in the response for both dry-run and real edits. **This already works** — the `diff` field is in every edit response.

**What's actually missing:** The diff is a unified diff string, but batch output can be verbose. The real request is probably: after batch edits, show a consolidated diff summary rather than making agents chain `-r` to verify.

**What to check:** Is the diff field being stripped somewhere in session dedup or output formatting? If it's already there, this is a documentation/awareness issue, not a code change.

**Priority:** Low — may already be solved. Verify before building anything.

### 34. `--insert-at N` for pure insertion without `--old`
Currently, insertion requires either `--old` (find anchor text, replace with anchor+insertion) or `--start-line N --end-line N` (which replaces line N's content). There's no clean "insert before line N without replacing anything."

**What to build:** `--insert-at N` flag that inserts `--new` text before line N (or after, with `--insert-after N`).

**Implementation in `runSmartEdit()` (`dispatch_edit.go:15`):**
1. New flag: `insert_at` (int) in `cmdspec.go`
2. When set: convert line N to byte offset (reuse the line→byte logic from `smartEditSpan`, lines 135-155), set `startByte = endByte = offsetOfLineN` (zero-width span)
3. Call `smartEditByteRange()` with the zero-width span — `Transaction` already handles insertions (startByte == endByte means insert, no deletion)
4. `--new` content should have `\n` appended if not present (inserting a line, not splicing mid-line)

**Batch support:** In `batch_cmd.go`, `--insert-at N` modifier on `-e` operations. Mutually exclusive with `--old` and `--start-line`.

**Priority:** Medium.

### 35. `rename --dry-run` with full cross-file diff preview
`edr rename --dry-run` currently exists but need to verify its output format.

**What to build:** When `--dry-run` is set, collect all would-be edits across files and show a unified diff for each file, plus a summary line (`N files, M replacements`).

**Key code paths:**
- `dispatch.go:runRenameSymbol()` — find it, check current dry-run output
- Reuse `edit.DiffPreview()` / `edit.DiffPreviewContent()` for per-file diffs
- Output format: array of `{"file": "...", "diff": "...", "count": N}` objects

**Priority:** Low — rename works, this builds confidence.
