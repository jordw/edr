# edr spec

The current public contract for edr, with near-term direction where the product shape is already clear. This is an outside-in spec: it describes what commands exist, what forms they accept, what they return, and what stability guarantees the user can rely on. It is not a migration plan, and it is not an implementation design.

Words in this document are normative:

- `MUST` / `MUST NOT`: hard contract
- `SHOULD` / `SHOULD NOT`: strong default; deviations need an explicit reason
- `MAY`: optional behavior that does not change the contract

## Priorities

In order:

1. Trust — agents rely on output without second-guessing
2. Determinism — same input, same output, every time
3. Correctness — wrong answers are worse than no answers
4. Token efficiency — less output for the same information
5. Speed — fast enough to never be the bottleneck
6. Demo appeal — last, not first

## Principles

**Boring, strict, machine-first.** Not clever. Not magical. Not "flexible" in ways that create ambiguity.

**Equivalent operations, identical results.** Batch (`-r`, `-s`, `-e`, `-w`) and standalone (`edr read`, `edr search`, `edr edit`, `edr write`) are two interfaces for the same semantic operations. Same defaults, same semantics, same output shape. Divergence is a bug.

**Fail loud, fail early.** Empty success is misinformation. If edr cannot do what was asked, it says so with a structured error and a useful suggestion. It never returns empty results and exit 0 when the real problem is a wrong root or ambiguous input.

**Install once, works everywhere.** edr is a global tool. `edr setup` installs instructions into the agent's home-directory config; from that point, every repo the agent touches uses edr without per-project configuration. Updates happen silently when edr is rebuilt.

**Docs are tested contracts.** If the binary cannot do it, the docs must not teach it. If the docs teach it, there is a test proving it works. Help text, examples, and `CLAUDE.md` instructions are all generated from or validated against the same source.

## Semantic surface

edr has **twelve public workflow commands**, **one meta command**, and **one composition syntax**.

The semantic API is the standalone verb surface. Batch flags are transport sugar for composing multiple semantic ops in one invocation. Batch MUST NOT define meanings that do not exist in the standalone verbs.

Public workflow commands:

| Command | Purpose |
|---------|---------|
| `read` | Read files and symbols |
| `search` | Find text and symbols |
| `map` | Structural overview |
| `edit` | Mutate existing content |
| `write` | Create files or insert into structures |
| `rename` | Cross-file rename (sugar over refs + edit) |
| `refs` | Reference traversal and impact analysis |
| `verify` | Build/test verification |
| `run` | Execute command with sparse diff against previous run |
| `status` | Session state: recent ops, build state, action items |
| `checkpoint` | Snapshot or restore session state |
| `reset` | Clean slate: reindex, clear session, clear checkpoints |

Meta command:

| Command | Purpose |
|---------|---------|
| `setup` | Index repo, install global agent instructions |

Batch syntax:

- `edr -r ... -s ... -e ... -w ...`
- Batch is an invocation form, not a separate semantic command.
- Batch is the preferred workflow for agents when they need multiple ops in one turn.
- Standalone verbs are the canonical semantic contract and the primary documentation surface.
- In batch mode, output order is deterministic.

### Canonical command forms

These are the public forms edr SHOULD teach in docs, help text, setup instructions, and examples.

#### read

```bash
edr read <path>
edr read <path>:<symbol>
edr read <path> --lines 10:40
edr read <path>:<symbol> --signatures
edr read <path> --skeleton
```

Rules:

- `file:symbol` is the canonical symbol-targeting form
- `--lines start:end` is the canonical line-range form
- positional ambiguity such as `edr read file symbol` is compatibility-only and MUST NOT be the documented contract

#### search

```bash
edr search <pattern>
edr search <pattern> --text
edr search <pattern> --text --regex
edr search <pattern> --text --include "*.go" --context 3
edr search <pattern> --text --in <path>:<symbol>
```

Rules:

- plain `search` means symbol search
- `--text` is required for text search
- current builds also infer text search from flags like `--regex`, `--include`, `--exclude`, `--context`, and `--in`
- docs SHOULD still teach `--text` as the canonical way to request text search

