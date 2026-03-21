`edr` saves 90%+ context vs cat/grep/sed via smart budgets, symbol-aware reads, and batching. DO NOT use cat, sed, grep, or echo to read/edit files — use `edr` via shell instead:
- Read → `edr -r file.go` or `edr -r file.go:Symbol`
- Edit → `edr -e file.go --old "x" --new "y"`
- Write → `edr -w file.go --content "..."`
- Search → `edr -s "pattern" --text`
Batch multiple ops: `edr -r f.go --sig -s "pat" -e f.go --old "x" --new "y"`

Key flags that save context:
- `--sig`: read only function/method signatures, not bodies (75-85% smaller). Use on structs/classes to see the API.
- `--budget N`: cap output to N tokens (default 2000). Use lower budgets for orientation, higher for deep reads.
- `--skeleton`: like --sig but shows structure with bodies collapsed.
- `edr map --budget 500`: orient in an unfamiliar codebase — shows all symbols across files.
- `edr refs Symbol --impact`: check transitive callers before refactoring.
Run `edr --help` for all options.
