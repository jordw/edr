# Contributing to edr

Bug reports and pull requests are welcome on [GitHub](https://github.com/jordw/edr/issues).

## Development setup

```bash
git clone https://github.com/jordw/edr.git && cd edr
go build -o edr .       # build (requires Go 1.25+ and a C compiler)
go test ./...            # run all tests
```

After changing Go source files, rebuild with `go build -o edr . && go install`.

## Submitting changes

1. Fork the repo and create a branch from `main`
2. Make your changes — keep PRs focused on a single concern
3. Add or update tests for any changed behavior
4. Ensure `go vet ./...` and `go test ./...` pass
5. Open a pull request with a clear description of what changed and why

## Reporting bugs

Open an issue with:
- What you ran (command or batch flags)
- What you expected
- What actually happened
- Go version (`go version`) and OS

## Project structure

```
cmd/           CLI commands, batch orchestrator
internal/
  cmdspec/     canonical command registry (names, categories, flags)
  index/       tree-sitter parsing, SQLite symbol index
  search/      symbol and text search (parallel, cached)
  edit/        file edits, transactions, diffing
  dispatch/    command routing and execution
  gather/      context collection with token budgets
  session/     file-backed session state (deltas, body dedup)
  trace/       session tracing and benchmarks
  output/      structured JSON formatting
bench/         benchmarks and multi-language test suite
```

## Code style

- Follow standard Go conventions (`gofmt`, `go vet`)
- Keep functions focused — prefer small, testable units
- Structured JSON output for all commands (see `internal/output/`)
- Tests live next to the code they test (`*_test.go`)
