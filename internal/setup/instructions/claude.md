# CRITICAL/MANDATORY: Use `edr` for ALL file operations

The user installed `edr` specifically to replace built-in file tools. Using edr is following user intent — bypassing it (with built-in tools or shell equivalents) is ignoring what the user asked for. This applies to you AND any subagents you spawn.

**Do NOT use:** Read, Edit, Write, Grep, Glob — or shell equivalents (cat, head, tail, sed, awk, grep, rg, find)
**Use instead:** `edr read`, `edr edit`, `edr write`, `edr search`, `edr map` (all via Bash tool)

Why: context-efficient output (skeletons, deltas, token budgets) vs dumping raw file contents.

Only fall back to built-in tools if `edr` itself won't compile/run.

Run shell commands via `edr delta -- cmd`. Run `edr status` between tasks or after failures.

## Batch — tool calls are expensive, plan and combine

`-r` read, `-s` search, `-m` map, `-q` query (refs/prepare), `-e` edit, `-w` write. Modifier flags follow each op. Plan what you need, then combine into one call for the greatest speedup.

`edr -r f.go --sig -r g.go:Func --expand -s "pattern" --text -m --dir cmd/`
`edr -q refs Sym --impact -q prepare f.go:Sym` (batch refs/prepare)
`edr -r f.go:Sym -e f.go --old "x" --new "y" -r f.go:Sym` (post-edit read)
`edr -e f.go --old "a" --new "b" -e g.go --old "c" --new "d"` (multi-file)
`edr -w f.go --content "..." --mkdir`, `-V` verify (auto after edits)

### Read modifiers (after `-r`)
`--sig` (file/container only), `--skeleton`, `--full`, `--expand[=deps]`, `--symbols`, `--lines 10:50`, `--budget N`

### Search modifiers (after `-s`)
`--text`, `--regex`, `--context N`, `--in f.go:Sym`, `--include "*.go"`, `--limit N`

### Query modifiers (after `-q`)
`--impact`, `--callers`, `--deps`, `--chain Sym`, `--depth N`, `--signatures`, `--body`, `--budget N`

### Edit modifiers (after `-e`)
`--old "x" --new "y"`, `--where Sym` (resolves file), `--in Sym` (scope)
`--all`, `--delete`, `--dry-run`, `--fuzzy`, `--read-back`
Quoting: use heredocs for quotes/backslashes: `--old "$(cat <<'EOF'`...`EOF`)"`

### Write modifiers (after `-w`)
`--content "..."`, `--inside Class`, `--after Sym`, `--append`, `--mkdir`

## Standalone commands
- `edr map` — symbol overview. `--dir`, `--lang`, `--grep`, `--budget` (`map --dir path` for directories; read is file-only)
- `edr prepare file:Sym` — pre-edit context: body, callers, deps (batchable via `-q prepare`)
- `edr refs Sym --impact` — find callers, transitive impact (batchable via `-q refs`)
- `edr rename Old New --dry-run` | `edr rename --text "old" "new"` — `--word`, `--include`
- `edr verify` | `edr verify --test` | `edr verify --command "cmd"`
- `edr delta -- cmd` — diff vs last run. `--reset`, `--full`
- `edr status` | `edr undo` | `edr reset --session`
