# EDR Issues & Improvements

## Issues

### 1. Unbounded results when query `cmd` is inferred
- **Phase**: 6 (error handling)
- **Command**: `edr(queries: [{"pattern": "test"}])` — no `cmd` field
- **Expected**: Clear error or inferred `search` with a sensible default budget
- **Actual**: Inferred `search` and returned 242K chars — bypassed the 100KB hard cap in `handleDo`. MCP tool call exceeded token limits entirely.
- **Severity**: Bug (upgraded from UX friction — the 100KB hard cap failed to fire)
- **Area**: `cmd/mcp.go:inferQueryCmd`, `cmd/mcp.go:doQueryToMultiCmd`, `cmd/mcp.go:handleDo` (100KB truncation logic)
- **Iterations observed**: 1, 2, 3, 5, 6

### 2. Rename doesn't catch convention-related identifiers
- **Phase**: 4g (cross-file rename)
- **Command**: `edr(renames: [{old_name: "Greeter", new_name: "Speaker", ...}])`
- **Expected**: Also rename `NewGreeter` → `NewSpeaker` (Go constructor convention)
- **Actual**: Only renamed exact `Greeter` occurrences (6), left `NewGreeter` unchanged
- **Severity**: Enhancement
- **Area**: `internal/dispatch/dispatch.go:runRenameSymbol`

### 3. MCP schema strips unknown sub-object fields before typo detection
- **Phase**: 6a (error handling)
- **Command**: `edr(reads: [{file: "_iter_test.go", "symbl": "Hello"}])` via MCP
- **Expected**: Warning about unknown field `symbl` (did you mean `symbol`?)
- **Actual**: MCP tool schema strips unknown properties before they reach `handleDo`; `checkSubObjectFields` never fires
- **Severity**: UX friction — agents using MCP never see typo warnings for sub-object fields
- **Area**: `cmd/mcp.go:mcpTools` schema generation, `cmd/toolinfo.go`
- **Note**: Low priority — MCP schema validation is arguably better than post-hoc typo detection

### 4. Worktree copies indexed, causing ambiguity and noise
- **Phase**: 3 (search and xrefs)
- **Command**: `edr(queries: [{cmd: "refs", symbol: "DispatchMulti", impact: true}])`
- **Expected**: Refs for the actual source file only
- **Actual**: Ambiguity error listing 5 identical definitions from `.claude/worktrees/` copies. Search results also inflated (70 matches for 14 unique). `find` returned 120 results for 20 unique test files.
- **Severity**: Bug — worktree copies make `refs` unusable without file-scoping every call
- **Area**: `internal/index/indexer.go` — `.claude/worktrees/` should be added to default ignore patterns
- **Iterations observed**: 5, 6

### 5. SQLITE_BUSY on concurrent CLI access instead of retry
- **Phase**: 7 (concurrent access)
- **Command**: Two `edr do` processes in parallel (read + edit)
- **Expected**: Both succeed with serialized writes
- **Actual**: Edit fails with `database is locked (5) (SQLITE_BUSY)` — no retry or busy timeout
- **Severity**: Bug
- **Area**: `internal/index/db.go` — need `_busy_timeout` pragma or retry logic on SQLite connections
- **Iterations observed**: 5, 6

### 6. Delta reads return diff vs first-seen version instead of unchanged on repeated reads
- **Phase**: 2 (progressive reading)
- **Command**: Read symbol with `signatures: true`, then full body, then full body again (no `full: true`)
- **Expected**: Third read returns `{unchanged: true}` since content matches the second read
- **Actual**: Third read returns the same delta (signatures -> full body) as the second read, wasting tokens
- **Severity**: UX friction (wastes tokens on repeated identical deltas)
- **Area**: `internal/session/session.go:PostProcess` — content hash not updated after delta delivery
- **Iterations observed**: 6

### ~~4. Body dedup in search silently drops body instead of marking it~~ (RESOLVED)
Iteration 5 confirmed `"body": "[in context]"` marker is now present on deduped search results, and `skipped_bodies` array is included in the response.

## Improvements (priority order)

### 1. Exclude `.claude/worktrees/` from indexing
Worktree copies create duplicate symbols that cause ambiguity errors in `refs`, noise in `search` (5-6x inflated results), and bloated `find` output. This is the highest-friction issue, confirmed across iterations 5 and 6. It blocks `refs` entirely when worktrees exist and wastes tokens in every search.
- **Current**: worktree directories indexed like regular code
- **Desired**: auto-excluded from indexing (like `.git/`); add `.claude/worktrees/` to default ignore patterns
- **Area**: `internal/index/indexer.go` (ignore patterns / walkDir filter)