#### map

```bash
edr map
edr map <path>
edr map --dir src --type function --lang go
```

#### refs

```bash
edr refs <symbol>
edr refs <path>:<symbol>
edr refs <symbol> --impact
edr refs <symbol> --chain <target>
edr refs <symbol> --callers
edr refs <symbol> --deps
```

#### edit

```bash
edr edit <path> --old-text "x" --new-text "y"
edr edit <path> --lines 20:30 --new-text "..."
edr edit <path>:<symbol> --new-text "..."
edr edit <path>:<symbol> --delete
edr edit <path>:<symbol> --move-after <symbol>
edr edit <path> --insert-at 20 --new-text "..."
edr edit <path> --old-text "x" --new-text "y" --in <path>:<symbol>
edr edit <path> --old-text "x" --new-text "y" --fuzzy
edr edit <path> --old-text "x" --new-text "y" --no-expect-hash
edr edit ... --dry-run
```

Rules:

- `--old-text` / `--new-text` are the canonical text-replacement flags
- `<path>:<symbol>` is the canonical symbol-edit form
- line edits use `--lines`, not separate positional modes
- `--delete` is the canonical explicit symbol-deletion form
- `--move-after` MUST be supported for same-file symbol moves; it MUST be explicit and MUST NOT guess cross-file intent
- `--insert-at` is the canonical pure-insertion form for line-oriented edits
- `--fuzzy` is opt-in only; it MUST NOT become the default matching mode without revisiting the spec's strictness guarantees
- stale-read protection is on by default: if the session has a prior read for the target file (or a symbol within it), edr MUST reject the edit when the file has changed since that read
- explicit hash plumbing is compatibility-only: `--expect-hash` MAY still exist, but agents SHOULD NOT need to thread hashes through normal edit workflows
- edr SHOULD expose an explicit opt-out such as `--no-expect-hash` for intentional blind edits

#### write

```bash
edr write <path> --content "..."
edr write <path> --append --content "..."
edr write <path> --inside <symbol> --content "..."
edr write <path> --after <symbol> --content "..."
edr write ... --dry-run
```

#### rename

```bash
edr rename <old> <new>
edr rename <old> <new> --dry-run
```

#### verify

```bash
edr verify
edr verify --level build
edr verify --level test
edr verify --command "make test"
```

#### run

```bash
edr run -- make test
edr run --full -- make test
edr run --reset -- make test
```

#### status

```bash
edr status
edr status --focus "implement auth middleware"
edr status --count 20
```

Rules:

- `edr status` is the canonical "where am I?" command
- Returns recent ops, build state, stale assumptions (fix items), and live signatures of active symbols (current items)
- `--focus` sets or clears a session-scoped focus string that persists across calls
- `--count` controls how many recent ops to include (default 10)
- Build state tracks the last verify result and whether edits have occurred since
- Fix items flag symbols whose signatures have changed since the agent last read them
- Current items show live signatures of recently modified, stale, or read symbols (capped at 10)

#### checkpoint

```bash
edr checkpoint
edr checkpoint "before refactor"
edr checkpoint --list
edr checkpoint --restore cp_1
edr checkpoint --diff cp_1
edr checkpoint --drop cp_1
```

Rules:

- Default action (no flags) creates a new checkpoint snapshotting all dirty files in the session
- Positional arg or `--label` sets a human-readable label
- `--restore` reverts files to checkpoint state; automatically creates a pre-restore safety checkpoint unless `--no-save` is given
- `--list` shows all checkpoints with ID, label, timestamp, and file count
- `--diff` shows which files changed since the given checkpoint
- `--drop` deletes a checkpoint

#### reset

```bash
edr reset                # full clean slate: reindex + new session + clear checkpoints
edr reset --index        # just rebuild index
edr reset --session      # just clear session state
```

Rules:

- Default `edr reset` (no flags) reindexes the repo, starts a fresh session, and clears all checkpoints
- `--index` rebuilds the index only
- `--session` clears session state only (equivalent to the former `session new`)
- `reindex` is a hidden alias for `reset --index`
- `session new` is a hidden alias for `reset --session`

