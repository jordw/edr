# Bugs — Open Spec Violations

Triage date: 2026-03-21

This file tracks currently verified divergences from [spec.md](/Users/jordw/Documents/GitHub/edr/spec.md).

## P1 — Public transport and session contract

1. **`session new` bypasses the plain-mode transport contract**
   The spec makes `session` a public semantic command, and the transport section says stdout line 1 is always a JSON header.
   Actual behavior is a bare ID with no header:
   ```text
   77aacb8e
   ```
   The command is implemented directly in [cmd/commands.go](/Users/jordw/Documents/GitHub/edr/cmd/commands.go) via `fmt.Println(id)` in `sessionNewCmd`, bypassing the normal output renderer.

2. **`setup` bypasses the plain-mode transport contract**
   `setup` is also a public semantic command, but [cmd/setup.go](/Users/jordw/Documents/GitHub/edr/cmd/setup.go) has bespoke output paths instead of going through the normal dispatch/plain renderer.
   Reproduced outputs:
   ```text
   $ edr setup --status
     /Users/jordw/.claude/CLAUDE.md: outdated (installed:cb08c61 current:506ddf8)
     /Users/jordw/.codex/AGENTS.md: outdated (installed:cb08c61 current:506ddf8)
     /Users/jordw/.cursor/rules/edr.mdc: outdated (installed:cb08c61 current:506ddf8)
   ```
   ```json
   {"current_hash":"506ddf8","global":[...],"op_id":"s0","type":"setup"}
   ```
   The first is raw human text; the second (`--json`) is a legacy op object, not the spec's header+body transport.

3. **Session dedup is broken for `search`, `map`, and `refs`**
   The spec says content-returning ops include `session`, and repeated calls in the same session should return `session:"unchanged"` with the body omitted when content is unchanged.
   Repeated calls with the same explicit session keep returning `session:"new"` and the full body for all three commands:
   ```text
   $ EDR_SESSION=triage4 EDR_NO_HINTS=1 edr refs main.go:main
   {"n":5,"session":"new","sym":"main.go:main"}
   ...
   $ EDR_SESSION=triage4 EDR_NO_HINTS=1 edr refs main.go:main
   {"n":5,"session":"new","sym":"main.go:main"}
   ...
   ```
   The same happens for repeated `search` and `map` calls. The intended content-hash path exists in [internal/session/session.go](/Users/jordw/Documents/GitHub/edr/internal/session/session.go), but the current CLI behavior never surfaces `session:"unchanged"` for these commands.

4. **Contract tests still validate the removed envelope-style JSON transport**
   The spec says docs are tested contracts, but the test suite is still protecting the legacy JSON envelope rather than the current plain-mode contract.
   Examples:
   - [cmd/contract_test.go](/Users/jordw/Documents/GitHub/edr/cmd/contract_test.go) still unmarshals reads into `ops[0]` and checks legacy fields like `"c"` and `"unchanged"`.
   - [cmd/exit_code_test.go](/Users/jordw/Documents/GitHub/edr/cmd/exit_code_test.go) still expects `setup --status --json` to return an envelope with `ok` and `ops`.

## P2 — Output shape and determinism

5. **Default text-search output is flat, so `--no-group` is effectively a no-op**
   The spec says search results are grep-style match lines grouped by file, and `--no-group` disables that grouping.
   Current output is already flat by default, and `--no-group` produces identical output. This is visible both in command behavior and in [internal/output/plain.go](/Users/jordw/Documents/GitHub/edr/internal/output/plain.go), where `plainSearch` emits line-by-line matches with no grouped file section rendering.

6. **Go verify scope ordering is nondeterministic**
   The spec puts determinism ahead of correctness and also says the executed verify command must be reported exactly.
   [internal/dispatch/dispatch_verify.go](/Users/jordw/Documents/GitHub/edr/internal/dispatch/dispatch_verify.go) builds Go verify scopes from maps and joins them without sorting in `goVerifyScope`, and also accumulates reverse importers unsorted in `goReverseImporters`.
   That means the reported `go build ...` / `go test ...` command can vary across runs for the same touched file set.

## P3 — Spec self-contradictions

7. **Edit "always has file and status" vs "failed ops MUST NOT include file"**
   The spec's "One command, one result shape" section says: "An edit header always has `file` and `status`; `hash` is present on `applied`."
   The spec's "Failure shape" section says: "Failed ops MUST NOT also include success-only fields (`file`, `lines`, `hash`, etc.)"
   These contradict each other. The implementation follows the failure-shape rule: failed edits return structured errors without `file` or `status`.

8. **`run` exit-code rules contradict the transport contract**
   The transport section says there are only two public exit codes: `0` for success and `1` for failure.
   The `run` section says the wrapped command's exit code passes through.
   Current implementation follows pass-through: `edr run -- /bin/sh -c 'exit 7'` exits with code `7`.
   The spec needs to choose one rule.

## P1 — Transport contract (continued)

9. **`run` bypasses the plain-mode transport contract**
   The transport section says "Stdout line 1 is always a JSON object (the header)."
   `run` emits raw command output with no JSON header:
   ```text
   $ edr run --reset -- echo hello
   hello
   ```
   [cmd/run.go](/Users/jordw/Documents/GitHub/edr/cmd/run.go) writes directly to stdout via `os.Stdout.Write(out)` and `fmt.Print(output)`, bypassing the header+body renderer entirely.

## P2 — Output shape and determinism (continued)

10. **`run` identical-output format diverges from spec**
    The spec says identical output prints `[no changes, N lines]`.
    The implementation prints `[no changes]` with no line count:
    ```go
    // cmd/run.go
    return "[no changes]\n"
    ```

11. **`--no-expect-hash` not exposed on edit**
    The spec says edr SHOULD expose `--no-expect-hash` for intentional blind edits.
    The flag does not exist — only `--expect-hash` is registered. Agents have no explicit opt-out for the stale-read precondition.

12. **Standalone `verify` header has undocumented fields**
    The spec's verify header shape is `{"verify":"passed"|"failed"|"skipped"}` with optional `error` and `reason`.
    Standalone `edr verify` returns extra fields not in the spec:
    ```text
    $ edr verify --command "echo ok"
    {"command":"echo ok","verify":"passed"}
    $ edr verify --command "false"
    {"command":"false","error":"exit status 1","output":"fail","verify":"failed"}
    ```
    The `command` and `output` fields are not in the field name table or verify section.

13. **`refs --budget` does not report `budget_used`**
    The spec says "Ops that shape text output by budget SHOULD report `budget_used`."
    `refs` accepts `--budget` but never includes `budget_used` in the header:
    ```text
    $ edr refs main.go:main --budget 100
    {"n":5,"session":"new","sym":"main.go:main"}
    ```
