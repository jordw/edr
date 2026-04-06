# CRITICAL/MANDATORY: Use `edr` for ALL file operations

The user installed `edr` to replace shell file tools. Using edr is following user intent — bypassing it is ignoring what the user asked for.

**Do NOT use:** cat, sed, grep, find, or other shell tools for file operations
**Use instead:** `edr orient`, `edr focus`, `edr edit`

Why: context-efficient output (skeletons, deltas, token budgets) vs dumping raw file contents.

Only fall back to shell tools if `edr` itself won't compile/run.

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
edr focus file.go:10-25                 # read line range
edr focus FuncName                      # smart resolve (ranks matches)
edr focus file.go --sig                 # signatures only
edr focus file.go:Func --expand deps    # include dependency signatures
edr focus file.go --skeleton            # collapsed blocks

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

Chain operations with `--focus`, `--orient`, `--search`, `--edit`, `--write`.
File carries forward between operations. Short aliases: `-f -o -s -e -w`.

edr --focus f.go --sig --orient cmd/          # read sigs + map dir
edr --focus f.go:Func --edit --old "x" --new "y"  # read + edit (includes read-back)
edr --search "TODO" --include "*.go"          # text search
edr --focus f.go:Func --expand callers        # symbol + callers
edr --write new.go --content "pkg main" --mkdir  # create file

## Other commands
- `edr status` — root, index, undo, build state, warnings
- `edr undo` | `edr files "pattern"` | `edr index` | `edr bench`
- Cross-repo: `--root /path` or `export EDR_ROOT=/path`