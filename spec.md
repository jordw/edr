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

**Fail loud, fail early.** Empty success is misinformation. If edr cannot do what was asked, it says so with a structured error and a useful suggestion. It never returns empty results and exit 0 when the real problem is a missing index, wrong root, or ambiguous input.

**Install once, works everywhere.** edr is a global tool. `edr setup` installs instructions into the agent's home-directory config; from that point, every repo the agent touches uses edr without per-project configuration. Updates happen silently when edr is rebuilt.

**Docs are tested contracts.** If the binary cannot do it, the docs must not teach it. If the docs teach it, there is a test proving it works. Help text, examples, and `CLAUDE.md` instructions are all generated from or validated against the same source.

## Semantic surface

edr has **ten semantic commands** and **one batch syntax**.

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
| `setup` | Index repo, install global agent instructions |

Batch syntax:

- `edr -r ... -s ... -e ... -w ...`
- Batch is an invocation form, not a separate semantic command.
- In batch mode, `ops[].type` is the authoritative operation kind.

Internal names such as `explore`, `find`, `edit-plan`, `multi`, and `init` are implementation details. They MUST NOT:

- appear in `--help`
- be documented as supported commands
- appear in the output envelope for successful dispatch
- be accepted as stable public inputs

If a user invokes an internal command name directly, edr MUST reject it with a structured error. Hiding it from help is not enough.

`rename` stays as convenience sugar over `refs` + `edit`. It MUST NOT grow independent semantics that drift from those primitives.

`setup` is a full public command and MUST emit the standard output envelope when JSON output is requested.

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
- JSON mode (`--json`) never prompts.

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

Each invocation emits exactly one UTF-8 JSON object on stdout, terminated by a newline.

Rules:

- Stdout MUST contain only the envelope JSON
- Stderr MUST be empty unless `--verbose` was explicitly requested
- Exit code `0` means `envelope.ok == true`
- Exit code `1` means `envelope.ok == false`
- There are no other public exit codes

Pretty-printing vs compact JSON is not part of the public contract. The shape is.

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
- `truncated: true` means content was reduced to satisfy budget or other response-shaping limits

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

Index requirements MUST be explicit and command-specific:

- `reindex` and `setup` create or refresh index state
- `verify` MUST work without an index and MUST NOT create one as a side effect
- Commands that require structural repo knowledge (`search`, `map`, `refs`, symbol-scoped `read`, `rename`) MUST fail with `no_index` when no index exists
- If the index exists but contains zero usable files, those commands MUST fail with `empty_index`

Plain file reads are allowed to be stricter than necessary, but they MUST be consistent. A command MUST NOT sometimes auto-index, sometimes fail with `no_index`, and sometimes read raw disk based on hidden heuristics. Pick one behavior and keep it uniform.

## Output envelope

Every invocation returns one envelope on stdout.

```json
{
  "schema_version": 2,
  "ok": true,
  "command": "read",
  "ops": [
    {
      "op_id": "r0",
      "type": "read",
      "file": "src/config.go",
      "symbol": "parseConfig",
      "content": "func parseConfig() (*Config, error) { ... }",
      "lines": [10, 45],
      "hash": "abc123",
      "session": "new"
    }
  ],
  "verify": null,
  "errors": []
}
```

Envelope rules:

- `schema_version` is a monotonically increasing integer
- `ok` is true only if every op succeeded and verify, if attempted, passed
- `ops` is always an array, even for single-op invocations
- `errors` is always an array
- `verify` is `null` when not attempted, an object when attempted
- `command` describes the invocation target, not the semantic meaning of every op:
  - standalone `edr read ...` => `"command": "read"`
  - standalone `edr verify ...` => `"command": "verify"`
  - rejected unknown command `edr explore ...` => `"command": "explore"`
- In mixed-op batch invocations, `ops[].type` is authoritative
- In mixed-op batch invocations, `command` MAY be omitted
- If `command` is present for batch compatibility, agents MUST treat it as informational only
- `command` MUST NOT be `"batch"` in the ideal contract

The `command` field MUST never leak internal names for successful public operations. `"command": "reindex"`, never `"init"`. `"command": "search"`, never `"find"`.

## Envelope-level vs op-level failure

There are exactly two failure scopes:

- Envelope-level `errors[]`: invocation could not be dispatched or interpreted as requested
- Op-level `error` / `error_code`: a specific operation failed after dispatch

Envelope-level errors are for:

- unknown command
- invalid flag combinations
- malformed JSON input
- repository root detection failure
- other failures that prevent op execution entirely

Op-level failures are for:

- file not found
- symbol not found
- ambiguous symbol
- ambiguous edit target
- prerequisite-gated skipped mutations

