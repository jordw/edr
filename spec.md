# Ideal edr

The target state for edr. This is not a migration plan. It is the product contract for a machine-first code tool that agents can trust.

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

edr has **twelve semantic commands** and **one batch syntax**.

Public semantic commands:

| Command | Purpose |
|---------|---------|
| `read` | Read files and symbols |
| `search` | Find text and symbols |
| `map` | Structural overview |
| `edit` | Mutate existing content |
| `write` | Create files or insert into structures |
| `refs` | Reference traversal |
| `verify` | Build/test verification |
| `reindex` | Force re-index |
| `rename` | Cross-file rename (sugar over refs + edit) |
| `run` | Execute command with sparse diff against previous run |
| `session` | Session management (`session new`) |
| `setup` | Index repo, install global agent instructions |

Batch syntax:

- `edr -r ... -s ... -e ... -w ...`
- Batch is an invocation form, not a separate semantic command.
- In batch mode, the header+body block order reflects the operation types.

Internal names such as `explore`, `find`, `edit-plan`, `multi`, and `init` are implementation details. They MUST NOT:

- appear in `--help`
- be documented as supported commands
- appear in output headers for successful dispatch
- be accepted as stable public inputs

If a user invokes an internal command name directly, edr MUST reject it with a structured error. Hiding it from help is not enough.

`rename` stays as convenience sugar over `refs` + `edit`. It MUST NOT grow independent semantics that drift from those primitives.

`setup` is a full public command.

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

After the user opts in via `edr setup`, a sentinel file (`~/.edr/global_hash`) records the build hash. On every subsequent edr invocation:

- No sentinel → skip (user never opted in, do not auto-install)
- Same hash → skip (already current)
- Different hash → silently force-update all global instructions and update sentinel

Rules:

- Auto-update MUST be silent. No stderr output, no delay.
- Auto-update MUST NOT run if the build hash is empty or "unknown".
- Auto-update is best-effort. Errors are swallowed — a failed update MUST NOT break the actual command.
- The sentinel check is a single file read + string compare. It MUST add negligible latency.

### Instruction quality

The instruction block is product surface area. It is the first thing an agent reads about edr, and it determines whether the agent reaches for edr or falls back to built-in tools.

Rules:

- Instructions MUST lead with the value proposition (context savings).
- Instructions MUST explicitly prohibit the built-in tools they replace, by name.
- Instructions MUST include a direct mapping: built-in tool → edr equivalent.
- Instructions MUST teach key context-saving features: `--sig`, `--budget`, `--skeleton`, `map`, batching.
- Instructions MUST be under 250 tokens. Every token spent here is spent on every conversation.
- Different agent platforms get platform-specific instructions (Claude tools vs Codex shell commands).

### Dev builds

`BuildHash` falls back to `git rev-parse --short HEAD` when not set via ldflags. Dev builds MUST NOT use "unknown" as a hash — that would prevent auto-update from working.

## Transport contract

Each invocation emits a **header line** (compact JSON metadata) followed by a **body** (raw text: code, diffs, grep-style matches). The header is always exactly one line. The body may be empty.

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
- Mutation commands do not use budget for correctness decisions; large diffs MAY still be summarized if the diff contract says so

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

edr auto-indexes on first use. Agents should never see "run edr reindex".

Rules:

- If no index exists (or it contains zero files), edr MUST auto-index the repository before proceeding. This happens under the writer lock with a stderr progress message.
- `reindex` and `setup` explicitly create or refresh index state.
- `verify` MUST work without an index and MUST NOT create one as a side effect.
- Auto-indexing is the single consistent behavior. There is no "sometimes fail, sometimes auto-index" ambiguity.

### Per-file freshness

Individual files may become stale between full reindexes (e.g., after an external edit). edr checks per-file freshness on access:

- When resolving a symbol, edr compares the file's on-disk mtime against its indexed mtime.
- If stale (~20µs check), the single file is reindexed under the writer lock (~2ms) before proceeding.
- If fresh, no work is done.
- This MUST be invisible to the agent — no output shape changes, no extra fields.

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

Batch ops MUST preserve request order in output.

Examples:

- `edr -r a.go -s foo -e b.go ...` => output order is read, search, edit
- `edr -e a.go ... -e b.go ... -w c.go ...` => output order is edit, edit, write

This is mandatory even if implementation internally parallelizes read-only work. Deterministic output order matters more than execution order.

Op IDs are used internally but are not part of the plain output format. The output order itself is the correlation mechanism.

## Field name table

Field names are short for token efficiency. This is the canonical mapping:

| Header field | Meaning |
|-------------|---------|
| `file` | Repository-relative file path |
| `sym` | Targeted or matched symbol name |
| `lines` | `[start, end]` 1-based inclusive range |
| `hash` | Opaque full-file content hash (mutations only) |
| `n` | Total match count (search, refs) |
| `trunc` | `true` when content was budget-limited |
| `ec` | Error code (see failure codes table) |
| `status` | Mutation status: `applied`, `dry_run`, `noop`, `failed`, `skipped` |
| `session` | `"unchanged"` when content was previously seen in session |
| `msg` | Human-readable message (noop edits) |

