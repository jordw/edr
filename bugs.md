# Bugs to Fix

Purpose: make this backlog implementation-ready. Each item below states the current behavior, the required behavior, the main code touch points, and the minimum verification needed before closing it.

Prioritization: spec `MUST` violations first, then `SHOULD`, then batch/standalone parity gaps, then cleanup, then optional features.
Renumbered: 2026-03-21.

## Suggested implementation order

1. P1 items 1-8: spec and transport correctness.
2. P2 items 9-12: edit surface missing from the spec-defined CLI.
3. P3 items 13-16: batch/standalone parity.
4. P4 items 17-20: cleanup and policy alignment.
5. P5 items 21-23: optional feature work, not regressions.

## P1 — Spec correctness

### 1. Failed ops leak success-only fields into error headers

- Current: `internal/output/envelope.go:AddFailedOpResult()` JSON-roundtrips the whole result map into the failed op. For errors like `NotFoundError`, success-only fields such as `file` can leak into the error header.
- Required: failed ops must not include success-only fields like `file`, `lines`, `hash`, `sym`, or `content`.
- Main change: strip known success-only fields from the flattened map before appending the failed op.
- Touch points: `internal/output/envelope.go:AddFailedOpResult()`
- Verify: trigger a failing read/edit/search path and confirm the error header contains only error metadata plus allowed shared fields.

### 2. Auto-index emits stderr in normal mode

- Current: `cmd/root.go:openDBStrictRoot()` prints `edr: no index found, indexing repository...` and `edr: index ready (...)` to stderr even when `--verbose` was not requested.
- Required: stderr stays empty unless `--verbose` is explicitly set.
- Main change: gate all auto-index stderr writes on the verbose flag.
- Touch points: `cmd/root.go:openDBStrictRoot`
- Verify: run a first-use command with and without `--verbose`; only the verbose run should emit stderr.

### 3. Verify uses non-spec `"timeout"` status

- Current: `internal/dispatch/dispatch_verify.go` sets `status = "timeout"` on deadline exceeded, and plain output forwards that value.
- Required: verify status must be one of `passed`, `failed`, or `skipped` only.
- Main change: map timeout to `failed` and set `error` to `timeout after Ns`.
- Touch points: `internal/dispatch/dispatch_verify.go`
- Verify: force a verify timeout and confirm the result is `{"verify":"failed", ... "error":"timeout after Ns"}`.

### 4. Plain verify output drops the executed command

- Current: `runVerify()` records `command`, but `internal/output/plain.go:printPlain()` only emits `verify`, `reason`, and `error`.
- Required: the executed command must always be present in the plain verify header.
- Main change: include `command` in the verify header serialization.
- Touch points: `internal/output/plain.go:printPlain`
- Verify: run verify in passed, failed, and skipped states and confirm the same command string is reported in each header.

### 5. Plain transport omits `session:"new"`

- Current: plain rendering surfaces `session:"unchanged"` but silently drops `session:"new"`.
- Required: content-returning plain responses must preserve both `session:"new"` and `session:"unchanged"`.
- Main change: emit `session:"new"` in plain read/search/map/refs headers and make sure the wire helpers preserve it.
- Touch points: `internal/output/plain.go`, `internal/output/wire.go`
- Verify: after `edr session new`, run read/search/map/refs and confirm the first content-returning op emits `session:"new"`.

### 6. Verify failure body breaks the "errors are header-only" contract

- Current: failed verify prints the verify header and then writes trailing output as a body.
- Required: failed ops remain header-only.
- Main change: move the captured verify tail into the header as an `output` field.
- Touch points: `internal/output/plain.go`
- Verify: run a failing verify and confirm the response is a single header object with `verify`, `command`, `error`, and `output`, with no separate body payload.

### 7. `invalid_mode` error code is never used

- Current: mutually exclusive flag combinations such as `--signatures` plus `--skeleton` are classified as `command_error`.
- Required: impossible or mutually exclusive modes should classify as `invalid_mode`.
- Main change: add explicit detection in `cmd/commands.go:classifyErrorMsg()`.
- Touch points: `cmd/commands.go:classifyErrorMsg()`
- Verify: run known mutually exclusive flag combinations and confirm the error code is `invalid_mode`.

### 8. `budget_used` is never reported

