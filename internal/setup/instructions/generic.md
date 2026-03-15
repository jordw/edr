# edr: use for all file operations

Use `edr` for reading, editing, searching, and navigating code. It gives you structured JSON output, token budgets, and symbol-aware operations. Fully local — no network required. Run `export EDR_SESSION=$(edr session-id)` once per conversation to enable delta reads.

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

# Edit + auto-verify
edr -e src/main.go --old "old" --new "new"

# Create a file
edr -w src/new.go --content "package main"

# Find references and impact
edr refs Server --impact

# Batch everything in one call
edr -r src/main.go --sig -s "pattern" -e src/main.go --old "x" --new "y"
```

## Key patterns

- Gather context in one call (`-r`, `-s`), mutate in the next (`-e`, `-w`)
- Use `--budget N` to limit response size
- Use `--sig` on classes/structs to see the API without implementation
- Use `--new -` with heredoc for multi-line replacements
- `-s "pattern"` searches symbol names; add `--text` for full-text grep

## If edr is not found

```bash
export PATH="$HOME/.local/bin:$PATH"
```