#### setup

```bash
edr setup [path]
```

### Canonical spelling rules

- Docs MUST teach one spelling per concept
- Canonical spellings use long standalone flags: `--signatures`, `--old-text`, `--new-text`, `--dry-run`
- Batch shorthand such as `-r`, `-s`, `-e`, `-w`, `--sig`, `--old`, and `--new` is convenience syntax, not the canonical contract
- Hidden aliases MAY exist for compatibility, but they MUST NOT be the primary documented form
- Legacy names such as `explore`, `find`, `next`, `reindex`, and `session` are not part of the public CLI surface

Hidden compatibility aliases and internal command names are not part of the public CLI surface. They SHOULD NOT appear in help text, docs, or successful output, and users SHOULD NOT need to know they exist.

`rename` stays as convenience sugar over `refs` + `edit`. It MUST NOT grow independent semantics that drift from those primitives.

`setup` is a meta command — not part of the agent workflow loop, but a full public command.

## Setup and adoption

edr is a global tool. Install once, works in every repo.

### Install flow

`edr setup` does three things:

1. Index the current repo
2. Ensure `.edr/` in `.gitignore`
3. Install global agent instructions (with user consent)

Global instructions are written to agent-specific config files:

- `~/.claude/CLAUDE.md` for Claude Code
- `~/.codex/AGENTS.md` for Codex
- `~/.cursor/rules/edr.mdc` for Cursor

The instruction block is wrapped in sentinel comments for surgical updates:

```
<!-- edr-instructions hash:COMMIT_HASH -->
...instructions...
<!-- /edr-instructions -->
```

Rules:

- edr MUST NOT write to project-level config files (CLAUDE.md in the repo, .cursorrules, etc.). Those belong to the user.
- First install MUST ask permission before writing to the home directory. `--global` bypasses the prompt for non-interactive use.
- `--no-global` suppresses the prompt entirely.
- `--generic` prints instructions to stdout for unsupported agents.
- Non-interactive mode (`--global`) never prompts.

### Auto-update

After the user opts in via `edr setup`, edr may update global instructions automatically on later invocations when the installed instructions are out of date.

Rules:

- Auto-update MUST be silent. No stderr output, no delay.
- Auto-update MUST only happen after explicit user opt-in.
- Auto-update is best-effort. Errors are swallowed — a failed update MUST NOT break the actual command.
- Auto-update MUST add negligible latency.

### Instruction quality

The instruction block is product surface area. It is the first thing an agent reads about edr, and it determines whether the agent reaches for edr or falls back to built-in tools.

Rules:

- Instructions MUST lead with the value proposition (context savings).
- Instructions MUST explicitly prohibit the built-in tools they replace, by name.
- Instructions MUST include a direct mapping: built-in tool → edr equivalent.
- Instructions MUST teach key context-saving features: `--sig`, `--budget`, `--skeleton`, `map`, batching.
- Instructions MUST be under 500 tokens. Every token spent here is spent on every conversation.
- Different agent platforms get platform-specific instructions where needed.

## Transport contract

The intended public transport is **plain mode**: a **header line** (compact JSON metadata) followed by a **body** (raw text: code, diffs, grep-style matches). The header is always exactly one line. The body may be empty.

```
{"file":"f.go","sym":"Foo","lines":[10,20]}\n
func Foo() { ... }\n
```

Rules:

- Stdout line 1 is always a JSON object (the header)
- Everything after the first newline is the body — raw text, not JSON
- Stderr MUST be empty unless `--verbose` was explicitly requested. Hints (e.g., `hint: use --sig`) MAY appear on stderr for unbounded reads but are not part of the contract.
- Exit code `0` means success (all ops succeeded, verify passed if attempted)
- Exit code `1` means failure (any op failed or verify failed)
- There are no other public exit codes
- Batch ops are separated by `---\n` — each op gets its own header+body block
- Verify is appended as a final header line: `{"verify":"passed"}\n`

## Budget semantics

Budget is one of edr's core control surfaces. It MUST be defined precisely.

