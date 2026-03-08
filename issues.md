# EDR Issues & Improvements

## Issues

### 1. Unbounded results when query `cmd` is inferred
- **Phase**: 6 (error handling)
- **Command**: `edr(queries: [{"pattern": "test"}])` — no `cmd` field
- **Expected**: Clear error or inferred `search` with a sensible default budget
- **Actual**: Inferred `search` and returned 242K chars — bypassed the 100KB hard cap. MCP tool call exceeded token limits.
- **Severity**: Bug (upgraded from UX friction — the 100KB hard cap failed to fire)
- **Area**: `cmd/mcp.go:inferQueryCmd`, `cmd/mcp.go:doQueryToMultiCmd`, `cmd/mcp.go:handleDo` (100KB truncation logic)
- **Iterations observed**: 1-8

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

### 4. `.claude/worktrees/` directories are indexed, polluting all results
- **Phase**: 1, 3 (orientation, search and xrefs)
- **Command**: All search, refs, and map queries
- **Expected**: Only files in the main repo tree are indexed; `.claude/worktrees/` should be excluded like `.git`
- **Actual**: All worktree copies are indexed, causing 5x duplicate results in search, ambiguity errors in refs (e.g., `DispatchMulti` has 5 definitions), and noisy map output
- **Severity**: Bug — worktree copies make `refs` unusable without file-scoping every call
- **Area**: `internal/index/indexer.go` — `.claude/worktrees/` should be added to default ignore patterns
- **Iterations observed**: 4-8

### 5. SQLITE_BUSY on concurrent CLI access instead of retry
- **Phase**: 7 (concurrent access)
- **Command**: Two `edr do` processes in parallel (read + edit)
- **Expected**: Both succeed with serialized writes
- **Actual**: Edit fails with `database is locked (5) (SQLITE_BUSY)` — no retry or busy timeout
- **Severity**: Bug
- **Area**: `internal/index/db.go` — need `_busy_timeout` pragma or retry logic on SQLite connections
- **Iterations observed**: 5, 6, 8
- **Note**: MCP in-process mutex handles concurrency fine. Issue is specific to multi-process CLI usage.

### 6. Delta reads return diff vs first-seen version instead of unchanged on repeated reads
- **Phase**: 2 (progressive reading)
- **Command**: Read symbol with `signatures: true`, then full body, then full body again (no `full: true`)
- **Expected**: Third read returns `{unchanged: true}` since content matches the second read
- **Actual**: Third read returns the same delta (signatures -> full body) as the second read, wasting tokens
- **Severity**: UX friction (wastes tokens on repeated identical deltas)
- **Area**: `internal/session/session.go:PostProcess` — content hash not updated after delta delivery
- **Iterations observed**: 6, 8
- **Note**: Some iterations (7) saw correct `{unchanged: true}`. May be intermittent or timing-dependent.

### 7. Move dry-run preview may use stale index content
- **Phase**: 4e (move symbol)
- **Command**: `edr(edits: [{file: "_iter_test.go", move: "Goodbye", before: "Hello"}], dry_run: true)`
- **Expected**: Preview diff reflects current on-disk file content
- **Actual**: Preview diff showed pre-edit content instead of the current content written in Phase 4c
- **Severity**: Bug — misleading preview could cause agents to approve incorrect moves
- **Area**: `internal/dispatch/dispatch.go:runEditPlan` — move preview should read fresh from disk, not from index/cache
- **Iterations observed**: 7

### 8. MCP server operates on main repo root, not worktree root
- **Phase**: 10 (update issues.md)
- **Command**: `edr(edits: [{file: "issues.md", ...}])` from a worktree context
- **Expected**: File operations target the worktree working directory
- **Actual**: Files are read/written relative to the main repo root, not the worktree the agent is running in
- **Severity**: Bug — agents in worktrees silently edit the wrong files
- **Area**: MCP server initialization in `cmd/mcp.go:serveMCP` — needs to respect the agent's cwd
- **Iterations observed**: 5

### 9. post_edit_reads and verify fire even when edits fail
- **Phase**: 4c, 5b (edit workflows)
- **Command**: `edr(edits: [{...failing...}], read_after_edit: true, verify: "build")`
- **Expected**: post_edit_reads and verify should be skipped when edits fail
- **Actual**: post_edit_reads returns file content and verify runs and passes, even though edits errored — misleading agents into thinking edits succeeded
- **Severity**: Bug
- **Area**: `cmd/mcp.go:handleDo` — sections 5b (post-edit reads) and 6 (verify) need guards on edit success
- **Iterations observed**: 8

### 10. Parallel iteration agents conflict on shared `_iter_test.go`
- **Phase**: 4, 5 (edit workflows)
- **Command**: `edr(writes/edits on _iter_test.go)` from multiple worktree agents
- **Expected**: Worktree agents isolated from each other
- **Actual**: All agents write `_iter_test.go` to the shared repo root, causing race conditions
- **Severity**: UX friction (in iteration prompt design, not edr itself)
- **Area**: `iteration.md` Phase 4a — test files should use unique names per worktree
- **Iterations observed**: 7, 8

### ~~Body dedup in search silently drops body instead of marking it~~ (RESOLVED)
Now shows `"body": "[in context]"` marker and `skipped_bodies` array. Confirmed iterations 5-8.

## Improvements (priority order)

