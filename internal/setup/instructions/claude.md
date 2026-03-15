# edr: use for all file operations

**Use `edr` via Bash instead of Read, Edit, Write, Grep, and Glob.** It gives you structured JSON output, token budgets, and symbol-aware operations that raw file tools can't match.

**Set up a session once per conversation** — re-reading unchanged files returns `{unchanged: true}` instead of the full content:

```bash
export EDR_SESSION=$(uuidgen)
```

**Use batch flags for all operations:**

```bash
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
| `Grep` (search text) | `edr -s "pattern" --text` |
| `Grep` (search symbols) | `edr -s "pattern"` |
| Multiple tool calls | `edr -r f.go -s "pat" -e f.go --old "x" --new "y"` |

## Key patterns

- **Batch reads + searches** in one call, then batch edits in the next
- **Use `--sig`** on classes/structs to see the API without implementation (75%+ fewer tokens)
- **Use `--budget N`** to limit response size
- **Use `--inside`** to add methods/fields without reading the file first: `edr -w f.go --inside MyStruct --content "Name string"`
- **Use `--new -`** with heredoc for multi-line replacements:

```bash
edr -e src/config.go:parseConfig --new - <<'EOF'
func parseConfig() (*Config, error) {
    // new implementation
}
EOF
```

- **`-s "pattern"`** searches symbol names by default. Add **`--text`** for full-text grep.
- Use `edr refs Symbol --impact` before refactoring to see all transitive callers.
- Use `edr map --budget 500` to orient in an unfamiliar codebase.

## Output format

All output is JSON. Edit responses include `status` and `hash` for verification:

```json
{"status": "applied", "hash": "a1b2c3d4", "file": "src/main.go"}
```

Re-reading an unchanged file with an active session returns:

```json
{"file": "src/main.go", "unchanged": true}
```

## If edr is not found

```bash
export PATH="$HOME/.local/bin:$PATH"
```