Rules:

- Budget unit is **approximate tokens**
- The estimator is `ceil(utf8_bytes / 4)` for returned text payloads
- Budget is a response-shaping limit, not a correctness limit
- Budget affects how much content is returned, never whether the underlying operation succeeds
- `--full` disables the default budget cap for that operation

Defaults:

- `read`, `search`, `map`, and `refs` have command-specific default budgets
- In the ideal design, the default is `2000` approximate tokens unless a command documents a narrower default
- Mutation commands do not use budget for correctness decisions; large diffs SHOULD be summarized if the diff contract says so

Multi-op batch rules:

- Budget is **per operation**, not global
- A batch does not have a shared token pool that gets redistributed implicitly
- If an op has no explicit budget, it uses that command's default budget independently
- If two reads appear in the same batch, one op consuming more of its budget MUST NOT reduce the other op's budget

Reporting:

- Ops that shape text output by budget SHOULD report `budget_used`
- `budget_used` is the same approximate-token unit as `--budget`
- `trunc: true` means content was reduced to satisfy budget or other response-shaping limits

## Path and position normalization

These rules apply across all commands and all op types:

- `file` paths MUST be repository-relative
- Path separators in JSON MUST be `/`, even on non-Unix hosts
- Absolute paths MUST NOT appear in successful output
- `lines` is always `[start, end]`, 1-based and inclusive
- `column` values are 1-based
- When a range is unknown or not applicable, the field is omitted instead of guessed

An agent should never need to normalize paths or wonder whether a line range is half-open.

## Index contract

edr auto-indexes on first use. Agents should never need to manually reindex.

Rules:

- If no index exists (or it contains zero files), edr MUST auto-index the repository before proceeding.
- `reset` and `reset --index` explicitly rebuild index state. `setup` also indexes on first install.
- `verify` MUST work without an index and MUST NOT create one as a side effect.
- Auto-indexing is the single consistent behavior. There is no "sometimes fail, sometimes auto-index" ambiguity.

### Per-file freshness

Individual files may become stale between full reindexes (e.g., after an external edit). edr SHOULD refresh stale file state automatically when needed. This MUST be invisible to the agent — no output shape changes, no extra fields.

## Output format

Every op produces one **header+body** block on stdout. The header is a single-line JSON object with metadata. The body is raw text (code, diffs, match lines). Multiple ops in a batch are separated by `---`.

Examples:

```
# Read
{"file":"src/config.go","sym":"parseConfig","lines":[10,45]}
func parseConfig() (*Config, error) { ... }

# Search
{"n":4}
src/config.go:10: parseConfig
src/config.go:45: parseFlags

# Edit
{"file":"src/config.go","status":"applied","hash":"def456"}
--- a/src/config.go
+++ b/src/config.go
@@ -10,4 +10,6 @@
...

# Error
{"error":"symbol \"Foo\" not found in src/config.go","ec":"not_found"}

# Verify (appended after ops)
{"verify":"passed"}
```

Rules:

- Each op is one header line + optional body text
- Header is always valid JSON; body is raw text, never JSON
- Batch ops are separated by `---` on its own line
- Verify is a final header line appended after all ops: `{"verify":"passed"}`, `{"verify":"failed","error":"..."}`, or `{"verify":"skipped","reason":"..."}`
- When verify is not attempted, no verify line is emitted
- Errors are header-only (no body): `{"error":"...","ec":"code"}`
- Field names use short forms for token efficiency (see field name table below)

## Failure scopes

There are exactly two failure scopes:

- **Invocation-level**: the command could not be dispatched at all (unknown command, invalid flags, malformed input, repo root detection failure). These emit a single error header: `{"error":"...","ec":"code"}`
- **Op-level**: a specific operation failed after dispatch. In a batch, the failed op gets an error header while other ops succeed normally.

Op-level failures are for:

- file not found
- symbol not found
- ambiguous symbol
- ambiguous edit target
- prerequisite-gated skipped mutations

In a batch, each op is independent. A failed read does not prevent other reads from succeeding.

## Op ordering

Batch output order MUST be deterministic.

