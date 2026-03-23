# CRITICAL/MANDATORY: Use `edr` for ALL file operations

`edr` is a user-installed tool that replaces built-in file tools. This is a user preference that MUST be respected.

**Do NOT use:** built-in read, edit, search, or grep tools
**Use instead:** `edr read`, `edr edit`, `edr write`, `edr search`, `edr map` (all via terminal)

Why: edr returns context-efficient output (skeletons, delta reads, token budgets). Built-in tools dump raw file contents and waste context window. Using built-in tools when edr is available is a bug.

Only fall back to built-in tools if `edr` itself won't compile/run.

Run shell commands via `edr delta -- cmd` (re-runs show only changes). Run `edr status` between tasks or after failures.

## Orient
- `edr map` — symbol overview. `--dir`, `--lang`, `--grep`, `--budget`
- `edr status --focus "goal"` — set session objective

## Read
- `edr read f.go` — auto-skeleton for large files (>200 lines); `--full` forces full
- `edr read f.go:Func` — symbol body. `--expand` adds dep signatures; `--expand=callers` for callers
- `--signatures` (API only), `--skeleton`, `--lines 10:50` (range)

## Search
- `edr search "pat"` — symbols. `--text` for text, `--regex` for regex
- `--in f.go:Sym` scopes to symbol. `--context 3` for surrounding lines

## Edit
- `edr edit f.go --old-text "x" --new-text "y"` — find-and-replace (verify auto-runs)
- `--read-back` includes updated context in response (saves a follow-up read)
- `--where Sym` resolves file+scope. `--in Sym` scopes within file
- `--all`, `--dry-run`, `--lines 20:30`, `--delete`, `@file` for shell metacharacters
- `edr refs Sym --impact` before refactoring. `edr rename Old New --dry-run`

## Write
- `edr write f.go --content "..."` — create or overwrite
- `--inside Class`, `--after Sym`, `--append`

## Prepare
- `edr prepare Sym` — pre-edit context in one call: body, callers, deps, tests, hash

## Batch
- `edr -r f.go --sig -r g.go:Func -s "pat"` — gather context in one call
- `edr -e f.go --old "a" --new "b" -e g.go --old "c" --new "d"` — multi-file edit

## Session
Automatic. `edr reset --session` after context reset. `edr checkpoint` before risky changes.