### 1. Exclude `.claude/worktrees/` from indexing
Worktree copies create duplicate symbols that cause ambiguity errors in `refs`, noise in `search` (5-6x inflated results), and bloated `find` output. Highest-friction issue, confirmed across iterations 4-8. Blocks `refs` entirely when worktrees exist.
- **Current**: worktree directories indexed like regular code
- **Desired**: auto-excluded from indexing (like `.git/`); add `.claude` to default ignore patterns
- **Area**: `internal/index/indexer.go` (ignore patterns / walkDir filter)

### 2. Guard post_edit_reads and verify on edit success
When edits fail, agents get misleading results: post_edit_reads shows pre-edit content and verify passes. Both should be skipped on edit failure.
- **Current**: post_edit_reads and verify always run regardless of edit outcome
- **Desired**: skip both when edits fail; include `"skipped": "edits_failed"` in response
- **Area**: `cmd/mcp.go:handleDo` — add `editsFailed` boolean, guard sections 5b and 6

### 3. Distribute top-level budget to queries (not just reads)
Agents often set a global `budget` but not per-query budgets. Currently only reads get budget distribution; queries do not. Root cause of Issue 1 (unbounded results).
- **Current**: queries without individual budget return unlimited results even when top-level budget is set
- **Desired**: `handleDo` distributes `budget / len(queries)` (minimum 50) to queries without explicit budget
- **Area**: `cmd/mcp.go:handleDo` (section 2, query dispatch — mirror logic from section 1, reads)

### 4. Default budget cap when `cmd` is inferred
Agents that omit `cmd` likely also omit `budget`. 242K char responses exceed MCP transport limits.
- **Current**: inferred search returns all matches unbounded
- **Desired**: inferred commands get a default budget (e.g., 200 tokens)
- **Area**: `cmd/mcp.go:inferQueryCmd` + `cmd/mcp.go:doQueryToMultiCmd`

### 5. Fix delta tracking to update content hash after delivery
Progressive reads (signatures -> full -> full) should settle to `{unchanged: true}` on third read.
- **Current**: repeated full reads return the same delta diff each time (intermittent)
- **Desired**: second identical read returns `{unchanged: true}`
- **Area**: `internal/session/session.go:PostProcess` (update stored content hash after delta delivery)

### 6. Add SQLite busy timeout for concurrent CLI access
CLI `edr do` processes that hit writer lock contention fail with SQLITE_BUSY instead of waiting.
- **Current**: immediate failure on lock contention
- **Desired**: `_busy_timeout=5000` pragma or equivalent retry logic
- **Area**: `internal/index/db.go` (connection setup)

### 7. Add `limit` parameter to search queries
Agents often want "top N matches" but can only control token budget, not result count.
- **Current**: no way to cap result count independent of budget
- **Desired**: `edr(queries: [{cmd: "search", pattern: "X", limit: 5}])`
- **Area**: `internal/search/search.go`, `cmd/mcp.go:doQuery`, `cmd/mcp.go:doQueryToMultiCmd`

### 8. Convention-aware rename option
When renaming `Foo` -> `Bar`, also offer to rename `NewFoo` -> `NewBar`, `FooConfig` -> `BarConfig`, etc.
- **Current**: exact name match only
- **Desired**: `convention: true` flag renames related identifiers
- **Area**: `internal/dispatch/dispatch.go:runRenameSymbol`

### 9. Proportional budget distribution in batch reads
Batch reads divide budget evenly across N items. A small symbol and a large function each get the same allocation.
- **Current**: `budget / len(commands)` per command
- **Desired**: estimate size from index metadata, allocate proportionally with a minimum floor
- **Area**: `internal/dispatch/dispatch.go:DispatchMulti`

### 10. Move symbol: unified diff in dry-run
Move dry-run shows two separate diffs (delete + insert). Also may use stale index content (Issue 7). Confirmed iterations 2-8.
- **Current**: two separate diffs, potentially with stale content
- **Desired**: single merged diff; add `"final_order"` summary; always read fresh from disk
- **Area**: `internal/dispatch/dispatch.go:runEditPlan`

### 11. Rename dry-run should show diffs, not just line previews
Rename `--dry-run` shows file/line/text for each occurrence, but not unified diffs.
- **Current**: preview is `[{file, line, text}]` — just the matching line
- **Desired**: show unified diff format (like edit dry-run) so agents see context
- **Area**: `internal/dispatch/dispatch.go:runRenameSymbol`

### 12. read_after_edit for writes should use delta
`read_after_edit` after writes forces `full: true`. Delta could save tokens.
- **Current**: `full: true` forced in post-edit reads
- **Desired**: normal read with delta awareness
- **Area**: `cmd/mcp.go:handleDo` (~line 826, post-edit reads section)

### 13. Map truncation should show counts and guidance
When `map` truncates, no hint on how to narrow scope.
- **Current**: `"truncated": true`
- **Desired**: `"truncated": true, "shown": 45, "total": 1268, "hint": "use dir, type, or grep filter to narrow"`
- **Area**: `internal/dispatch/dispatch.go:runMapUnified`, `internal/index/indexer.go` (RepoMap function)

### 14. Iteration prompt: unique test file names per worktree
Parallel iteration agents all write to `_iter_test.go` in the shared repo root, causing race conditions.
- **Current**: all agents use `_iter_test.go`
- **Desired**: use unique names per worktree or write to worktree directory
- **Area**: `iteration.md` Phase 4a setup