- Current: budget truncation can set `trunc: true`, but the actual budget consumed is never returned.
- Required: budget-shaped responses should report `budget_used` wherever the spec says they `SHOULD`.
- Main change: thread `budget_used` through read, search, map, and refs results.
- Touch points: `internal/dispatch/dispatch_read.go`, `internal/dispatch/dispatch_search.go`, `internal/dispatch/dispatch.go`
- Verify: run truncated read/search/map/refs commands and confirm `budget_used` appears alongside `trunc:true`.

## P2 — Missing edit surface from the defined spec

### 9. `--delete` flag for edit

- Current: symbol deletion works indirectly via `--new-text ""`, but the explicit `--delete` flag does not exist.
- Required: `--delete` is the canonical deletion form for symbol-targeted edits.
- Main change: add a boolean `delete` flag to edit in cmdspec and batch parsing; in `runSmartEdit()`, treat symbol-targeted `--delete` as `newText = ""` and consume one trailing newline when deleting a whole symbol block.
- Touch points: `internal/cmdspec/cmdspec.go`, `internal/dispatch/dispatch_edit.go`, `cmd/batch_cmd.go`
- Verify: delete a symbol in standalone and batch mode and confirm the result is cleanly removed without leaving an extra blank line.

### 10. Edit `--lines` flag

- Current: edit supports `--start-line` and `--end-line`, but not the canonical `--lines 20:30` form used by the spec.
- Required: edit accepts `--lines` with the same colon-range parsing used by read.
- Main change: add a string `lines` flag to edit and parse it in `runSmartEdit()` using the same range rules as `runReadUnified()`.
- Touch points: `internal/cmdspec/cmdspec.go`, `internal/dispatch/dispatch_edit.go`
- Verify: apply a line-range edit via `--lines` and confirm invalid ranges fail the same way read does.

### 11. `--insert-at` flag for edit

- Current: there is no canonical pure-insert form for line-oriented edits.
- Required: edit supports `--insert-at N` as a zero-width insertion at line `N`.
- Main change: add `insert_at` to cmdspec and batch parsing; convert the target line to a byte offset and produce a zero-width span for the transaction layer. Normalize inserted text to end with `\n` for line-oriented use.
- Touch points: `internal/cmdspec/cmdspec.go`, `internal/dispatch/dispatch_edit.go`, `cmd/batch_cmd.go`
- Verify: insert before, inside, and at EOF; confirm inserted content lands at the right line and does not merge with surrounding lines.

### 12. `--fuzzy` flag for edit

- Current: fuzzy matching heuristics exist only to produce not-found hints; they never apply an edit.
- Required: `--fuzzy` is opt-in and can resolve whitespace/indentation-only mismatches while still enforcing uniqueness.
- Main change: add `fuzzy` to cmdspec and batch parsing; after exact match fails in `smartEditMatch()`, try the existing normalization cascade, map the normalized hit back to original byte offsets, and reject ambiguous fuzzy matches. Disallow `--all` plus `--fuzzy` initially.
- Touch points: `internal/cmdspec/cmdspec.go`, `internal/dispatch/dispatch_edit.go`, `cmd/batch_cmd.go`
- Verify: confirm exact-match behavior is unchanged, whitespace-only mismatch succeeds with `--fuzzy`, and duplicate fuzzy matches fail with `ambiguous_match`.

## P3 — Batch/standalone parity

### 13. Batch CLI missing `--no-group` for search

- Current: `no_group` exists in cmdspec and dispatch, but batch parsing does not accept `--no-group`.
- Required: batch search exposes the same grouping control as standalone search.
- Main change: add `--no-group` handling in `cmd/batch_cmd.go`.
- Touch points: `cmd/batch_cmd.go`
- Verify: run equivalent standalone and batch search commands and confirm they produce the same grouped vs ungrouped shape.

### 14. Batch CLI missing `--lang` for map

- Current: standalone map accepts `--lang`, but batch parsing does not and `doQuery` has no `Lang` field.
- Required: batch map exposes the same language filter as standalone map.
- Main change: add `Lang` to the batch query struct and parse `--lang` in `cmd/batch_cmd.go`.
- Touch points: `cmd/batch_cmd.go`
- Verify: run equivalent standalone and batch map queries with `--lang go` and confirm matching output scope.

