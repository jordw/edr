You are testing `edr`, a CLI tool for coding agents. Your goal is to use it heavily on this codebase, find contract violations against spec.md, and report them.

## Phase 1: Exercise every command

Run each command below against this repo. For each, note: did it work? Was the output shape correct per spec.md? Was anything confusing, redundant, or missing?

```bash
# Orient
edr map --budget 300
edr map internal/dispatch/

# Read - all modes
edr read cmd/batch.go --budget 200
edr read cmd/batch.go --lines 1-20
edr read cmd/batch.go:executeReads
edr read cmd/batch.go:executeReads --sig
edr read cmd/batch.go:executeReads --skeleton
edr read cmd/batch.go cmd/commands.go --budget 300
edr read cmd/batch.go --full

# Search - both modes
edr search "executeReads"
edr search "executeReads" --text
edr search "TODO" --text --include "*.go" --context 2
edr search "nonexistent_xyz_12345"

# Refs
edr refs executeReads
edr refs executeReads --impact
edr refs Dispatch --chain executeReads

# Edit - dry run
edr edit cmd/batch.go --old-text "executeReads" --new-text "executeReads" --dry-run
edr edit cmd/batch.go --old-text "NONEXISTENT" --new-text "x"

# Write - dry run
edr write /tmp/edr_test_file.go --content "package main" --dry-run

# Session
edr session new

# Verify
edr verify

# Run - dedup across repeated calls
edr run -- echo "hello"
edr run -- echo "hello"
edr run --fuzzy -- printf 'PASS in 0.003s\n\nFAIL test2\n'
edr run --full -- echo "hello"

# Batch - combine operations
edr -r cmd/batch.go:executeReads --sig -s "DispatchMulti" --text -r internal/dispatch/dispatch.go --budget 300
edr -e cmd/batch.go --old "executeReads" --new "executeReads" --dry-run
```

## Phase 2: Stress the edges

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
14. **Run dedup**: `edr run -- echo "x"` twice — second should show `[unchanged: 1 lines]`
15. **Run exit code passthrough**: `edr run -- false` should exit non-zero
16. **Session PPID resolution**: After `edr session new`, a plain `edr read` should use the new session without `EDR_SESSION` env

## Phase 3: Check contracts against spec.md

### Transport contract
- [ ] Stdout contains only the envelope JSON, nothing else
- [ ] Stderr is empty (no `--verbose`)
- [ ] Exit code 0 iff `envelope.ok == true`
- [ ] Exit code 1 iff `envelope.ok == false`

### Envelope shape
- [ ] Envelope has `schema_version`, `ok`, `command`, `ops`, `verify`, `errors`
- [ ] `ops` is always an array, even for single ops
- [ ] `errors` is always an array
- [ ] `verify` is `null` when not attempted, an object when attempted
- [ ] `command` reflects the public invocation target, never internal names

### Op shape (all types)
- [ ] Every op has `op_id` and `type`
- [ ] `op_id` uses correct prefix (`r`, `s`, `m`, `e`, `w`, `x`, `n`) and zero-based count
- [ ] Ops preserve request order in batch
- [ ] Individual ops NEVER have an `ok` field

### Failure shape
- [ ] Failed ops have both `error` and `error_code`, never just one
- [ ] Successful ops never have `error` or `error_code`
- [ ] Failed ops do NOT include success fields (`content`, `matches`, `hash`, etc.)
- [ ] Per-op failures are in `ops[]`, not in envelope `errors[]`
- [ ] Every `error_code` is from the documented set (no undocumented codes)

### Path and position normalization
- [ ] `file` paths are repo-relative in all ops
- [ ] No absolute paths in successful output
- [ ] `lines` is `[start, end]`, 1-based, inclusive
- [ ] `column` values are 1-based (in search results)

### Read ops
- [ ] Successful reads have `file`, `hash`, `content`, `lines` at top level
- [ ] Symbol reads have `"symbol": "name"` (string, not sub-object)
- [ ] `--signatures` and `--skeleton` change content, not op shape
- [ ] `truncated` present when content was budget-limited
- [ ] Multi-file reads produce one op per file

### Search ops
- [ ] Has `kind` field: `"symbol"` or `"text"`
- [ ] Has `matches` array and `total_matches` integer
- [ ] Zero matches returns `ok: true`, not an error
- [ ] No silent fallback from symbol to text search

### Map ops
- [ ] Has `files`, `shown_files`, `shown_symbols`, `truncated`, `symbols`
- [ ] File-scoped maps have same fields with trivial values

### Mutation ops (edit, write, rename)
- [ ] Have `file`, `status`, `hash`
- [ ] `status` is one of: `applied`, `failed`, `skipped`, `dry_run`, `noop`
- [ ] Noop edits (old == new) return `status: "noop"`
- [ ] Noop renames (old == new) return `status: "noop"`
- [ ] Diff metadata present: `destructive`, `lines_changed`, `lines_added`, `lines_removed`, `diff`, `diff_available`
- [ ] `old_size` and `new_size` present on applied and dry-run edits

### Verify
- [ ] Uses `status`: `"passed"`, `"failed"`, or `"skipped"` — never `ok`
- [ ] Has `command` reflecting actual executed scope
- [ ] Has `output` with stdout/stderr
- [ ] Has `error` only when `status: "failed"`
- [ ] Has `reason` only when `status: "skipped"`
- [ ] Verify works without an index (does not create `.edr/`)

### Session
- [ ] Sessions are always active (default to "default" session)
- [ ] `edr session new` creates a unique session and writes PPID mapping
- [ ] After `session new`, subsequent calls auto-resolve via PPID
- [ ] First read returns `session: "new"`
- [ ] Repeated read returns `session: "unchanged"`
- [ ] `EDR_SESSION` env var overrides PPID-based resolution

### Run
- [ ] `edr run -- <cmd>` executes and returns output
- [ ] Second identical run returns `[unchanged: N lines]`
- [ ] Changed blocks show through, unchanged blocks are collapsed
- [ ] `--fuzzy` tolerates number changes in output
- [ ] `--full` bypasses dedup entirely
- [ ] Exit code passes through from wrapped command

### Budget
- [ ] Budget shapes response size, does not affect operation success
- [ ] `--full` bypasses budget cap
- [ ] Budget is per-operation in batch, not a shared pool
- [ ] `truncated: true` when content was reduced by budget

### Parity
- [ ] Standalone and batch produce identical op payloads for equivalent requests
- [ ] Shorthand flags (`--sig`, `--old`, `--new`) work in both standalone and batch
- [ ] Flag aliases resolve to canonical names in output

## Phase 4: Report

After testing, produce a prioritized list of findings:

```
## Contract violations (spec.md says X, edr does Y)
1. [severity] description — spec section violated — how to reproduce

## Bugs (wrong behavior not covered by spec)
1. [severity] description — how to reproduce

## Spec gaps (behavior exists but spec is silent)
1. description — what should spec.md say?
```

Rank by impact on agent trust and token efficiency. Focus on: can an agent write one parser and rely on it?
