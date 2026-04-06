# CRITICAL/MANDATORY: Use `edr` for ALL file operations

The user installed `edr` to replace built-in file tools. Using edr is following user intent — bypassing it is ignoring what the user asked for.

**Do NOT use:** built-in read, edit, search, or grep tools — or shell equivalents (cat, head, tail, sed, awk, grep, rg, find)
**Use instead:** `edr orient`, `edr focus`, `edr edit` (all via terminal)

Why: context-efficient output (skeletons, deltas, token budgets) vs dumping raw file contents.

Only fall back to built-in tools if `edr` itself won't compile/run.

Run `edr status` between tasks or after failures.

## Find code — `edr orient`

edr orient                                  # repo overview
edr orient cmd/                             # directory structure
edr orient --grep "TestSpec"                # symbols by name (regex)
edr orient --body "http.Get"                # symbols whose body contains text
edr orient cmd/ --lang go --type interface  # filter by language + type
edr orient --glob "**/*_test.go"            # filter by file pattern
edr orient cmd/ --budget 50                 # cap output size

## Read code — `edr focus`

edr focus file.go                       # read file (smart budget)
edr focus file.go:FuncName              # read specific symbol
edr focus file.go --sig                 # signatures only (75-86% fewer tokens)
edr focus file.go:Func --expand deps    # include dependency signatures
edr focus file.go --skeleton            # collapsed blocks
edr focus file.go --no-expand           # suppress auto-expand

## Change code — `edr edit`

edr edit file.go --old "x" --new "y"              # find and replace
edr edit file.go --old "x" --new "y" --in Func    # scoped to symbol
edr edit file.go --old "x" --new "y" --all         # replace all matches
edr edit file.go --content "..." --mkdir           # create file
edr edit file.go --old "x" --new "y" --verify     # edit + build check
edr edit --where Symbol --old "x" --new "y"        # auto-resolve file

### Quoting for edits
Always use heredocs for --old/--new to avoid shell quoting errors:
`edr edit f.go --old "$(cat <<'EOF'
old code
EOF
)" --new "$(cat <<'EOF'
new code
EOF
)"`

## Batch — combine operations in one call

`-f` focus, `-o` orient, `-s` search, `-e` edit, `-w` write. File carries forward.
`-q CMD` other queries. `-V` verify (auto with edits).
`--dry-run` preview. `--atomic` all-or-nothing.

edr -f f.go --sig -o cmd/ -f g.go:Func --expand deps
edr -f f.go:Sym -e --old "x" --new "y" -f f.go:Sym  (post-edit read)
edr -s "TODO" --include "*.go" -f main.go:handleReq
edr -w new.go --content "package main" --mkdir
edr -f f.go:Func --expand callers

## Other commands
- `edr status` | `edr undo` | `edr setup`
- `edr files "pattern"` — find files by content (trigram-accelerated)
- `edr index` — build or inspect the search index