Per-op failures MUST live in `ops[]`, not in `errors[]`. An agent should never have to inspect both locations for "did op 2 fail?"

## Op ordering

`ops[]` MUST preserve request order.

Examples:

- `edr -r a.go -s foo -e b.go ...` => ops order is `r0`, `s0`, `e0`
- `edr -e a.go ... -e b.go ... -w c.go ...` => ops order is `e0`, `e1`, `w0`

This is mandatory even if implementation internally parallelizes read-only work. Deterministic output order matters more than execution order.

## Op IDs

`op_id` is the stable per-op correlation key.

Rules:

- Prefix is operation type: `r`, `s`, `m`, `e`, `w`, `x`, `n`
- Suffix is zero-based count within that type in request order
- Standalone always emits one op id for the invoked command (`r0`, `s0`, `m0`, `e0`, `w0`, `x0`, `n0`)
- Batch uses the same scheme across the whole request

Suggested prefixes:

| Prefix | Meaning |
|--------|---------|
| `r` | read |
| `s` | search |
| `m` | map |
| `e` | edit |
| `w` | write |
| `x` | refs |
| `n` | rename |

`verify` does not produce an op. Its result lives in the top-level `verify` field.

## Shared op fields

Different commands return different payloads. That is expected. Shared concepts use shared field names in the same location.

| Field | Meaning |
|-------|---------|
| `file` | Repository-relative file path |
| `symbol` | Targeted or matched symbol |
| `lines` | `[start, end]` 1-based inclusive range |
| `content` | Text payload |
| `hash` | Opaque full-file content hash |

Rules:

- Shared fields live at the top level of the op
- The same concept MUST NOT move between `op.hash`, `op.symbol.hash`, and `op.result.hash`
- `hash` refers to the full file content, not the snippet content
- Hash format is opaque but stable within a schema version

An agent should never have to check two paths for the same datum.

## One command, one result shape

Each command has exactly one op shape. Flags change values, not structure.

That means:

- A `read` op always uses the same top-level keys whether it returned full content, signatures, skeleton, or a line range
- A `search` op always has `kind`, `matches`, and `total_matches`
- A `map` op always has the same shape whether repo-scoped or file-scoped
- An `edit` op always has `status`, diff metadata, and hash information in the same places
- A `write` op always has `status`, diff metadata, and hash information in the same places

Fields MAY be omitted when truly inapplicable. They MUST NOT appear under different wrappers depending on flags.

## Read ops

A successful `read` op always contains:

- `file`
- `hash`
- `content`
- `lines`

Optional additions:

- `session`
- `symbol`
- `truncated`: boolean
- `budget_used`: integer

`--signatures` and `--skeleton` change the meaning of `content`, not the shape of the op.

## Search ops

Search mode MUST be explicit and deterministic.

Rules:

- `--text` means text search
- if `--text` is absent, the default mode is symbol search
- edr MUST NOT silently fall back from symbol search to text search

A successful search op always contains:

- `kind`: `"symbol"` or `"text"`
- `matches`: array
- `total_matches`: integer

Zero matches is a valid success, not an error.

Suggested search metadata:

- `truncated`
- `budget_used`
- `include`
- `exclude`

## Map ops

`map` always returns one shape, regardless of scope.

Required fields:

- `files`
- `shown_files`
- `shown_symbols`
- `truncated`
- `symbols`

File-scoped maps use the same fields with trivial values (`files: 1`, etc.). Agents should not have to special-case repo map vs file map.

## Mutation ops

Each `edit`, `write`, and `rename` op reports its own state:

```json
{
  "op_id": "e0",
  "type": "edit",
  "file": "src/config.go",
  "status": "applied",
  "hash": "def456"
}
```

`status` is one of:

- `"applied"`
- `"failed"`
- `"skipped"`
- `"dry_run"`
- `"noop"`

Rules:

- `"noop"` means the operation was valid but made no change
- `"failed"` means the operation itself failed; an `error_code` is required
- `"skipped"` means the operation was not attempted because a prerequisite failed
- Dry-run ops never trigger verify
- Noop ops skip verify

When verify fails after successful edits, envelope `ok` is false but the mutation op statuses remain `"applied"`. That distinction is essential.

## Diff metadata

Mutation ops expose structured change metadata whether previewed or applied.

```json
{
  "op_id": "e0",
  "type": "edit",
  "file": "src/config.go",
  "status": "dry_run",
  "destructive": false,
  "lines_changed": 2,
  "lines_added": 3,
  "lines_removed": 1,
  "old_size": 5,
  "new_size": 7,
  "diff": "--- a/src/config.go\n+++ b/src/config.go\n@@ -10,4 +10,6 @@\n...",
  "diff_available": true
}
```

