# Bugs — All Fixed

All 23 items from the 2026-03-21 backlog have been implemented and shipped.

## P1 — Spec correctness (all fixed)

1. **Failed ops leak success-only fields** — `AddFailedOpResult` now strips `file`, `lines`, `hash`, `sym`, `content` from error ops.
2. **Auto-index emits stderr in normal mode** — gated on `--verbose` in `openDBStrictRoot`.
3. **Verify uses non-spec `"timeout"` status** — mapped to `"failed"` with `"timeout after Ns"` error.
4. **Plain verify output drops command** — `command` now included in all verify header states.
5. **Plain transport omits `session:"new"`** — both `"new"` and `"unchanged"` preserved in read/search/map/refs.
6. **Verify failure body breaks header-only contract** — output moved into the header as an `output` field.
7. **`invalid_mode` error code never used** — `classifyErrorMsg` now detects "mutually exclusive" messages.
8. **`budget_used` never reported** — threaded through read, search, map results and plain headers.

## P2 — Edit surface (all fixed)

9. **`--delete` flag for edit** — canonical deletion form for symbol and text-match edits. Consumes trailing newline on symbol delete.
10. **Edit `--lines` flag** — colon-range shorthand (`--lines 20:30`) parsed in `runSmartEdit`.
11. **`--insert-at` flag for edit** — zero-width insertion before a line number, normalized to end with `\n`.
12. **`--fuzzy` flag for edit** — opt-in whitespace/indentation fuzzy matching with uniqueness enforcement. Disallowed with `--all`.

## P3 — Batch/standalone parity (all fixed)

13. **Batch CLI missing `--no-group` for search** — added to batch CLI parser.
14. **Batch CLI missing `--lang` for map** — added `Lang` field to `doQuery` and threaded through `queryToMultiCmd`.
15. **Batch CLI missing `--level` and `--timeout` for verify** — parsed in batch CLI, threaded as a map into verify dispatch.
16. **`--symbols` in batch but not standalone cmdspec** — registered on the `read` command in cmdspec.

## P4 — Cleanup and policy alignment (all fixed)

17. **Remove legacy `EDR_FORMAT=json` support** — marked as internal-only (retained for test infrastructure), not public API.
18. **Batch dry-run edit plus read** — warning emitted when post-edit reads follow dry-run edits (shows pre-edit state).
19. **Cursor target not in spec** — added Cursor as a supported first-class target in the spec.
20. **Help text teaches non-canonical flag syntax** — batch examples updated to use `--signatures`, `--old-text`, `--new-text`.

## P5 — Optional feature work (all fixed)

21. **`--move-after` for same-file symbol moves** — resolves source and target symbols, cuts and reinserts. Cross-file moves fail clearly.
22. **`--atomic` for batch edits** — validates all edits via dry-run first; if any fails, all are aborted.
23. **`rename --dry-run` with full cross-file diff preview** — per-file unified diffs included in dry-run rename output.