### 15. Batch CLI missing `--level` and `--timeout` for verify

- Current: standalone verify exposes `--level` and `--timeout`, but batch parsing only accepts `--command`.
- Required: batch verify supports the same execution controls as standalone verify.
- Main change: parse `--level` and `--timeout` in `cmd/batch_cmd.go` and thread them into the verify dispatch flags.
- Touch points: `cmd/batch_cmd.go`
- Verify: run batch verify with custom level and timeout and confirm dispatch receives and applies both values.

### 16. `--symbols` exists in batch but not in standalone cmdspec

- Current: batch read and dispatch support `--symbols`, but standalone `edr read --symbols` does not because the flag is missing from cmdspec.
- Required: standalone and batch expose the same `--symbols` read mode.
- Main change: register `--symbols` for `read` in cmdspec.
- Touch points: `internal/cmdspec/cmdspec.go`
- Verify: run `edr read <path> --symbols` in standalone mode and confirm it reaches the same dispatch path as batch mode.

## P4 — Cleanup and policy alignment

### 17. Remove legacy `EDR_FORMAT=json` support

- Current: `EDR_FORMAT=json` still works and the codebase still references it even though plain mode is the public contract.
- Required: legacy env-based JSON mode is removed from the supported surface.
- Main change: delete the compatibility path and update any help or setup text that still mentions it.
- Verify: set `EDR_FORMAT=json` and confirm it has no effect or returns a clear unsupported-mode error, whichever behavior we standardize.

### 18. Batch dry-run edit plus read does not show post-edit state

- Current: a chained dry-run edit shows the diff, but a following read still shows pre-edit content.
- Required: pick one behavior and make it explicit:
  - Preferred: subsequent reads in the same dry-run batch see the staged post-edit state.
  - Acceptable fallback: document dry-run chaining as unsupported and fail clearly when a later op depends on staged output.
- Decision: choose the behavior before implementation. The preferred direction is staged post-edit reads because it preserves the mental model of chained batch ops.
- Verify: run `-e ... --dry-run -r ...` in one batch and confirm the read behavior matches the chosen contract.

### 19. Cursor target in setup is not in the spec

- Current: setup still treats Cursor as a first-class target via `TargetCursor` and `~/.cursor/rules/edr.mdc`, but the spec only names Claude and Codex plus `--generic`.
- Required: make code and spec agree.
- Decision: choose one of these, then implement it consistently:
  - Add Cursor to the spec as a supported first-class target.
  - Remove Cursor from `GlobalTargets()` and keep Cursor users on `--generic`.
- Touch points if removing: `internal/setup/setup.go`, `cmd/setup.go`
- Verify: after the decision, check setup help text, install flow, and status output for consistent target listings.

### 20. Help text teaches non-canonical flag syntax

- Current: help text and command descriptions still present shorthands as primary examples.
- Required: help text should teach one canonical long-form spelling per concept, such as `--signatures`, `--old-text`, `--new-text`, and `--dry-run`.
- Main change: update help and examples to lead with canonical long forms while still accepting shorthand aliases where supported.
- Touch points: `cmd/commands.go`, `cmd/batch_cmd.go`
- Verify: review `--help` output for read/edit/batch and confirm the primary examples use canonical long-form flags.

## P5 — Optional feature work

These are not regressions or spec violations. They should not block P1-P4.

### 21. `--move-after` for same-file symbol moves

- Scope: optional support for moving one symbol after another in the same file.
- Guardrails: same-file only; cross-file moves must fail.
- Main change: resolve source and target symbols, cut the source bytes, then insert after the target symbol end.
- Verify: moving a symbol preserves file validity and fails clearly on cross-file requests.

### 22. `--atomic` for batch edits

- Scope: optional explicit atomic mode for multi-edit batches.
- Main change: resolve all edits first, validate all matches and hashes, and commit once. If any resolution fails, no files are modified.
- Note: existing transaction/rollback support should reduce implementation risk.
- Verify: one failing edit in an atomic batch leaves all files unchanged.

### 23. `rename --dry-run` with full cross-file diff preview

- Scope: optional richer dry-run output for rename.
- Main change: confirm whether current dry-run output is sufficient; if not, collect all candidate edits and render per-file unified diffs plus a summary.
- Verify: dry-run rename previews enough information for a user to approve the rename without applying it.
