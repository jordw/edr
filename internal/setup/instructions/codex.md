# edr: use for all file operations

Use `edr` for reading, editing, searching, and navigating code. It gives you structured JSON output, token budgets, and symbol-aware operations.

## Quick start

```bash
# Read a file or symbol
edr read src/main.go
edr read src/main.go:Server

# Search
edr search "handleRequest" --body
edr search "TODO" --text --include "*.go"

# Symbol map
edr map --dir internal/ --type function

# Edit + verify
edr edit src/main.go --old_text "old" --new_text "new"
edr verify

# Create a file
edr write src/new.go --content "package main"

# Find references
edr refs Server --impact
```

## Batch mode (more efficient)

Use batch flags to combine operations in one call:

```bash
# Gather context
edr -r src/main.go:Server --sig -s "handleRequest"

# Edit + verify
edr -e src/main.go --old "old" --new "new" -v
```

Use `--budget N` to limit response size. Use `--sig` on classes to see the API without bodies.
All output is structured JSON. Run `edr <command> --help` for details.
