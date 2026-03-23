# Development

## Quick Reference

- **Build:** `go build -o edr .`
- **Lint:** `go vet ./...`
- **Test:** `go test ./...`
- **Requires:** Go 1.24+, C compiler (for tree-sitter grammars)
- **Parsing:** on-demand via tree-sitter, no pre-built index. 

## Version Embedding

The `setup.sh` script injects version metadata via ldflags. For manual builds:

```bash
go build -ldflags "-X github.com/jordw/edr/cmd.Version=$(git describe --tags --always) -X github.com/jordw/edr/cmd.BuildHash=$(git rev-parse --short HEAD)" -o edr .
```

## Platform Support

edr is developed and tested on Linux and macOS. Windows is not currently supported — file locking uses `syscall.Flock` and the `verify` command shells through `sh -c`.
