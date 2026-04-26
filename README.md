# edr — agent-native code editing tools

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

**edr gives coding agents code-aware file tools that front-load the context needed for the next step.**

Instead of raw files and grep output, edr gives agents a small set of code-aware primitives organized by what they're trying to do:

**Read — structured context, not raw bytes.**
- `orient` — structural overview, budgeted by symbols and files.
- `focus file:Symbol` (or just `SymbolName`) — symbol body plus its dependency signatures.
- `refs-to file:Symbol` — references for impact analysis.
- `files "pattern"` — trigram-accelerated text search.

**Write — scope-aware mutations with safety nets.**
- `edit --old X --new Y --verify` — diff, updated context, build verification.
- `rename file:Symbol --to New --cross-file --verify` — scope-aware rename; the `mode` field flags `scope` vs `name-match` so you know what you got.

**Workflow — designed for how agents actually call tools.**
- Chain operations in one call (`edr -o ... -f ... -e ...`).
- Repeated reads deduplicated, delta-only on changes.
- Auto-checkpointed undo.

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

Ask for one function and `edr focus` returns it *plus* the signatures of every helper it calls — so the agent has enough API in hand to reason about an edit without a second read:

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
dispatch_index.go   func runIndex(...) (any, error)
dispatch_files.go   func runFiles(...) (any, error)
```

The agent asked for `Dispatch`. It also got back signatures for `runSmartEdit`, `runSearchUnified`, `runVerify`, `runIndex`, and `runFiles` — every helper the function calls — from the files they actually live in. No grep, no guessing, no second tool call. (And as a side effect, 45 lines of body beats dumping the whole 965-line file.)

Typical workflow: orient → focus → edit:

```bash
edr orient --dir internal/dispatch/            # structural overview
edr focus internal/dispatch/dispatch.go:Dispatch   # just the function
edr edit internal/dispatch/dispatch.go \
    --old 'case "search":' --new 'case "search", "find":' --verify
```

Rename a symbol across the repo with the build as the safety net:

```
$ edr rename internal/dispatch/dispatch_edit.go:runSmartEdit --to runSmartEditDispatch --cross-file --verify
{"status":"applied","to":"runSmartEditDispatch","mode":"scope","n":2,"code":2,"from":"runSmartEdit"}
--- a/internal/dispatch/dispatch.go
+++ b/internal/dispatch/dispatch.go
@@ -106,7 +106,7 @@
-			result, err = runSmartEdit(ctx, db, root, args, flags)
+			result, err = runSmartEditDispatch(ctx, db, root, args, flags)
--- a/internal/dispatch/dispatch_edit.go
+++ b/internal/dispatch/dispatch_edit.go
@@ -13,7 +13,7 @@
-func runSmartEdit(ctx context.Context, db index.SymbolStore, root string, args []string, flags map[string]any) (any, error) {
+func runSmartEditDispatch(ctx context.Context, db index.SymbolStore, root string, args []string, flags map[string]any) (any, error) {
{"verify":"ok"}
```

`mode: "scope"` means the scope builder bound each ref by lexical resolution — no shadowed locals, no string literals, no comment matches. `mode: "name-match"` would mean the language fell through to the regex fallback and the diff needs review. `--verify` runs the project's build after the rewrite and reverts if it breaks.

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
| `rename file:Sym --to New [--cross-file] [--verify]` | Same-file by default; `--cross-file` walks the repo. `mode` field flags scope vs name-match. |
| `refs-to file:Sym` | References for impact analysis |
| `edit file:Sym --move-after B.go:Tgt` | Atomic two-file move with diffs |
| Re-read unchanged file | Deduplicated (zero output, zero waste) |

Under the hood:

- **Symbol extraction** is pure-Go, lexer-based per language — no CGO, no build step, works on broken code.
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
| `rename file:Symbol` | Rename a symbol; same-file by default, `--cross-file` for repo-wide. Scope-aware where supported. |
| `refs-to file:Symbol` | List references to a symbol |
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

edr reads and edits any text file. Symbol-aware features (symbol reads, `--signatures`, `orient`) require a supported language; rename safety further depends on whether the language has a scope builder admitted for writes:

**Symbol parsing (16):** Go, Python, JavaScript/JSX, TypeScript/TSX, Rust, Java, C, C++, C#, Kotlin, Swift, Ruby, PHP, Scala, Lua, Zig

**Scope-aware rename (12):** Go, JavaScript/TypeScript, Python, Java, Kotlin, Rust, C, C++, Ruby, C#, Swift, PHP — these get `mode: "scope"` with shadow filtering and binding analysis. Scala, Lua, and Zig have symbol parsing but fall through to `mode: "name-match"` for rename, so review the diff or run `--verify`.

## Limitations

- **Structural navigation, not full code analysis.** edr finds functions, classes, and references with lexer-based parsers, import graphs, and scope heuristics — not by type-checking. The main semantic promise is rename safety: where a language's scope builder is admitted for writes, rename avoids shadowed or unrelated same-name identifiers. The `mode` field on the rename result reports which path ran (`scope` is safe, `name-match` may contain cross-class false positives — review the diff or run `--verify`).
- **macOS and Linux only.** Windows is not planned.
- **Pure Go.** No CGO, no C compiler needed. Single ~6MB binary.

## Development

Setup on a new machine:

```bash
git clone https://github.com/jordw/edr.git
cd edr
./setup.sh                    # build + install edr (deps auto-installed via brew/apt/apk)
./scripts/eval/setup.sh       # clone the 12-repo test corpus as siblings of edr (~30 GB)
go test ./internal/...        # run the full suite
```

`scripts/eval/setup.sh` accepts `--repo NAME` for a single-repo setup and `--skip-index` to clone without indexing. Scope-graph dogfood tests run via `EDR_SCOPE_DOGFOOD_DIR=/path/to/corpus/repo go test ./internal/scope/store/ -run TestDogfood_ImportGraph -v`.

## License

[MIT](LICENSE)
