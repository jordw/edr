# EDR Issues & Improvements

## Issues

### 1. Unbounded results when query `cmd` is inferred
- **Phase**: 6 (error handling)
- **Command**: `edr(queries: [{"pattern": "test"}])` — no `cmd` field
- **Expected**: Clear error or inferred `search` with a sensible default budget
- **Actual**: Inferred `search` and returned 250 matches — massive unbudgeted response
- **Severity**: UX friction
- **Area**: `cmd/mcp.go:inferQueryCmd`, `cmd/mcp.go:doQueryToMultiCmd`

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

## Improvements (priority order)

### 1. Default budget cap when `cmd` is inferred
Agents that omit `cmd` likely also omit `budget`. Returning hundreds of unbudgeted results wastes tokens.
- **Current**: inferred search returns all matches unbounded
- **Desired**: inferred commands get a default budget (e.g., 200 tokens)
- **Area**: `cmd/mcp.go:inferQueryCmd` + `cmd/mcp.go:doQueryToMultiCmd`

### 2. Convention-aware rename option
When renaming `Foo` → `Bar`, also offer to rename `NewFoo` → `NewBar`, `FooConfig` → `BarConfig`, etc.
- **Current**: exact name match only
- **Desired**: `convention: true` flag renames related identifiers
- **Area**: `internal/dispatch/dispatch.go:runRenameSymbol`

### 3. Add `limit` parameter to search queries
Agents often want "top N matches" but can only control token budget, not result count. A `limit` param would let agents say "give me top 5" directly.
- **Current**: no way to cap result count independent of budget
- **Desired**: `edr(queries: [{cmd: "search", pattern: "X", limit: 5}])`
- **Area**: `internal/search/search.go`, `cmd/mcp.go:doQuery`, `cmd/mcp.go:doQueryToMultiCmd`

### 4. Proportional budget distribution in batch reads
Batch reads divide budget evenly across N items. A small symbol and a large function each get the same allocation, truncating the large one unnecessarily.
- **Current**: `budget / len(commands)` per command
- **Desired**: estimate size from index metadata, allocate proportionally with a minimum floor
- **Area**: `internal/dispatch/dispatch.go:DispatchMulti`

### 5. Move symbol: unified diff in dry-run
Move dry-run shows two separate diffs (delete + insert) which requires mental reconstruction.
- **Current**: two separate diffs
- **Desired**: single merged diff or `preview_content` field showing final state
- **Area**: `internal/dispatch/dispatch.go:runEditPlan`

### 6. read_after_edit for writes should use delta
`read_after_edit` after writes forces `full: true`. Since the session just saw the write content, delta could save tokens.
- **Current**: `full: true` forced in post-edit reads
- **Desired**: normal read with delta awareness
- **Area**: `cmd/mcp.go:handleDo` (~line 826, post-edit reads section)

### 7. Map truncation should include guidance
When `map` truncates at large repos, the response just says `truncated: true` with no hint on how to narrow.
- **Current**: `"truncated": true`
- **Desired**: `"truncated": true, "hint": "use dir, type, or grep filter to narrow"`
- **Area**: `internal/dispatch/dispatch.go:runMapUnified`