Rules:

- edr MUST normalize execution order around mutations so that reads before a mutation observe pre-edit state and reads after a mutation observe post-edit state
- A failed mutation SHOULD cause later dependent mutations to be emitted as skipped ops
- Users SHOULD be able to understand output order from the observable semantics alone, without knowing the internal scheduler

## Field name table

Field names are short for token efficiency. This is the canonical mapping:

| Header field | Meaning |
|-------------|---------|
| `file` | Repository-relative file path |
| `sym` | Targeted or matched symbol name |
| `lines` | `[start, end]` 1-based inclusive range |
| `hash` | Opaque full-file content hash (always present on applied mutations; may also appear on some plain read results) |
| `n` | Total match count (search, refs) |
| `trunc` | `true` when content was budget-limited |
| `ec` | Error code (see failure codes table) |
| `status` | Mutation status: `applied`, `dry_run`, `noop`, `failed`, `skipped` |
| `session` | `"unchanged"` when content was previously seen in session |
| `msg` | Human-readable message (noop edits) |

Rules:

- All header fields live at the top level — no nesting
- `hash` is always present on applied mutation ops; read ops MAY include it in plain mode
- Hash format is opaque but stable
- Fields are omitted when not applicable, never set to null or empty defaults

## One command, one result shape

Each command has one primary header+body shape. Flags mostly change the body content, but some modes add or remove optional metadata fields.

That means:

- A `read` header always has `file` and `lines`, whether it returned full content, signatures, skeleton, or a line range
- A `search` header normally has `n` (match count), including `{"n":0}` for zero matches
- A successful `edit` header always has `file` and `status`; `hash` is present on `applied`
- A successful `write` header always has `file` and `status`; `hash` is present on `applied`

Fields MAY be omitted when truly inapplicable. Current builds also expose some mode-specific metadata such as `hint`, `session`, or read-time `hash`.

## Read ops

A successful read header contains:

- `file` — repository-relative path
- `lines` — `[start, end]` 1-based inclusive

Optional header fields:

- `sym` — symbol name (when reading a symbol)
- `hash` — file content hash (currently present in plain mode on some reads)
- `trunc` — `true` when budget-limited
- `session` — `"unchanged"` when content was seen before (body omitted)

The body is the raw file/symbol content. `--signatures` and `--skeleton` change the body content, not the header shape.

## Search ops

Search mode is symbol-first, with some current inference rules for text-search flags.

Rules:

- `--text` means text search
- if `--text` is absent, the default mode is symbol search
- current builds also treat `--regex`, `--include`, `--exclude`, `--context`, and `--in` as text-search selectors
- docs SHOULD still teach `--text` as the canonical way to request text search

A successful search header contains:

- `n` — total match count

Optional:

- `hint` — suggestion when results are budget-limited

The body is grep-style lines: `file:line: text` (one per match, grouped by file).

Zero matches is a valid success (`{"n":0}`), not an error.

## Map ops

The map header may include summary metadata such as `files`, `symbols`, `trunc`, `hint`, or `session`.

The body is `file:line-endline: kind name` lines (one per symbol).

## Refs ops

A successful refs header contains:

- `sym` — resolved symbol as `file:name`
- `n` — total reference count

The body is `file:line: name` lines (one per reference).

## Rename ops

A successful `rename` header contains:

- `status` — one of `applied`, `dry_run`, `noop`, `failed`

Optional header fields:

- `from` — old symbol name
- `to` — new symbol name
- `n` — number of affected occurrences

Rules:

- `rename --dry-run` SHOULD provide enough preview detail for an agent to judge the change without applying it
- richer dry-run preview MAY include per-file diffs or per-occurrence previews, but it MUST stay within the normal rename contract rather than inventing a separate command

## Mutation ops

Each `edit` and `write` op header contains:

- `file` — repository-relative path
- `status` — one of `applied`, `dry_run`, `noop`, `failed`, `skipped`
- `hash` — file content hash after mutation (present on `applied` only)

Optional header fields:

- `msg` — human-readable message (noop explanation)

The body is a unified diff when available.

