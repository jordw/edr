# EDR Iteration Prompt

Use `edr` (the MCP tool) as your primary tool for ALL file operations. Exercise it on its own codebase. Your goal: find friction, bugs, and improvement opportunities by running real agent workflows.

## Phase 1: Orientation (3 calls max)

1. `edr(queries: [{cmd: "map", budget: 600}])` — get the repo shape
2. `edr(reads: [{file: "CLAUDE.md", budget: 400}])` — understand the tool's contract
3. `edr(queries: [{cmd: "find", pattern: "**/*_test.go"}])` — locate tests

Record: total files, languages detected, whether the map was useful for navigating next steps.

## Phase 2: Progressive reading (4 calls max)

Do these in order. Each builds on the previous.

1. Read `cmd/mcp.go:handleDo` with `signatures: true` — get the API surface
2. Read `cmd/mcp.go:handleDo` full body — verify delta kicks in (should return diff, not full content)
3. Read `cmd/mcp.go:handleDo` again without `full: true` — verify `unchanged: true`
4. Batch read 3 symbols from different files in one call with a shared `budget: 500`

For each call, note: response size (eyeball: small/medium/large), whether delta/dedup worked, whether the content was sufficient for understanding.

## Phase 3: Search and xrefs (3 calls max)

1. Symbol search: `edr(queries: [{cmd: "search", pattern: "Dispatch", body: true, budget: 300}])`
2. Text search with filter: `edr(queries: [{cmd: "search", pattern: "TODO|FIXME|HACK", regex: true, text: true, include: "*.go", budget: 200}])`
3. Impact analysis: `edr(queries: [{cmd: "refs", symbol: "DispatchMulti", impact: true, budget: 300}])`

Record: were results structured and scannable? Did budget prevent flooding? Any false positives in refs?

## Phase 4: Edit workflows (use disposable files)

Create a test file, then exercise these exact scenarios:

### 4a. Setup
```
edr(writes: [{file: "_iter_test.go", content: "package main\n\nimport \"fmt\"\n\ntype Greeter struct {\n\tName string\n}\n\nfunc (g *Greeter) Hello() string {\n\treturn \"hello \" + g.Name\n}\n\nfunc (g *Greeter) Goodbye() string {\n\treturn \"goodbye \" + g.Name\n}\n\nfunc main() {\n\tg := &Greeter{Name: \"world\"}\n\tfmt.Println(g.Hello())\n\tfmt.Println(g.Goodbye())\n}\n"}])
```

### 4b. Dry-run edit (preview only)
```
edr(edits: [{file: "_iter_test.go", old_text: "\"hello \" + g.Name", new_text: "fmt.Sprintf(\"hello, %s!\", g.Name)"}], dry_run: true)
```
Verify: diff shown, file unchanged.

### 4c. Real edit with read_after_edit
```
edr(edits: [{file: "_iter_test.go", old_text: "\"hello \" + g.Name", new_text: "fmt.Sprintf(\"hello, %s!\", g.Name)"}], read_after_edit: true)
```
Verify: edit applied, post_edit_reads contains updated content.

### 4d. Write inside a container
```
edr(writes: [{file: "_iter_test.go", content: "\tLanguage string", inside: "Greeter"}], read_after_edit: true)
```
Verify: field added before closing brace, indentation correct.

### 4e. Move a symbol
```
edr(edits: [{file: "_iter_test.go", move: "Goodbye", before: "Hello"}], dry_run: true)
```
Verify: preview shows delete + insert.

### 4f. Ambiguous match (intentional error)
```
edr(edits: [{file: "_iter_test.go", old_text: "g.Name", new_text: "g.FullName"}])
```
Verify: error message explains ambiguity, suggests using `all: true` or more context.

### 4g. Cross-file rename (dry run)
Create a second file that references `Greeter`, then:
```
edr(renames: [{old_name: "Greeter", new_name: "Speaker", dry_run: true, scope: "_iter_*.go"}])
```
Verify: preview shows all occurrences across both files.

## Phase 5: Batch / compound calls (2 calls max)

### 5a. Read + query in one call
```
edr(
  reads: [{file: "_iter_test.go", symbol: "Greeter", signatures: true}],
  queries: [{cmd: "search", pattern: "Greeter", body: true, budget: 200}, {cmd: "map", dir: "cmd/", type: "function", grep: "dispatch", budget: 200}]
)
```

### 5b. Edit + verify in one call
```
edr(
  edits: [{file: "_iter_test.go", old_text: "fmt.Sprintf(\"hello, %s!\", g.Name)", new_text: "\"hello \" + g.Name"}],
  verify: "build"
)
```
Verify: edit applied AND build check ran, both results in one response.

## Phase 6: Error handling and edge cases (3 calls max)

1. **Nonexistent file + typo field**: `edr(reads: [{file: "nope.go"}, {file: "_iter_test.go", "symbl": "Hello"}])` — verify error for first, typo warning for second
2. **Missing cmd in query**: `edr(queries: [{"pattern": "test"}])` — verify it infers `search` or gives a clear error
3. **Empty edit**: `edr(edits: [{file: "_iter_test.go", old_text: "\"hello \" + g.Name", new_text: "\"hello \" + g.Name"}])` — verify noop detection

## Phase 7: Concurrent access

Run two `edr do` processes in parallel via shell:
```bash
(echo '{"reads":[{"file":"cmd/mcp.go","symbol":"serveMCP"}]}' | edr do) &
(echo '{"edits":[{"file":"_iter_test.go","old_text":"\"hello \"","new_text":"\"hi \""}]}' | edr do) &
wait
```
Verify: both succeed, no corruption, writer lock works.

## Phase 8: Cleanup

```bash
rm -f _iter_test.go _iter_test2.go
git status  # verify no untracked files remain
```

## Phase 9: Report

After completing all phases, produce a structured report:

### Summary table

| Phase | Calls used / budget | Worked? | Issues |
|-------|-------------------|---------|--------|

### What works well
- List 3-5 strengths with the specific phase that demonstrated them

### Issues found

For each issue:
- **Phase**: which phase
- **Command**: exact edr call
- **Expected**: what should happen
- **Actual**: what happened
- **Severity**: bug / UX friction / enhancement
- **Implementation area**: file and function where you'd fix it

### Top 5 improvements (priority order)

For each:
1. What to change and why it matters for agents
2. Current behavior → desired behavior
3. Starting point in the codebase (file:function)

## Phase 10: Update issues.md

After producing the report, update `issues.md` at the repo root:

1. **Read `issues.md`** to see existing issues and improvements.
2. **Add new issues** found during this iteration to the Issues section. If a new issue duplicates an existing one, enhance the existing entry with additional detail or leave it as-is — do not create duplicates.
3. **Add new improvements** to the Improvements section using the same dedup logic.
4. **Re-assess priority** of the entire Improvements list based on cumulative evidence across all iterations. Reorder if priorities have shifted.
5. **Remove resolved items** — if an issue or improvement has been fixed since the last iteration, delete it.
