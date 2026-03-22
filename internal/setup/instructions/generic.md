Use `edr` (via shell) instead of cat, sed, grep, and find for all file operations. 90%+ less context via progressive disclosure and batching.

Sessions are automatic. After a context reset, run `edr session new`.

`edr read f.go` | `f.go:Sym` | `--signatures` | `--skeleton` | `--lines 10:50`
`edr search "pat" --text` | `--in f.go:Sym` | `--context 3` | `--regex`
`edr edit f.go --old-text "x" --new-text "y"` | `--lines 20:30` | `--in Sym` | `--all` | `--dry-run`
`edr edit f.go:Sym --delete` | `--insert-at 20 --new-text "..."`
`edr write f.go --content "..."` | `--inside Sym` | `--after Sym` | `--append` | `--dry-run`
`edr map --budget 500` | `--dir src` | `--lang go` | `--grep pat`
`edr refs Sym --impact` — run before removing/renaming functions
`edr rename Old New --dry-run` — cross-file, import-aware
`edr verify` — auto-detects go/npm/cargo/make; auto-runs after edits
`edr run -- cmd` — sparse diff vs previous run; `--full` | `--reset`

Batch 2+ ops into one call — fewer roundtrips, less context:
`edr -r f.go --sig -r g.go:Func -s "pat" -e f.go --old "x" --new "y"`
Multi-edit: `edr -e f.go --old "a" --new "b" -e g.go --old "c" --new "d"`
Chained edit-then-read: `edr -e f.go --old "x" --new "y" -r f.go:200-210`
Shell metacharacters ($, backticks): use `--old-text @/tmp/old.txt` to read from file