```
# Applied edit
{"file":"src/config.go","status":"applied","hash":"def456"}
--- a/src/config.go
+++ b/src/config.go
@@ -10,4 +10,6 @@
...

# Noop edit (old == new)
{"file":"src/config.go","msg":"old_text equals new_text, no change applied","status":"noop"}
```

Rules:

- `"noop"` means the operation was valid but made no change (e.g., old_text == new_text)
- `"failed"` means the operation itself failed; `ec` is required
- `"skipped"` means the operation was not attempted because a prerequisite failed
- Dry-run ops never trigger verify
- Noop ops skip verify
- The only difference between preview and commit is `status` (`dry_run` vs `applied`)
- `edit --delete` is equivalent to an explicit deletion request; it SHOULD return the same mutation shape as other edits
- `edit --insert-at` is an insertion edit, not a write; it SHOULD return the same mutation shape as other edits
- If `edit --fuzzy` succeeds, the response SHOULD include metadata indicating that matching was fuzzy rather than exact
- If `edit --move-after` is supported, it SHOULD return a normal edit result rather than inventing a separate mutation type
- if a session-backed stale-read precondition fails, the edit MUST fail with `ec:"hash_mismatch"`
- in a batch, reads that occur before edits establish the expected snapshot for those later edits
- if there is no prior session read for the target, edr MAY apply the edit without an implicit hash precondition

When verify fails after successful edits, exit code is 1 but the mutation headers still show `"status":"applied"`. That distinction is essential.

### Atomic batch edits

Batch edit execution MUST support an explicit atomic mode.

Rules:

- `--atomic` applies to a batch of edits, not to standalone single-edit invocations
- In atomic mode, either all targeted edits apply or none do
- If any edit in an atomic batch fails validation or matching, the batch MUST leave files unchanged
- Atomic mode MUST NOT weaken existing error reporting; failures still surface as normal op-level errors

## Verify

Verify is appended as a final header line after all op blocks.

```
# Passed
{"verify":"passed"}

# Failed
{"verify":"failed","error":"exit status 1"}

# Skipped
{"verify":"skipped","reason":"dry run"}
```

The `verify` value is the status: `"passed"`, `"failed"`, or `"skipped"`.

Optional fields:

- `error` — process error string (when failed)
- `reason` — why it was skipped

Rules:

- When verify is not attempted, no verify line is emitted
- `verify` uses `status`, not `ok: true/false`
- Standalone `edit` and `write` currently auto-verify after a successful non-dry-run apply
- Batch mode currently auto-verifies when edits are present unless explicitly disabled
- Writes-only batch flows require explicit verify (`-V`, `--verify`, or JSON batch verify config)
- Auto-detection chain: `go.mod` → `package.json` → `Cargo.toml` → `Makefile` → skip

## Verify scope

Default verify MUST operate on the relevant build graph, not blindly on the whole repo.

Rules:

- Scope to what was touched
- Respect module and package boundaries
- Ignore irrelevant broken fixtures by default (`testdata/`, scratch dirs, standalone examples, temp dirs)
- If edr cannot determine a truthful scope, it SHOULD skip verify rather than run a misleading one
- The executed command MUST be reported exactly

`"status": "skipped", "reason": "could not determine build scope"` is better than a false failure.

## Failure shape

Every failed op emits a header-only block (no body):

```
{"error":"symbol \"NonExistentSymbol\" not found in src/config.go","ec":"not_found"}
```

Rules:

- `ec` SHOULD be present when a specific error code is known
- Failed ops MUST NOT also include success-only fields (`file`, `lines`, `hash`, etc.)
- Failed ops have no body

Recommended codes:

| Code | Meaning |
|------|---------|
| `file_not_found` | File does not exist |
| `not_found` | Symbol or match target not found |
| `ambiguous_symbol` | Multiple symbols match; `candidates` included |
| `ambiguous_match` | Edit old-text matched multiple locations |
| `index_failed` | Auto-indexing failed |
| `outside_repo` | Path is outside repository root |
| `hash_mismatch` | Explicit or implicit stale-read precondition failed |
| `invalid_mode` | Mutually exclusive flags or impossible mode |
| `command_error` | Last-resort fallback only |

