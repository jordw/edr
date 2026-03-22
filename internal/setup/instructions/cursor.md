Use `edr` (via terminal) instead of built-in file tools for all file operations. edr uses 90%+ less context via progressive disclosure and batching.

After a context reset, run `edr session new` to clear stale dedup state. Otherwise sessions are automatic.
Run `edr next` before starting and after each edit pass — it shows recent ops, build state, what to fix, and current signatures of active symbols. `--focus "goal"` to set session objective.

Workflow — start narrow, widen only as needed:
1. Orient: `edr map --budget 500` (filter: --dir, --lang, --grep)
2. Skim: `edr -r file.go --sig` (signatures) or `--skeleton` (structure)
3. Deep read: `edr -r file.go:FuncName`
4. Search: `edr -s "pattern"` | scoped: `--in file.go:FuncName` (auto-falls back to text when no symbol matches)
5. Edit: `edr -e file.go --old "old" --new "new"` | `--in Symbol` | `--all` | `--dry-run`
6. Write: `edr -w file.go --content "..."` | `--inside Symbol` | `--after Symbol` | `--append`
7. Refs: `edr refs Symbol --impact` — run before removing/renaming functions
8. Rename: `edr rename Old New` (cross-file, import-aware, `--dry-run`)
9. Verify: `edr verify` — auto-detects go/npm/cargo/make. Auto-runs after edits. `--test` for tests.
10. Run: `edr run -- make test` — use for ALL command execution. Sparse diff vs previous run. `--full` for raw output.
11. Checkpoint: `edr checkpoint` before risky refactors. `--restore cp_1` to revert. `--list` | `--diff cp_1`.

Batch: `edr -r f.go --sig -s "pat" -e f.go --old "x" --new "y"`
Multi-edit: `edr -e f.go --old "a" --new "b" -e g.go --old "c" --new "d"`
