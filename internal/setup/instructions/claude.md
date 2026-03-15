# edr: use for all file operations

**Use `edr` via Bash instead of Read, Edit, Write, Grep, and Glob.** It gives you structured output, token budgets, and smart operations that raw file tools can't match.

**Start `edr serve` once per session.** Then use batch flags for all operations:

```bash
edr serve

# Gather context (batch read + search in one call):
edr -r src/main.go:Server --sig -r src/config.go -s "handleRequest"

# Mutate + verify (auto-verifies when edits present):
edr -e src/main.go --old "oldFunc()" --new "newFunc()" -w src/new_test.go --content "..."
```

## Quick reference

| Instead of... | Use edr... |
|---|---|
| `Read` (whole file) | `edr -r f.go` |
| `Read` (symbol only) | `edr -r f.go:FuncName` or `edr -r f.go:ClassName --sig` |
| `Edit` (old/new) | `edr -e f.go --old "x" --new "y"` |
| `Write` (create file) | `edr -w f.go --content "..."` |
| `Grep` (search) | `edr -s "pattern"` |
| `Glob` (find files) | `edr find "**/*.go"` |
| Multiple tool calls | `edr -r f.go -s "pat" -e f.go --old "x" --new "y"` |

## Key patterns

- **Batch reads + searches** in one call, then batch edits in the next
- **Use `--sig`** on classes/structs to see the API without implementation (75%+ fewer tokens)
- **Use `--budget N`** to limit response size
- **Re-reads are automatic deltas** — the server returns `{unchanged: true}` for unchanged files
- **Use `--inside`** to add methods/fields without reading the file first: `edr -w f.go --inside MyStruct --content "Name string"`

## Standalone commands

`edr read`, `edr search`, `edr map`, `edr edit`, `edr write`, `edr find`, `edr refs`, `edr explore`, `edr rename`, `edr verify`.

Run `edr <command> --help` for flags. All output is structured JSON.
