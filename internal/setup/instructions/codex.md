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

Start `edr serve --stdio` and send NDJSON requests:

```jsonc
// Gather context
{"request_id":"1","reads":[{"file":"src/main.go","symbol":"Server"}],"queries":[{"cmd":"search","pattern":"handleRequest","body":true}]}

// Edit + verify
{"request_id":"2","edits":[{"file":"src/main.go","old_text":"old","new_text":"new"}],"verify":true}
```

Use `budget` to limit response size. Use `signatures: true` on classes to see the API without bodies.
All output is structured JSON. Run `edr <command> --help` for details.
