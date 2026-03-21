DO NOT use Read, Edit, Write, Grep, or Glob tools. Use `edr` via Bash instead:
- Read → `edr -r file.go` or `edr -r file.go:Symbol --sig`
- Edit → `edr -e file.go --old "x" --new "y"`
- Write → `edr -w file.go --content "..."`
- Grep/Glob → `edr -s "pattern" --text`
Combine: `edr -r f.go --sig -s "pat" -e f.go --old "x" --new "y"`. Run `edr --help` for all options.
