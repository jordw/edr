IMPORTANT: Use `edr` (via Bash) instead of Read, Edit, Write, Grep, and Glob for ALL file operations. Do NOT use built-in file tools — edr provides the same operations with 90%+ less context. Built-in tools dump entire files into your context; edr reads only what you need and tracks what you've seen.

Do NOT run shell commands directly — use `edr delta -- cmd` for ALL command execution (tests, builds, scripts). Re-runs only return what changed, not the full output again. `--reset` clears the baseline.

ALWAYS run `edr context` between tasks, after failures, and when unsure what to do next. It returns build pass/fail, modified files, stale assumptions, and suggested next actions.

## Orient
- `edr map` — symbol overview. `--dir src`, `--lang go`, `--grep pat`, `--budget 500`
- `edr context --focus "goal"` — set session objective

## Read
- `edr read f.go` — whole file. `edr read f.go:Func` — one symbol
- `--signatures` (API only), `--skeleton` (structure), `--lines 10:50` (range)

## Search
- `edr search "pat"` — symbols. `--text` for text, `--regex` for regex
- `--in f.go:Sym` scopes to symbol. `--context 3` for surrounding lines

## Edit
- `edr edit f.go --old-text "x" --new-text "y"` — find-and-replace, auto-verifies build
- `--in Sym` scopes match. `--all` replaces all. `--dry-run` previews. `--lines 20:30` for range
- `edr edit f.go:Sym --delete` removes a symbol
- Shell metacharacters: `--old-text @/tmp/old.txt --new-text @/tmp/new.txt`

## Write
- `edr write f.go --content "..."` — create or overwrite
- `--inside Class` (add method), `--after Sym` (insert after), `--append` (end of file)

## Refactor
- `edr refs Sym --impact` — find all references before changing a symbol
- `edr rename Old New --dry-run` — cross-file, import-aware

## Verify
- `edr verify` — auto-detects build system; runs after edits. `--test` for tests

## Batch (`-r` read, `-s` search, `-e` edit, `-w` write)
- `edr -r f.go --sig -r g.go:Func -s "pat"` — gather context in one call
- `edr -e f.go --old "a" --new "b" -e g.go --old "c" --new "d"` — multi-file edit
- `edr -e f.go --old "x" --new "y" -r f.go:200-210` — edit then verify

## Session
Automatic. After context reset: `edr reset --session`
`edr checkpoint` before risky changes; `--restore cp_1`, `--list`, `--diff cp_1`
