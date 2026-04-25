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
edr orient --body "http.Get"                # body contains text
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

## Rename — `edr rename`

edr rename file.go:OldName --to NewName            # same-file rename (def + same-file callers)
edr rename file.go:OldName --to NewName --dry-run  # preview without applying
edr rename file.go:OldName --to NewName --cross-file # update callers across all files
edr rename file.go:Method --to New --cross-file --verify # build-check after; the only signal for interface conformance breaks

## References — `edr refs-to`

edr refs-to file.go:Func                         # inspect references before rename

After batches of edits or `git checkout`, run `edr index` before refs-to/rename — the header flags `stale_index: true` if you forget, but the count is already wrong by then.

## Cross-file move

edr edit file.go:Func --move-after other.go:Target  # move to another file

### Assertions (batch)
edr --edit f.go --old "Foo" --new "Bar" --assert-symbol-exists f.go:Bar

### Quoting for edits
Use heredocs or @file refs for --old/--new to avoid quoting errors:
`edr edit f.go --old "$(cat <<'EOF'
old code
EOF
)" --new "$(cat <<'EOF'
new code
EOF
)"`

## Python — multi-op scripts

For loops, filters, or 5+ chained ops. Session state shared via PID.

python3 <<EOF
import sys; sys.path.insert(0, "$(edr python-path)")
import edr
for s in edr.orient("internal/", grep="^run", type="function"):
    if not edr.files(s.name): print("unused:", s.name)
EOF

## Other commands
- `edr status` — root, index, undo, build state, warnings
- `edr undo` | `edr files "pattern"` | `edr index` | `edr bench`
- Cross-repo: `--root /path` or `export EDR_ROOT=/path`