These fields SHOULD be present for `edit`, `write`, and `rename` whenever a diff is meaningful:

- `destructive`
- `lines_changed`
- `lines_added`
- `lines_removed`
- `old_size`
- `new_size`
- `diff`
- `diff_available`

The only difference between preview and commit is `status`.

## Verify

`verify` is top-level, never an op.

Fields:

- `status`: `"passed"`, `"failed"`, or `"skipped"`
- `command`: exact command that was run when verify was attempted
- `output`: stdout/stderr from the verify command
- `error`: process error string when verify failed
- `reason`: required when status is `"skipped"`

Rules:

- `verify` is `null` when no verify was attempted
- `verify.status` is the only source of truth for verify outcome
- `verify` MUST NOT use `ok: true/false`
- If verify is attempted, `command` MUST reflect the actual executed scope

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

Every failed op uses the same shape:

```json
{
  "op_id": "r0",
  "type": "read",
  "error": "symbol \"NonExistentSymbol\" not found in src/config.go",
  "error_code": "not_found"
}
```

Rules:

- `error` and `error_code` appear together or not at all
- Failed ops MUST NOT also include success-only payload fields like `content`, `matches`, or mutation `hash`
- Individual ops MUST NOT have their own `ok` field

Recommended codes:

| Code | Meaning |
|------|---------|
| `file_not_found` | File does not exist |
| `not_found` | Symbol or match target not found |
| `ambiguous_symbol` | Multiple symbols match; `candidates` included |
| `ambiguous_match` | Edit old-text matched multiple locations |
| `no_index` | No `.edr` directory |
| `empty_index` | Index exists but contains no usable files |
| `outside_repo` | Path is outside repository root |
| `hash_mismatch` | `expect_hash` precondition failed |
| `invalid_mode` | Mutually exclusive flags or impossible mode |
| `command_error` | Last-resort fallback only |

Every failed op MUST have an `error_code`. If a path can only produce `error` without a code, that path is underspecified.

## Validation

edr should be skeptical by default. These are hard errors, not warnings:

```json
{"ok": false, "errors": [{"code": "no_index", "message": "No .edr directory. Run: edr reindex"}]}
```

```json
{"ok": false, "errors": [{"code": "empty_index", "message": "Index contains 0 files. Run: edr reindex"}]}
```

```json
{"ok": false, "ops": [{"op_id":"r0","type":"read","error":"3 matches for 'parse'","error_code":"ambiguous_symbol","candidates":["parseConfig","parseFlags","parseEnv"]}],"errors":[]}
```

Validation rules:

- Unknown flags are invocation errors, not ignored warnings
- Ambiguous targets are errors, never auto-picked
- Contradictory flags are parse errors
- Paths outside repo are errors
- Empty success is only valid for commands where "zero results" is semantically real, such as search with `total_matches: 0`

## Batch / standalone parity

For any standalone invocation, there is an equivalent batch invocation that produces identical op payloads after ignoring only batch-incidental fields such as `op_id`.

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

Sessions are driven by the `EDR_SESSION` environment variable.

Rules:

- Without `EDR_SESSION`, reads always return full content; no dedup state exists
- With `EDR_SESSION`, content-returning ops include `session`
- `session` is one of:
  - `"new"`: first time this content was seen in the session
  - `"unchanged"`: same content as previously seen; body MAY be omitted
- If session detection is ambiguous, edr MUST fall back to no-session

Concurrency rules:

- Session files are per session, not per process
- Concurrent writers with the same `EDR_SESSION` MUST NOT corrupt session state
- Atomic rename is sufficient
- Last-write-wins is acceptable if the only consequence is a missed dedup

Session state MUST be bounded in size. A cap plus eviction is required.

## Result consistency

Agents should need exactly one parser:

1. Parse the envelope
2. Iterate `ops[]` in order
3. Check `error_code`
4. Switch on `type` for command-specific fields
5. Check top-level `verify`

If an agent needs separate parsers for batch vs standalone, or for `read --sig` vs `read --skeleton`, the contract has failed.

## Implementation discipline

The public contract above SHOULD be enforced by CI, but CI structure is not itself part of the product spec.

In practice, this means:

- contract tests should exist for help surface, envelope shape, parity, exit codes, session behavior, and failure placement
- documentation and command registry should be mechanically validated against each other
- any behavior described here that is not testable yet is a spec debt item, not an excuse to loosen the contract

## Non-goals

This spec does not require:

- natural-language interpretation of vague requests
- auto-healing silent fallbacks
- schema flexibility for convenience
- human-optimized prose output on stdout

The point of edr is not to feel smart. The point is to be a reliable component in an agent loop.
