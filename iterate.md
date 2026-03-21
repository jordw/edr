You are testing `edr`, a CLI tool for coding agents. Your goal is to use it on this codebase, find bugs and ergonomic issues, and report them.

**Important:** Your agent config already has edr instructions installed (via `edr setup`). Use those instructions to figure out how to accomplish the tasks below — do not look at this file for edr command syntax. The point is to test whether the installed instructions are sufficient for an agent to discover and use edr correctly.

If a task is unclear or the instructions don't cover it, note that as a finding.

## Phase 1: Task-driven discovery

Work through these tasks using only the edr instructions in your agent config. For each, note: did the instructions guide you to the right command? Was the output useful? Was anything confusing or missing from the instructions?

### Orientation
1. Get a structural overview of this codebase, limited to ~300 tokens
2. Get a structural overview of just the `internal/dispatch/` directory

### Reading
3. Read a file with a budget of 200 tokens
4. Read just lines 1-20 of a file
5. Pick a function you found in the map and read just that function
6. Read just the signatures of a type or struct you found
7. Read the skeleton of a file
8. Read two files in a single call
9. Read a file with no budget limit

### Searching
10. Search for a symbol by name
11. Search for a text pattern across the codebase
12. Search for something you know doesn't exist

### References
13. Find all references to a function you've been reading
14. Check the impact of that function (what depends on it)

### Mutations (dry run only)
15. Dry-run an edit: change a string in a file and preview the result
16. Try to edit a string that doesn't exist in the file
17. Dry-run writing a new file

### Sessions
18. Start a new session, then read the same file twice — observe the dedup behavior

### Verification
19. Run the project's build/test verification

### Running commands
20. Run a shell command through edr twice — observe the sparse diff behavior
21. Run the same command with `--full` to bypass diffing

### Batching
22. Combine a symbol read, a text search, and a file read in one call
23. Combine a dry-run edit with another operation in one call

## Phase 2: Stress the edges

Phase 1 tested whether the instructions are sufficient for discovery. From here on, use exact commands — this is edge-case validation, not discovery testing.

Test these specific scenarios that tend to surface bugs:

1. **Ambiguous symbols**: `edr read Init` (should error with `ambiguous_symbol` code and candidates)
2. **File not found**: `edr read nonexistent.go` (should error with `file_not_found` code)
3. **Symbol not found**: `edr read cmd/batch.go:NonexistentSymbol` (should error with `not_found` code)
4. **Empty search**: `edr search "" --text` (should error, not empty success)
5. **Outside repo**: `edr write /tmp/test.go --content "x" --dry-run` (should error with `outside_repo` code)
6. **Session behavior**: Run `edr session new`, then read the same file twice — second should return `"session": "unchanged"`
7. **Multi-edit noop**: `edr -e cmd/batch.go --old "context" --new "context" --dry-run` (should return `status: "noop"`)
8. **Verify with bad code**: Make a syntax error via edit, then check verify returns `status: "failed"` with `command` and `output`
9. **Rename noop**: `edr rename executeReads executeReads --dry-run` (should return `status: "noop"`, not report changes)
10. **Batch with mixed success**: `edr -r cmd/batch.go --budget 100 -r nonexistent.go` — envelope `ok: false`, first op succeeds, second has `error_code`
11. **Signatures on function**: `edr read cmd/batch.go:executeReads --signatures` (should fail — signatures is for containers)
12. **Contradictory flags**: `edr read cmd/batch.go --signatures --skeleton` (should be a parse error)
13. **Internal command rejected**: `edr explore` (should be rejected with structured error, not silently handled)
14. **Run dedup**: `edr run -- echo "x"` twice — second should show `[no changes, 1 lines]`
15. **Run exit code passthrough**: `edr run -- false` should exit non-zero
16. **Session PPID resolution**: After `edr session new`, a plain `edr read` should use the new session without `EDR_SESSION` env

## Phase 3: Report

After testing, produce a prioritized list of findings:

```
## Bugs (something is broken or wrong)
1. [severity] description — how to reproduce

## Ergonomic issues (something is awkward, confusing, or wasteful)
1. [severity] description — what would be better

## Instruction gaps (agent couldn't figure out how to do something)
1. description — what was the task, what was missing from the instructions
```

Rank by impact on agent trust and token efficiency.