### 2. Distribute top-level budget to queries (not just reads)
Agents often set a global `budget` but not per-query budgets. Currently only reads get budget distribution from the top-level parameter; queries do not. This is the root cause of Issue 1 (unbounded results).
- **Current**: queries without individual budget return unlimited results even when top-level budget is set
- **Desired**: `handleDo` distributes `budget / len(queries)` (minimum 50) to queries without explicit budget, mirroring the existing reads logic
- **Area**: `cmd/mcp.go:handleDo` (section 2, query dispatch — mirror logic from section 1, reads)

### 3. Default budget cap when `cmd` is inferred
Agents that omit `cmd` likely also omit `budget`. Returning hundreds of unbudgeted results wastes tokens.
- **Current**: inferred search returns all matches unbounded
- **Desired**: inferred commands get a default budget (e.g., 200 tokens)
- **Area**: `cmd/mcp.go:inferQueryCmd` + `cmd/mcp.go:doQueryToMultiCmd`

### 4. Fix delta tracking to update content hash after delivery
When reading a symbol progressively (signatures, then full body, then full body again), the session should update its stored hash to the full body content after delivering the delta. Currently it keeps comparing against the first-seen version.
- **Current**: repeated full reads return the same delta diff each time
- **Desired**: second identical read returns `{unchanged: true}`
- **Area**: `internal/session/session.go:PostProcess` (update stored content hash after delta delivery)

### 5. Add SQLite busy timeout for concurrent CLI access
CLI `edr do` processes that hit writer lock contention fail with SQLITE_BUSY instead of waiting.
- **Current**: immediate failure on lock contention
- **Desired**: `_busy_timeout=5000` pragma or equivalent retry logic
- **Area**: `internal/index/db.go` (connection setup)

### 6. Add `limit` parameter to search queries
Agents often want "top N matches" but can only control token budget, not result count. A `limit` param would let agents say "give me top 5" directly.
- **Current**: no way to cap result count independent of budget
- **Desired**: `edr(queries: [{cmd: "search", pattern: "X", limit: 5}])`
- **Area**: `internal/search/search.go`, `cmd/mcp.go:doQuery`, `cmd/mcp.go:doQueryToMultiCmd`

### 7. Convention-aware rename option
When renaming `Foo` → `Bar`, also offer to rename `NewFoo` → `NewBar`, `FooConfig` → `BarConfig`, etc.
- **Current**: exact name match only
- **Desired**: `convention: true` flag renames related identifiers
- **Area**: `internal/dispatch/dispatch.go:runRenameSymbol`

### 8. Proportional budget distribution in batch reads
Batch reads divide budget evenly across N items. A small symbol and a large function each get the same allocation, truncating the large one unnecessarily.
- **Current**: `budget / len(commands)` per command
- **Desired**: estimate size from index metadata, allocate proportionally with a minimum floor
- **Area**: `internal/dispatch/dispatch.go:DispatchMulti`

### 9. Move symbol: unified diff in dry-run
Move dry-run shows two separate diffs (delete + insert) which requires mental reconstruction. Confirmed in iterations 2-3, 5, 6 (Phase 4e): two diffs shown for Goodbye move.
- **Current**: two separate diffs
- **Desired**: single merged diff or `preview_content` field showing final state; also add `"final_order": ["Goodbye", "Hello", "main"]` summary so agents can verify intent
- **Area**: `internal/dispatch/dispatch.go:runEditPlan`

### 10. Rename dry-run should show diffs, not just line previews
Rename `--dry-run` shows file/line/text for each occurrence, but not unified diffs. Agents can't verify surrounding context.
- **Current**: preview is `[{file, line, text}]` — just the matching line
- **Desired**: show unified diff format (like edit dry-run) so agents see context around each rename site
- **Area**: `internal/dispatch/dispatch.go:runRenameSymbol`

### 11. read_after_edit for writes should use delta
`read_after_edit` after writes forces `full: true`. Since the session just saw the write content, delta could save tokens.
- **Current**: `full: true` forced in post-edit reads
- **Desired**: normal read with delta awareness
- **Area**: `cmd/mcp.go:handleDo` (~line 826, post-edit reads section)

### 12. Map truncation should show counts and guidance
When `map` truncates at large repos, the response just says `truncated: true` with no hint on scope or how to narrow.
- **Current**: `"truncated": true`
- **Desired**: `"truncated": true, "shown": 45, "total": 1268, "hint": "use dir, type, or grep filter to narrow"`
- **Area**: `internal/dispatch/dispatch.go:runMapUnified`, `internal/index/indexer.go` (RepoMap function)
