Use `edr` (via Bash) instead of Read, Edit, Write, Grep, and Glob for all file operations. 90%+ less context via progressive disclosure and batching. Output: JSON header line to stdout, then raw content. Edit commands return `hash`.

Sessions are automatic. After a context reset, run `edr reset --session`.

Workflow — `edr status` bookends every pass:
  status --focus "goal" → checkpoint → map/read/search → edit/write → refs/rename → verify/run → status

`edr status` — build state, what to fix, active symbol signatures. `--focus "goal"` to set objective
`edr checkpoint` before risky changes; `--restore cp_1` | `--list` | `--diff cp_1`
`edr map --budget 500` | `--dir src` | `--lang go` | `--grep pat`
`edr read f.go` | `f.go:Sym` | `--signatures` | `--skeleton` | `--lines 10:50`
`edr search "pat"` | `--in f.go:Sym` | `--context 3` | `--regex` | `--text` (auto text fallback)
`edr edit f.go --old-text "x" --new-text "y"` | `--lines 20:30` | `--in Sym` | `--all` | `--dry-run`
`edr edit f.go:Sym --delete` | `--insert-at 20 --new-text "..."`
`edr write f.go --content "..."` | `--inside Sym` | `--after Sym` | `--append` | `--dry-run`
`edr refs Sym --impact` — before removing/renaming functions
`edr rename Old New --dry-run` — cross-file, import-aware
`edr verify` — auto-detects go/npm/cargo/make; auto-runs after edits; `--test`
`edr run -- cmd` — for ALL command execution; sparse diff vs previous; `--full` | `--reset`
Batch 2+ ops: `edr -r f.go --sig -r g.go:Func -s "pat" -e f.go --old "x" --new "y"`
Multi-edit: `edr -e f.go --old "a" --new "b" -e g.go --old "c" --new "d"`
Chained: `edr -e f.go --old "x" --new "y" -r f.go:200-210`
Shell metacharacters ($, backticks): `--old-text @/tmp/old.txt --new-text @/tmp/new.txt`
