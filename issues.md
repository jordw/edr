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

### 3. Slim search results for large match counts
When search returns >50 matches, the response is huge even without `body: true`.
- **Current**: all matches returned at full detail
- **Desired**: top-N with `"more_available": true` and a count
- **Area**: `internal/search/search.go`, `internal/dispatch/dispatch.go:runSearchUnified`

### 4. Move symbol: unified diff in dry-run
Move dry-run shows two separate diffs (delete + insert) which requires mental reconstruction.
- **Current**: two separate diffs
- **Desired**: single merged diff or `preview_content` field showing final state
- **Area**: `internal/dispatch/dispatch.go:runEditPlan`

### 5. read_after_edit for writes should use delta
`read_after_edit` after writes forces `full: true`. Since the session just saw the write content, delta could save tokens.
- **Current**: `full: true` forced in post-edit reads
- **Desired**: normal read with delta awareness
- **Area**: `cmd/mcp.go:handleDo` (~line 826, post-edit reads section)
