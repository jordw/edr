# edr: use for all file operations

Use `edr` via terminal instead of built-in file reading and editing tools. It gives you structured output, token budgets, and smart operations.

## Quick reference

```bash
# Read a file or symbol
edr read src/main.go
edr -r src/main.go:Server --sig

# Search (symbol or text)
edr search "handleRequest" --body
edr -s "TODO" --text --include "*.go"

# Symbol map for orientation
edr map --dir internal/ --type function

# Edit + verify in one call
edr -e src/main.go --old "old" --new "new" -v

# Create a file
edr write src/new.go --content "package main" --mkdir

# Batch everything in one call
edr -r src/main.go --sig -s "pattern" -e src/main.go --old "x" --new "y"
```

## Key patterns

- Gather context in one call, mutate in the next
- Use `--budget N` to limit response size
- Use `--sig` on classes/structs to see the API without implementation
- Individual CLI commands also work: `edr read`, `edr search`, `edr map`, `edr edit`, etc.
