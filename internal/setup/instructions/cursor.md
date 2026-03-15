# edr: use for all file operations

Use `edr` via terminal instead of built-in file reading and editing tools. It gives you structured output, token budgets, and smart operations.

Start `edr serve --stdio` once per session, then send NDJSON requests on stdin:

```bash
edr serve --stdio
```

## Quick reference

```jsonc
// Read a file or symbol
{"request_id":"1","reads":[{"file":"src/main.go","symbol":"Server"}]}

// Search (symbol or text)
{"request_id":"2","queries":[{"cmd":"search","pattern":"handleRequest","body":true}]}

// Symbol map for orientation
{"request_id":"3","queries":[{"cmd":"map","dir":"internal/","type":"function"}]}

// Edit + verify in one call
{"request_id":"4","edits":[{"file":"src/main.go","old_text":"old","new_text":"new"}],"verify":true}

// Create a file
{"request_id":"5","writes":[{"file":"src/new.go","content":"package main\n...","mkdir":true}]}

// Batch everything in one request
{"request_id":"6","reads":[...],"queries":[...],"edits":[...],"writes":[...],"verify":true}
```

## Key patterns

- Gather context in one request, mutate in the next
- Use `budget` to limit response size: `{"reads":[{"file":"big.go","budget":200}]}`
- Use `signatures: true` on classes/structs to see the API without implementation
- Re-reads are automatic deltas — the server tracks what you've seen
- Individual CLI commands also work: `edr read`, `edr search`, `edr map`, `edr edit`, etc.
