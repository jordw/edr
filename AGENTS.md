## Cursor Cloud specific instructions

This is a pure Go CLI tool (`edr`) with zero external service dependencies. Go 1.25.0 is required (specified in `go.mod`).

### Build / Lint / Test

- **Build:** `go build -o edr .`
- **Lint:** `go vet ./...`
- **Test:** `go test ./...` (no test files exist yet; the command exits cleanly)

### Running the CLI

After building, the binary is at `./edr`. It operates on the current directory by default (override with `-r <path>`).

- `./edr init` — indexes the repo into `.edr/index.db` (SQLite, embedded, no external DB needed)
- `./edr repo-map` — full symbol map
- `./edr search <pattern>` — symbol search
- See `CLAUDE.md` for the full command reference.

### Notes

- The `.edr/` directory is created at the repo root on first index; it is local state and should be gitignored.
- SQLite uses pure-Go bindings (`modernc.org/sqlite`); no CGO needed for DB.
- Tree-sitter grammars require CGO (C compiler) for parsing.
