# edr: use for all file operations

Use `edr` via terminal instead of built-in file reading and editing tools. It gives you structured JSON output, token budgets, and symbol-aware operations. If edr output includes `session_hint`, run that command once to enable delta reads across calls.

## Quick reference

```bash
# Read a file or symbol (--sig = signatures only, 75% fewer tokens)
edr -r src/main.go
edr -r src/main.go:Server --sig

# Search symbols (default) or text
edr -s "handleRequest"
edr -s "TODO" --text --include "*.go"

# Symbol map for orientation
edr map --dir internal/ --type function

# Edit + auto-verify in one call
edr -e src/main.go --old "old" --new "new"

# Create a file
edr -w src/new.go --content "package main" --mkdir

# Batch everything in one call
edr -r src/main.go --sig -s "pattern" -e src/main.go --old "x" --new "y"
```

## Key patterns

- Gather context in one call (`-r`, `-s`), mutate in the next (`-e`, `-w`)
- Use `--budget N` to limit response size
- Use `--sig` on classes/structs to see the API without implementation
- Use `--new -` with heredoc for multi-line replacements
- `-s "pattern"` searches symbol names; add `--text` for full-text grep
- Use `edr refs Symbol --impact` before refactoring

## If edr is not found

```bash
export PATH="$HOME/.local/bin:$PATH"
```
