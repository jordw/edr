# Development

## Quick Reference

- **Build:** `go build -o edr .`
- **Lint:** `go vet ./...`
- **Test:** `go test ./...`
- **Requires:** Go 1.24+
- **Parsing:** pure-Go, lexer-based parsers run on demand; `edr index` optionally builds trigram, symbol, import, and reference indexes for faster repo-scale queries.

## Version Embedding

The `setup.sh` script injects version metadata via ldflags. For manual builds:

```bash
go build -ldflags "-X github.com/jordw/edr/cmd.Version=$(git describe --tags --always) -X github.com/jordw/edr/cmd.BuildHash=$(git rev-parse --short HEAD)" -o edr .
```

## Platform Support

edr is developed and tested on Linux and macOS. Windows is not currently supported — file locking and mmap/index paths use Unix-specific syscalls, and the `verify` command shells through `sh -c`.
