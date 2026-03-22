Use `edr` (via terminal) instead of built-in file tools for all file operations. edr uses 90%+ less context via progressive disclosure and batching.

After a context reset, run `edr reset --session` to clear stale dedup state. Otherwise sessions are automatic.

Workflow — `edr status` bookends every pass:
  status --focus "goal" → checkpoint → map/read/search → edit/write → refs/rename → verify/run → status

`edr status` — build state, what to fix, active symbol signatures. `--focus "goal"` to set objective
`edr checkpoint` before risky changes; `--restore cp_1` | `--list` | `--diff cp_1`
`edr map --budget 500` (filter: --dir, --lang, --grep)
`edr read f.go` | `f.go:Sym` | `--signatures` | `--skeleton` | `--lines 10:50`
`edr search "pat"` | `--in f.go:Sym` | `--context 3` | `--regex` | `--text` (auto text fallback)
`edr edit f.go --old-text "x" --new-text "y"` | `--lines 20:30` | `--in Sym` | `--all` | `--dry-run`
`edr edit f.go:Sym --delete` | `--insert-at 20 --new-text "..."`
`edr write f.go --content "..."` | `--inside Sym` | `--after Sym` | `--append` | `--dry-run`
`edr refs Sym --impact` — before removing/renaming functions
`edr rename Old New --dry-run` — cross-file, import-aware
`edr verify` — auto-detects go/npm/cargo/make; auto-runs after edits; `--test`
`edr run -- cmd` — for ALL command execution; sparse diff vs previous; `--full` | `--reset`

Batch: `edr -r f.go --sig -s "pat" -e f.go --old "x" --new "y"`
Multi-edit: `edr -e f.go --old "a" --new "b" -e g.go --old "c" --new "d"`
