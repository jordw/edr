Use `edr` (via terminal) instead of built-in file tools for all file operations. edr uses 90%+ less context via progressive disclosure and batching.

Workflow — start narrow, widen only as needed:
1. Orient: `edr map --budget 500` — see all symbols across files
2. Skim: `edr -r file.go --sig` — signatures only (75-85% smaller than full read)
3. Deep read: `edr -r file.go:FuncName` — read one symbol's full body
4. Search: `edr -s "pattern" --text` — search across codebase
5. Edit: `edr -e file.go --old "exact old text" --new "new text"`
6. Write: `edr -w file.go --content "..."`

Batch multiple ops in one call to save round-trips:
`edr -r f.go --sig -s "pat" -e f.go --old "x" --new "y"`

Before refactoring, check callers: `edr refs Symbol --impact`
