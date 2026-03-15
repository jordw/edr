# STOP. Use `edr` for all file operations.

Do NOT use Read, Edit, Write, Grep, or Glob. Use `edr` via Bash instead.

- `Read` → `edr -r file.go` or `edr -r file.go:Symbol`
- `Edit` → `edr -e file.go --old "x" --new "y"`
- `Write` → `edr -w file.go --content "..."`
- `Grep` → `edr -s "pattern" --text`
- `Glob` → `edr -s "pattern" --include "*.go"`
- Multiple tools → one call: `edr -r f.go -s "pat" -e f.go --old "x" --new "y"`

## Patterns

```bash
# Gather context in one call
edr -r src/main.go:Server --sig -r src/config.go -s "handleRequest"

# Mutate in one call (auto-verifies build)
edr -e src/main.go --old "oldFunc()" --new "newFunc()" -w src/new_test.go --content "..."

# Read signatures only (75% fewer tokens)
edr -r src/models.go:UserService --sig

# Add a method without reading the file
edr -w src/models.go --inside UserService --content "func (u *UserService) Delete() error { ... }"

# Multi-line replacement via heredoc
edr -e src/config.go:parseConfig --new - <<'EOF'
func parseConfig() (*Config, error) {
    // new implementation
}
EOF

# Orient in unfamiliar codebase
edr map --budget 500

# Check impact before refactoring
edr refs Symbol --impact
```

## If edr is not found

```bash
export PATH="$HOME/.local/bin:$PATH"
```