Not every current failed op carries an `ec`, but that is the desired direction for the public surface.

## Validation

edr should be skeptical by default. These are hard errors, not warnings:

```
{"error":"auto-indexing failed: permission denied","ec":"index_failed"}
```

```
{"error":"symbol \"Init\" is ambiguous (2 definitions)","ec":"ambiguous_symbol"}
```

Validation rules:

- Unknown flags are invocation errors, not ignored warnings
- Ambiguous targets are errors, never auto-picked
- Contradictory flags are parse errors
- Paths outside repo are errors
- Empty success is only valid for commands where "zero results" is semantically real, such as search with `n: 0`

Additional edit-matching rules:

- `--fuzzy` MUST remain stricter than semantic search; it is for formatting-tolerant matching, not approximate intent guessing
- `--fuzzy` MUST still fail on ambiguity
- `--all` and `--fuzzy` SHOULD NOT combine unless the semantics are made explicit and deterministic
- If `--move-after` is supported, cross-file moves MUST fail rather than silently degrade into copy/delete behavior
- when the session shows that the agent previously read the target file or symbol, edit MUST validate that the file has not changed since that read unless the user explicitly opts out
- the implicit stale-read check is file-level even when the prior read was symbol-scoped

## Batch / standalone parity

For any standalone invocation, there is an equivalent batch invocation that produces identical output. Batch composes the standalone semantic commands; it does not replace them.

```bash
edr read src/config.go --signatures
edr -r src/config.go --sig
```

```bash
edr edit src/config.go --old-text "foo" --new-text "bar"
edr -e src/config.go --old "foo" --new "bar"
```

```bash
edr edit src/config.go:Handler --delete
edr -e src/config.go:Handler --delete
```

```bash
edr search "handleRequest" --text
edr -s "handleRequest" --text
```

Rules:

- Standalone verbs are the canonical semantics
- Batch is composition syntax over those semantics
- Shorthand flags are aliases, not separate semantics
- If a shorthand works in batch, it MAY work in standalone, but docs SHOULD teach the canonical long-form spelling
- Batch MUST call the same semantic dispatch functions as standalone
- If a batch combination cannot be expressed as a public standalone invocation, it is invalid
- `--atomic` is a batch-only modifier and does not require a standalone equivalent

## Session contract

Sessions are always active. Session routing is automatic across repeated calls from the same calling process lineage. Agents SHOULD get session dedup by default without having to pass a session identifier on normal calls.

`edr reset --session` creates a fresh session and returns its ID.

Rules:

- Repeated calls from the same calling process lineage SHOULD reuse the same session automatically
- Automatic routing is based on the process that is making successive edr calls, not on the shell process used to launch those calls
- If explicit session selection is exposed, it overrides automatic routing
- Content-returning ops include `session`
- `session` is one of:
  - `"new"`: first time this content was seen in the session
  - `"unchanged"`: same content as previously seen; body MAY be omitted
- After a context reset, agents SHOULD run `edr reset --session` before continuing work so stale dedup state is not reused

Concurrency rules:

- Concurrent use of the same session MUST NOT corrupt session state
- Separate agents SHOULD normally get separate sessions unless they intentionally share one

## Run contract

`edr run -- <command> [args...]` executes argv directly and diffs output against the previous run of the same command line.

First run shows full output. Subsequent runs show a sparse diff: unchanged regions collapse and changed lines show inline markers.

Rules:

- Output is compared against the previous run of the same command line
- Identical output prints `[no changes, N lines]`
- Changed lines show inline `{old → new}` markers highlighting only the changed segments
- Unchanged regions collapse to `[N unchanged lines]`
- Digit-only changes (e.g. timing values, counters) are collapsed along with unchanged lines — no flag needed
- `--full` bypasses diff entirely, shows raw output
- Exit code passes through from the wrapped command
- `--` separates edr flags from the wrapped command's flags

## Status contract

`edr status` returns a structured status summary for the current session. It is the canonical entry point for an agent resuming work or deciding what to do.

