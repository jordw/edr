Use `edr` (via terminal) instead of built-in file tools for all file operations. edr uses 90%+ less context via progressive disclosure and batching.

Run `export EDR_SESSION=$(date +%s)` in your first terminal call to enable cross-call caching.

Workflow — start narrow, widen only as needed:
1. Orient: `edr map --budget 500` (filter: --dir, --lang, --grep)
2. Skim: `edr -r file.go --sig` (signatures) or `--skeleton` (structure)
3. Deep read: `edr -r file.go:FuncName`
4. Search: `edr -s "pattern" --text` | scoped: `--in file.go:FuncName`
5. Edit: `edr -e file.go --old "old" --new "new"`
   Also: `--start-line N --end-line M`, `--all`, `--dry-run`
6. Write: `edr -w file.go --content "..."` | `--inside Symbol`, `--after Symbol`, `--append`
7. Rename: `edr rename Old New` (cross-file, import-aware, `--dry-run`)
8. Verify: `edr verify` — auto-detects go/npm/cargo/make. Auto-runs after edits.

Batch: `edr -r f.go --sig -s "pat" -e f.go --old "x" --new "y"`
Repeat -e for multi-edit: `edr -e f.go --old "a" --new "b" -e g.go --old "c" --new "d"`
Before refactoring: `edr refs Symbol --impact`
