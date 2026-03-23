ALL file operations MUST go through `edr` (via Bash tool). Do not use Read, Edit, Write, Grep, or Glob — edr replaces all of them. If an edr command fails, fix the command and retry; only fall back to built-in tools if edr itself won't run.

Run shell commands through `edr delta -- cmd` (re-runs return only what changed). `--reset` clears the baseline.

Run `edr context` between tasks, after failures, and when unsure what to do next.

## Orient
- `edr map` — symbol overview. `--dir src`, `--lang go`, `--grep pat`, `--budget 500`
- `edr context --focus "goal"` — set session objective

## Read (replaces: Read tool)
- `edr read f.go` — whole file. `edr read f.go:Func` — one symbol
- `--signatures` (API only), `--skeleton` (structure), `--lines 10:50` (range)

## Search (replaces: Grep, Glob tools)
- `edr search "pat"` — symbols. `--text` for text, `--regex` for regex
- `--in f.go:Sym` scopes to symbol. `--context 3` for surrounding lines

## Edit (replaces: Edit tool)
- `edr edit f.go --old-text "x" --new-text "y"` — find-and-replace, auto-verifies
- `--where Sym` resolves file+scope from index. `--in Sym` scopes within a file
- `--all` replaces all. `--dry-run` previews. `--lines 20:30` for range
- `edr edit f.go:Sym --delete` removes a symbol
- Shell metacharacters: use `@file` indirection

## Write (replaces: Write tool)
- `edr write f.go --content "..."` — create or overwrite
- `--inside Class` (add method), `--after Sym` (insert after), `--append` (end of file)

## Refactor
- `edr refs Sym --impact` — find all references before changing a symbol
- `edr rename Old New --dry-run` — cross-file, import-aware

## Verify
- `edr verify` — auto-detects build system. `--test` for tests

## Batch (`-r` read, `-s` search, `-e` edit, `-w` write)
- `edr -r f.go --sig -r g.go:Func -s "pat"` — gather context in one call
- `edr -e f.go --old "a" --new "b" -e g.go --old "c" --new "d"` — multi-file edit
- `edr -e f.go --old "x" --new "y" -r f.go:200-210` — edit then verify

## Session
Automatic. After context reset: `edr reset --session`
`edr checkpoint` before risky changes; `--restore cp_1`, `--list`, `--diff cp_1`
