# edr — code editing tools for agents

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

**edr gives coding agents code-aware file tools that front-load the context needed for the next step.**

Instead of raw files and grep output, edr returns structured code context:

- **`orient`** — budgeted structural overview of the codebase in terms of symbols and files.
- **`focus file:Symbol`** — reads a symbol, not the whole file. Includes relevant surrounding context.
- **`focus SymbolName`** — resolves likely matches and opens the best candidate.
- **`edit --old X --new Y --verify`** — diff, updated context, and verification feedback.
- **`edr --orient cmd/ --focus file:Sym --edit ...`** — survey, inspect, and mutate in one call when needed.
- Repeated reads are deduplicated so agents do less work.

Fully local, shell-friendly, no telemetry. Designed to replace generic file operations with agent-oriented ones.

## Install

```bash
brew install jordw/tap/edr
edr setup
```

`edr setup` installs agent instructions to your global config (`~/.claude/CLAUDE.md`, `~/.cursor/rules/edr.mdc`, or `~/.codex/AGENTS.md`). Instructions auto-update when edr is rebuilt. They teach the agent to use edr instead of built-in file tools. Session data is stored in `~/.edr/repos/`, not in the project directory.

<details>
<summary>Other install methods</summary>

**Pre-built binary** ([review the script first](install.sh)):

```bash
curl -fsSL https://raw.githubusercontent.com/jordw/edr/main/install.sh | sh
```

**From source** (requires Go 1.24+):

```bash
go install github.com/jordw/edr@latest
edr setup
```

**Cloud agents and CI** (installs Go if needed, builds):

```bash
git clone https://github.com/jordw/edr.git && ./edr/setup.sh
```

</details>

## Example

A raw read of `dispatch.go` is 965 lines. Here's what `edr focus` returns for the symbol you actually care about — the function body plus signatures of the helpers it calls:

```
$ edr focus internal/dispatch/dispatch.go:Dispatch
{"file":"internal/dispatch/dispatch.go","sym":"Dispatch","lines":[79,123]}
func Dispatch(ctx context.Context, db index.SymbolStore, cmd string, args []string, flags map[string]any) (any, error) {
	root := db.Root()
	setRootOnce.Do(func() { output.SetRoot(root) })

	var result any
	var err error

	switch cmd {
	case "orient", "map":
		result, err = runMapUnified(ctx, db, root, args, flags)
	case "focus", "read":
		result, err = runReadUnified(ctx, db, root, args, flags)
	case "edit":
		if flagString(flags, "content", "") != "" && flagString(flags, "old_text", "") == "" {
			result, err = runWriteUnified(ctx, db, root, args, flags)
		} else {
			result, err = runSmartEdit(ctx, db, root, args, flags)
		}
	...
	}
	return result, nil
}

--- deps ---
dispatch_edit.go    func runSmartEdit(...) (any, error)
dispatch_search.go  func runSearchUnified(...) (any, error)
dispatch_verify.go  func runVerify(...) (any, error)
```

45 lines of body, 5 signatures, one JSON header. The agent gets the function it asked for plus enough surrounding API to reason about edits — without pulling in the other 920 lines.

Typical workflow: orient → focus → edit:

```bash
edr orient --dir internal/dispatch/            # structural overview
edr focus internal/dispatch/dispatch.go:Dispatch   # just the function
edr edit internal/dispatch/dispatch.go \
    --old 'case "search":' --new 'case "search", "find":' --verify
```

**Batched:** gather everything in one call, mutate in one call:

```bash
# 1. Orient + focus on three APIs, one call
edr -o --dir internal/dispatch/ \
    -f internal/dispatch/dispatch.go:Dispatch --sig \
    -f internal/dispatch/dispatch_edit.go:runSmartEdit \
    -f internal/dispatch/dispatch_search.go:runSearchUnified --sig

# 2. Edit two files, verify at the end
edr -e internal/dispatch/dispatch.go --old '"search"' --new '"search", "find"' \
    -e internal/dispatch/dispatch_search.go --old 'runSearchUnified' --new 'runSearchUnified' --all
```

## How it works

Without edr, agents grep to find code, read line ranges, guess what's relevant, edit, then re-read to check. Each step is a separate tool call returning raw text the agent has to filter. edr replaces that with code-aware primitives:

| Operation | What the agent gets |
|---|---|
| `orient` | Budgeted structural overview (symbols and files) |
| `focus file:Symbol` | Symbol body + dependency signatures |
| `focus SymbolName` | Ranked resolution, auto-opens best match |
| `edit --old X --new Y --verify` | Diff + updated context + verification feedback |
| Re-read unchanged file | Deduplicated (zero output, zero waste) |

Under the hood:

- **Symbol extraction** is pure-Go regex per language — no CGO, no build step, works on broken code.
- **Sessions** track what the agent has already seen and return only what changed on re-reads.
- **Indexing** is optional. `edr index` builds a trigram + symbol index; on the Linux kernel (93K files), indexed operations complete in 0.02–0.5s. Without an index, files are parsed on demand.
- **Edits** use span-based transactions with a TOCTOU hash guard, optional build verification, and auto-checkpointed undo.

## Commands

### Primary commands

| Command | Description |
|---|---|
| `orient [path]` | Structural overview of a directory or project (replaces `map`) |
| `focus file[:Symbol]` | Read file or symbol with context (replaces `read`) |
| `edit file` | Edit, write, create files. `--verify` to check build. |
| `status` | Repo root, index coverage, undo, build state, warnings |
| `undo` | Revert last edit/write (auto-checkpointed) |
| `files "pattern"` | Find files containing text (trigram-accelerated) |
| `index` | Build or inspect the search index |
| `bench` | Benchmark operations on current repo |
| `setup` | Install agent instructions |

Old command names `map` and `read` still work as aliases for `orient` and `focus`.

### Batch flags

Chain operations with `--focus`, `--orient`, `--search`, `--edit`, `--write` (short: `-f -o -s -e -w`). File carries forward. Edit includes read-back automatically.

```bash
edr --orient cmd/ --focus file:Sym --sig
edr --focus file:Func --edit --old "x" --new "y"
edr --search "TODO" --include "*.go"
edr --focus file:Func --expand callers
```

### Cross-repo targeting

```bash
edr focus file:Symbol --root /path/to/repo
export EDR_ROOT=/path/to/repo    # set once, all commands use it
```

Full flag reference: `edr --help` or `edr <command> --help`.

## Languages

edr reads and edits any text file. Symbol-aware features (symbol reads, `--signatures`, `map`) require a supported language:

**Symbol parsing:** Go, Python, JavaScript/JSX, TypeScript/TSX, Rust, Java, C, C++, Ruby

## Limitations

- **Structural navigation, not code analysis.** edr finds functions and classes by pattern, not by parsing or type-checking. This means it works instantly, on broken code, with zero config — but it does not have type information. It may miss unusual syntax (e.g. deeply nested anonymous functions).
- **macOS and Linux only.** Windows is not planned.
- **Pure Go.** No CGO, no C compiler needed. Single ~6MB binary.

## License

[MIT](LICENSE)