Output fields (all optional, omitted when empty):

- `focus` — the session focus string, if set
- `recent` — array of recent ops, most recent first. Each entry has `op_id`, `cmd`, `kind`, and optionally `file`, `symbol`, `ok`
- `total_ops` — total number of ops in session
- `build` — object with `status` (`"passed"`, `"failed"`) and optionally `edits_since: true`
- `fix` — array of stale assumptions. Each has `id`, `type`, `confidence`, `file`, `symbol`, `reason`, `suggest`
- `current` — array of active symbol signatures. Each has `file`, `symbol`, `reason` (`"modified"`, `"stale"`, `"recent"`), `signature`

Rules:

- `status` MUST work without an index (gracefully omits `fix` and `current`)
- `status` MUST NOT mutate session state except when `--focus` is explicitly set
- Fix items compare stored signature hashes against current index state
- Current items are capped at 10, prioritized: modified > stale > recent

## Checkpoint contract

`edr checkpoint` snapshots the current state of all files the session has touched, allowing rollback.

### Create (default)

Returns: `status: "created"`, `id`, `file_count`, and optionally `label`, `op_id`.

### List (`--list`)

Returns: `checkpoints` array. Each entry has `id`, `created_at`, `op_id`, `file_count`, and optionally `label`.

### Restore (`--restore <id>`)

Returns: `status: "restored"`, `target`, `restored` (list of restored files), optionally `pre_restore_checkpoint` (safety snapshot ID) and `not_removed` (files that could not be removed).

### Diff (`--diff <id>`)

Returns: `checkpoint` (the ID), `diffs` array. Each entry has `path` and `status`.

### Drop (`--drop <id>`)

Returns: `status: "dropped"`, `id`.

Rules:

- Create MUST snapshot all dirty files tracked by the session
- Restore MUST create a pre-restore safety checkpoint by default; `--no-save` opts out
- Restore MUST revert file contents on disk, not just session state
- List MUST return checkpoints in a stable order
- Drop MUST NOT fail silently if the checkpoint does not exist

## Reset contract

`edr reset` provides a clean-slate operation for recovering from bad state.

### Full reset (default)

Rebuilds the index, starts a fresh session, and clears all checkpoints.

Returns: `status: "reset"`, and optionally `index_files` (number of files indexed), `session` (new session ID).

### Index only (`--index`)

Rebuilds the SQLite index. Equivalent to the former `reindex` command.

Returns: `status: "reset"`, `scope: "index"`, `files` (number of files indexed).

### Session only (`--session`)

Clears session state and starts fresh. Equivalent to the former `session new`.

Returns: `status: "reset"`, `scope: "session"`, `session` (new session ID).

Rules:

- Default `edr reset` is the "something's wrong, fix it" verb — it clears everything
- `--index` and `--session` are mutually exclusive scoping flags; combining them is equivalent to a full reset
- Reset MUST NOT require user confirmation — it is a non-destructive rebuild (source files are never modified)
- `reindex` is a hidden alias for `reset --index`
- `session new` is a hidden alias for `reset --session`

## Result consistency

Agents should need exactly one parser:

1. Read the first line as JSON (the header)
2. Check for `error`/`ec` — if present, this op failed
3. Read remaining lines until `---` or EOF as the body
4. After all ops, check for a `{"verify":...}` line

If an agent needs separate parsers for batch vs standalone, or for `read --sig` vs `read --skeleton`, the contract has failed.

## Implementation discipline

The public contract above SHOULD be enforced by CI, but CI structure is not itself part of the product spec.

In practice, this means:

- contract tests should exist for help surface, output shape, parity, exit codes, session behavior, and failure placement
- documentation and command registry should be mechanically validated against each other
- any behavior described here that is not testable yet is a spec debt item, not an excuse to loosen the contract

## Non-goals

This spec does not require:

- natural-language interpretation of vague requests
- auto-healing silent fallbacks
- schema flexibility for convenience
- verbose prose output on stdout

The point of edr is not to feel smart. The point is to be a reliable component in an agent loop.
