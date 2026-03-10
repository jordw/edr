# EDR Development

See [CLAUDE.md](CLAUDE.md) for full command reference and MCP tool documentation.

## Quick Reference

- **Build:** `go build -o edr .`
- **Lint:** `go vet ./...`
- **Test:** `go test ./...`
- **Requires:** Go 1.25+, C compiler (for tree-sitter grammars)
- **SQLite:** pure-Go bindings (`modernc.org/sqlite`), no CGO needed for DB
- **Index:** stored in `.edr/index.db`, created on first query (`./edr init` to force)
