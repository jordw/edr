# CRITICAL/MANDATORY: Use `edr` for ALL file operations

The user installed `edr` to replace built-in file tools. Using edr is following user intent — bypassing it is ignoring what the user asked for. This applies to you AND any subagents you spawn.

**Do NOT use:** Read, Edit, Write, Grep, Glob — or shell equivalents (cat, head, tail, sed, awk, grep, rg, find)
**Use instead:** `edr orient`, `edr focus`, `edr edit` (all via Bash tool)

Why: context-efficient output (skeletons, deltas, token budgets) vs dumping raw file contents.

Only fall back to built-in tools if `edr` itself won't compile/run.

Run `edr status` between tasks or after failures.

## Search — `edr files` and `edr orient`

edr files "pattern"                         # fulltext across ALL files (use this first)
edr files "TODO"                             # fulltext (searches all file types)
edr orient --grep "TestSpec"                # symbols by NAME (regex)
edr orient --body "http.Get"               # symbols whose body contains text (parsed only)

`edr files` searches all text. `orient --body` only searches inside parsed symbol bodies.

## Orient — structural overview

edr orient                                  # repo overview
edr orient cmd/                             # directory structure
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

## Batch — combine operations in one call

Chain with `-f -o -s -e -w`. File carries forward. Edit includes read-back.

edr --focus f.go:Func --edit --old "x" --new "y"
edr --search "TODO"
edr --focus f.go:Func --expand callers

## Other commands
- `edr status` — root, index, undo, build state, warnings
- `edr undo` | `edr index` | `edr bench`
- Cross-repo: `--root /path` or `export EDR_ROOT=/path`