Rules:

- All header fields live at the top level — no nesting
- `hash` is only present on mutation ops (edit, write); read ops omit it
- Hash format is opaque but stable
- Fields are omitted when not applicable, never set to null or empty defaults

## One command, one result shape

Each command has exactly one header+body shape. Flags change the body content, not the header structure.

That means:

- A `read` header always has `file` and `lines`, whether it returned full content, signatures, skeleton, or a line range
- A `search` header always has `n` (match count) when there are matches, or `{}` for zero matches
- An `edit` header always has `file` and `status`; `hash` is present on `applied`
- A `write` header always has `file` and `status`; `hash` is present on `applied`

Fields MAY be omitted when truly inapplicable. They MUST NOT appear under different wrappers depending on flags.

## Read ops

A successful read header contains:

- `file` — repository-relative path
- `lines` — `[start, end]` 1-based inclusive

Optional header fields:

- `sym` — symbol name (when reading a symbol)
- `trunc` — `true` when budget-limited
- `session` — `"unchanged"` when content was seen before (body omitted)

The body is the raw file/symbol content. `--signatures` and `--skeleton` change the body content, not the header shape.

## Search ops

Search mode MUST be explicit and deterministic.

Rules:

- `--text` means text search
- if `--text` is absent, the default mode is symbol search
- edr MUST NOT silently fall back from symbol search to text search

A successful search header contains:

- `n` — total match count

Optional:

- `hint` — suggestion when results are budget-limited

The body is grep-style lines: `file:line: text` (one per match, grouped by file).

Zero matches is a valid success (`{"n":0}` or `{}`), not an error.

## Map ops

The map header is `{}` (metadata is omitted in plain mode).

The body is `file:line-endline: kind name` lines (one per symbol).

## Refs ops

A successful refs header contains:

- `sym` — resolved symbol as `file:name`
- `n` — total reference count

The body is `file:line: name` lines (one per reference).

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

When verify fails after successful edits, exit code is 1 but the mutation headers still show `"status":"applied"`. That distinction is essential.

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
- `verify` MUST NOT use `ok: true/false`
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

- `error` and `ec` appear together or not at all
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
| `hash_mismatch` | `expect_hash` precondition failed |
| `invalid_mode` | Mutually exclusive flags or impossible mode |
| `command_error` | Last-resort fallback only |

Every failed op MUST have an `ec`. If a path can only produce `error` without a code, that path is underspecified.

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

## Batch / standalone parity

For any standalone invocation, there is an equivalent batch invocation that produces identical output.

```bash
edr read src/config.go --signatures
edr -r src/config.go --sig
```

```bash
edr edit src/config.go --old-text "foo" --new-text "bar"
edr -e src/config.go --old "foo" --new "bar"
```

```bash
edr search "handleRequest" --text
edr -s "handleRequest" --text
```

Rules:

- Shorthand flags are aliases, not separate semantics
- If a shorthand works in batch, it SHOULD also work in standalone
- Batch MUST call the same semantic dispatch functions as standalone
- If a batch combination cannot be expressed as a public standalone invocation, it is invalid

There is no batch-only routing layer in the ideal design. No `inferQueryCmd`, no `doQueryToMultiCmd`, no `edit-plan` detour.

## Session contract

Sessions are always active. Resolution order:

1. `EDR_SESSION` env var (explicit ID)
2. PPID-based lookup (`.edr/sessions/ppid_<ppid>` maps to a session ID)
3. Fallback to `"default"` session

`edr session new` generates a unique session ID, creates the session file, and writes a PPID mapping so subsequent calls from the same parent process auto-resolve to that session. Old session files are cleaned up after 7 days.

Rules:

- Content-returning ops include `session`
- `session` is one of:
  - `"new"`: first time this content was seen in the session
  - `"unchanged"`: same content as previously seen; body MAY be omitted
- After a context reset, agents SHOULD run `edr session new` to clear stale dedup state

Concurrency rules:

- Session files are per session, not per process
- Concurrent writers with the same session MUST NOT corrupt session state
- Atomic rename is sufficient
- Last-write-wins is acceptable if the only consequence is a missed dedup
- Multiple agents get separate sessions via different PPIDs

Session state MUST be bounded in size. A cap plus eviction is required.

## Run contract

`edr run <command>` executes a shell command and diffs output against the previous run of the same command.

First run shows full output. Subsequent runs show a sparse diff: unchanged regions collapse, changed lines show inline markers. Uses LCS alignment for accurate diffing.

Rules:

- Output is compared line-by-line against the stored previous run using LCS alignment
- Identical output prints `[no changes, N lines]`
- Changed lines show inline `{old → new}` markers highlighting only the changed segments
- Unchanged regions collapse to `[N unchanged lines]`
- Digit-only changes (e.g. timing values, counters) are collapsed along with unchanged lines — no flag needed
- The command string is part of the storage key (different commands don't interfere)
- Previous output is stored in `.edr/run/` keyed by command hash, capped at 1MB
- `--full` bypasses diff entirely, shows raw output
- Exit code passes through from the wrapped command
- `--` separates edr flags from the wrapped command's flags

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
