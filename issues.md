# EDR Issues & Improvements

## Issues

### ~~1. Unbounded results when query `cmd` is inferred~~ (FIXED)
Fixed: top-level budget now distributed to queries; inferred commands get default budget of 200 tokens.

### 2. Rename doesn't catch convention-related identifiers
- **Iterations observed**: 4g (iterations 1-10)
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

### ~~4. `.claude/worktrees/` directories are indexed, polluting all results~~ (FIXED)
Fixed: `.claude` added to `alwaysIgnore` and `DefaultIgnore` in `internal/index/indexer.go`.

### ~~5. Delta reads return diff vs first-seen version instead of unchanged~~ (NOT A BUG)
Code already correctly updates stored hash after delta delivery. Regression tests added. Intermittent reports were likely due to worktree race conditions.

### 7. Move dry-run preview may use stale index content
- **Phase**: 4e (move symbol)
- **Command**: `edr(edits: [{file: "_iter_test.go", move: "Goodbye", before: "Hello"}], dry_run: true)`
- **Expected**: Preview diff reflects current on-disk file content
- **Actual**: Preview diff showed pre-edit content instead of the current content written in Phase 4c
- **Severity**: Bug — misleading preview could cause agents to approve incorrect moves
- **Area**: `internal/dispatch/dispatch.go:runEditPlan` — move preview should read fresh from disk, not from index/cache
- **Iterations observed**: 7 (not reproduced in iteration 9 -- preview showed correct post-edit content)

### 7. MCP server operates on main repo root, not worktree root
- **Phase**: 10 (update issues.md)
- **Command**: `edr(edits: [{file: "issues.md", ...}])` from a worktree context
- **Expected**: File operations target the worktree working directory
- **Actual**: Files are read/written relative to the main repo root, not the worktree the agent is running in
- **Severity**: Bug — agents in worktrees silently edit the wrong files
- **Area**: MCP server initialization in `cmd/mcp.go:serveMCP` — needs to respect the agent's cwd
- **Iterations observed**: 5

### ~~8. post_edit_reads and verify fire even when edits fail~~ (FIXED)
Fixed: `editsFailed` guard skips post_edit_reads and verify when edits fail, returns `"skipped: edits failed"`.

### 9. Parallel iteration agents conflict on shared `_iter_test.go`
- **Phase**: 4, 5 (edit workflows)
- **Command**: `edr(writes/edits on _iter_test.go)` from multiple worktree agents
- **Expected**: Worktree agents isolated from each other
- **Actual**: All agents write `_iter_test.go` to the shared repo root, causing race conditions
- **Severity**: UX friction (in iteration prompt design, not edr itself)
- **Area**: `iteration.md` Phase 4a — test files should use unique names per worktree
- **Iterations observed**: 7, 8

### ~~SQLITE_BUSY on concurrent CLI access~~ (RESOLVED)
Already fixed: `PRAGMA busy_timeout=5000`, `retryDB` wrapper with exponential backoff, WAL mode, and cross-process flock serialization all present in `internal/index/db.go`.

### ~~Body dedup in search silently drops body instead of marking it~~ (RESOLVED)
Now shows `"body": "[in context]"` marker and `skipped_bodies` array. Confirmed iterations 5-8.

## Improvements (priority order)

### 1. Add `limit` parameter to search queries
Agents often want "top N matches" but can only control token budget, not result count.
- **Current**: no way to cap result count independent of budget
- **Desired**: `edr(queries: [{cmd: "search", pattern: "X", limit: 5}])`
- **Area**: `internal/search/search.go`, `cmd/mcp.go:doQuery`, `cmd/mcp.go:doQueryToMultiCmd`

### 2. Convention-aware rename option
When renaming `Foo` -> `Bar`, also offer to rename `NewFoo` -> `NewBar`, `FooConfig` -> `BarConfig`, etc.
- **Current**: exact name match only
- **Desired**: `convention: true` flag renames related identifiers
- **Area**: `internal/dispatch/dispatch.go:runRenameSymbol`

### 3. Proportional budget distribution in batch reads
Batch reads divide budget evenly across N items. A small symbol and a large function each get the same allocation.
- **Current**: `budget / len(commands)` per command
- **Desired**: estimate size from index metadata, allocate proportionally with a minimum floor
- **Area**: `internal/dispatch/dispatch.go:DispatchMulti`

### 4. Move symbol: unified diff in dry-run
Move dry-run shows two separate diffs (delete + insert). Stale index content issue (Issue 7) not reproduced in iteration 9. Confirmed iterations 2-8.
- **Current**: two separate diffs
- **Desired**: single merged diff; add `"final_order"` summary
- **Area**: `internal/dispatch/dispatch.go:runEditPlan`

### 5. Rename dry-run should show diffs, not just line previews
Rename `--dry-run` shows file/line/text for each occurrence, but not unified diffs.
- **Current**: preview is `[{file, line, text}]` — just the matching line
- **Desired**: show unified diff format (like edit dry-run) so agents see context
- **Area**: `internal/dispatch/dispatch.go:runRenameSymbol`

### 6. read_after_edit for writes should use delta
`read_after_edit` after writes forces `full: true`. Delta could save tokens.
- **Current**: `full: true` forced in post-edit reads
- **Desired**: normal read with delta awareness
- **Area**: `cmd/mcp.go:handleDo` (~line 826, post-edit reads section)

### 7. Map truncation should show counts and guidance
When `map` truncates, no hint on how to narrow scope.
- **Current**: `"truncated": true`
- **Desired**: `"truncated": true, "shown": 45, "total": 1268, "hint": "use dir, type, or grep filter to narrow"`
- **Area**: `internal/dispatch/dispatch.go:runMapUnified`, `internal/index/indexer.go` (RepoMap function)

### 8. Iteration prompt: unique test file names per worktree
Parallel iteration agents all write to `_iter_test.go` in the shared repo root, causing race conditions.
- **Current**: all agents use `_iter_test.go`
- **Desired**: use unique names per worktree or write to worktree directory
- **Area**: `iteration.md` Phase 4a setup

### 9. Text search should default to `group: true` via MCP
Text search returns individual matches ungrouped by default, wasting tokens when multiple matches appear in one file. The `group: true` flag exists but agents must know to pass it.
- **Current**: `group: true` must be explicitly passed; agents rarely know to do this
- **Desired**: default `group: true` for text search via MCP (CLI can keep current behavior)
- **Area**: `cmd/mcp.go:doQueryToMultiCmd` (line ~1034), `internal/dispatch/dispatch_search.go`
- **Iterations observed**: 9, 10
