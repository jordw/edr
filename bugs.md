# Bugs — All Fixed

All 11 items from the 2026-03-21 backlog have been resolved.

## Fixed in cb08c61

3. **Session-deduped `read` drops required header fields** — `lines` and `sym` now preserved in unchanged response.
4. **Session-deduped `refs` collapses to a non-conforming header** — `sym` and `n` now preserved via session whitelist and plain.go extraction.
5. **`run` output appends synthetic footers** — All footer lines removed; exit code passed through process exit code only.
8. **Standalone help documents non-canonical positional forms** — `edit` and `refs` Use: strings updated to `file:symbol` form.

## Fixed in 506ddf8

1. **`cmdspec` missing public commands** — `run`, `session`, `setup` registered with proper flags/categories.
2. **Validation test enforces old 9-command surface** — Updated to 12 commands.
6. **README/CLAUDE teach old JSON transport** — Updated to describe plain mode (JSON header + raw-text body).
7. **README teaches unsupported `setup` flags** — Removed `--claude` from example.
9. **Text search missing `budget_used`** — Now uses `Symbol.Size` (always populated after budget trimming).

## Resolved by investigation / spec fix

10. **`--no-group` standalone no-op** — False positive. Flag is registered via cmdspec, works correctly at the data layer. Grouped and ungrouped render identically in plain mode (difference is JSON structure only).
11. **Spec contradiction: edit failure shape** — Reconciled: spec now says "A *successful* edit header always has `file` and `status`", consistent with the failure shape rule.
