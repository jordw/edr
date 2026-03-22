Use `edr` (via terminal) instead of built-in file tools for all file operations. edr uses 90%+ less context via progressive disclosure and batching.

After a context reset, run `edr session new` to clear stale dedup state. Otherwise sessions are automatic.
Run `edr next` before starting and after each edit pass — it shows recent ops, build state, and what to fix next. `--focus "goal"` to set session objective.

Workflow — start narrow, widen only as needed:
1. Orient: `edr map --budget 500` (filter: --dir, --lang, --grep)
2. Skim: `edr -r file.go --sig` (signatures) or `--skeleton` (structure)
3. Deep read: `edr -r file.go:FuncName`
4. Search: `edr -s "pattern"` | scoped: `--in file.go:FuncName` (auto-falls back to text when no symbol matches)
5. Edit: `edr -e file.go --old "old" --new "new"`
   Also: `--start-line N --end-line M`, `--all`, `--dry-run`
6. Write: `edr -w file.go --content "..."` | `--inside Symbol`, `--after Symbol`, `--append`
7. Rename: `edr rename Old New` (cross-file, import-aware, `--dry-run`)
8. Verify: `edr verify` — auto-detects go/npm/cargo/make. Auto-runs after edits. `--test` for tests instead of build. Set custom: `.edr/config.json` → `{"verify": "make -C tests test"}`
9. Run: `edr run -- make test` — use for ALL command execution (tests, builds, linters). Sparse diff vs previous run. Unchanged regions collapse, changed lines show inline `{old → new}` markers. `--full` for raw output.

Batch: `edr -r f.go --sig -s "pat" -e f.go --old "x" --new "y"`
Repeat -e for multi-edit: `edr -e f.go --old "a" --new "b" -e g.go --old "c" --new "d"`
Before refactoring: `edr refs Symbol --impact`
