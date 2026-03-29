# CRITICAL/MANDATORY: Use `edr` for ALL file operations

The user installed `edr` specifically to replace shell file tools. Using edr is following user intent — bypassing it with shell tools (cat, sed, grep, find, etc.) is ignoring what the user asked for.

**Do NOT use:** cat, sed, grep, find, or other shell tools for file operations
**Use instead:** `edr orient`, `edr focus`, `edr edit`

Why: context-efficient output (skeletons, deltas, token budgets) vs dumping raw file contents.

Only fall back to shell tools if `edr` itself won't compile/run.

Run `edr status` between tasks or after failures.

## 3 core commands

- `edr orient [path]` — structural overview of a directory or project
- `edr focus file[:Symbol]` — read file or symbol with context
- `edr edit file` — edit, write, create files (auto-verifies build)

## Batch — tool calls are expensive, plan and combine

`-f` focus, `-o` orient, `-e` edit. Modifier flags follow each op. Plan what you need, then combine into one call for the greatest speedup.

`edr -f f.go --sig -f g.go:Func --expand -o --dir cmd/`
`edr -f f.go:Sym -e f.go --old "x" --new "y" -f f.go:Sym` (post-edit read)
`edr -e f.go --old "a" --new "b" -e g.go --old "c" --new "d"` (multi-file)
`edr -e f.go --content "..." --mkdir` (create/write via edit)

### Focus modifiers (after `-f`)
`--sig` (file/container only), `--skeleton`, `--full`, `--expand[=deps]`, `--symbols`, `--lines 10:50`, `--budget N`

### Orient modifiers (after `-o`)
`--dir`, `--lang`, `--grep`, `--glob`, `--type`, `--search`, `--budget N`

### Edit modifiers (after `-e`)
`--old "x" --new "y"`, `--where Sym` (resolves file), `--in Sym` (scope)
`--content "..."`, `--inside Class`, `--after Sym`, `--append`, `--mkdir`
`--all`, `--delete`, `--dry-run`, `--fuzzy`, `--read-back`, `--no-verify`

### Quoting for edits
Always use heredocs for --old/--new to avoid shell quoting errors:
`edr edit f.go --old "$(cat <<'EOF'
old code
EOF
)" --new "$(cat <<'EOF'
new code
EOF
)"`

## Standalone commands
- `edr status` | `edr undo` | `edr setup`
