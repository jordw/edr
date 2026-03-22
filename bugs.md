# Bugs to Fix

## Critical

### 1. ~~Verify can pass with a real syntax error in the tree~~ FIXED
Standalone verify now uses `goListValidPackages` with `./...` fallback.
Verify failure output now includes compiler error tail (see #6).

### 2. ~~Contradictory write placement flags mutate files instead of erroring~~ FIXED
`--inside X --after X` (same symbol) now rejected before mutation.

## High

### 3. ~~Symbol search returns empty for indexed symbols~~ FIXED
SearchResult JSON tags used short wire names (`"m"`, `"n"`) but `plainSearch`
expected internal names (`"matches"`, `"total_matches"`). Fixed tags to use
internal names; wire transform handles shortening for JSON mode.

### 4. ~~`refs --deps` returns 0 results for non-leaf functions~~ FIXED
Data was present but `plainRefs` didn't render `deps`/`callers` fields from
ExpandResult. Added expand result handling to plain output.

### 5. ~~`--chain` returns 0 results for connected symbols~~ FIXED
Three issues: (1) arg parsing didn't handle 3-arg `[file, symbol, chain_target]`
form, (2) text-based fallback loaded AllSymbols per BFS node (36s → 36ms),
(3) BFS now tries both directions since caller/callee order is unknown.
Added chain result rendering to plain output.

### 6. ~~Verify failure gives no diagnostic detail~~ FIXED
Verify now includes output tail (up to 40 lines of compiler errors / test
failures) and `duration_ms` in the result. Plain output renders the tail
on failure/timeout.

### 7. ~~`session new` does not affect the current shell session~~ FIXED
PPID-based session routing now walks up the process tree to find the stable
ancestor (agent process), so `session new` mappings persist across tool calls
without needing `EDR_SESSION`.

## Medium

### 8. `--no-group` on text search returns empty results
The flag is accepted but suppresses normal results.

**Repro:**
```bash
edr search unchanged --text --no-group
```

**Actual:** `{}`

**Expected:** The same matches as grouped text search, just without file grouping.

### 9. `--context N` on text search does not show surrounding lines
Context changes line numbers but not the visible output.

**Repro:**
```bash
edr search unchanged --text --context 1
edr search '^func execute' --text --regex --context 2
```

**Actual:** Results appear to be the same hits with shifted line numbers, not actual surrounding lines.

**Expected:** Show the requested surrounding lines or reject the flag for unsupported modes.

### 10. Search `--limit N` reports total count, not displayed count
The header still reports total matches even when results are truncated.

**Repro:**
```bash
edr search unchanged --text --limit 5
```

**Actual:** Header showed `{"n":169}` while only 5 matches were displayed.

**Expected:** Either `{"n":5,"total":169}` or make `n` match the displayed count.

### 11. Line-range reads are not deduplicated across a stable session
Repeated line-range reads returned full content both times.

**Repro:**
```bash
EDR_SESSION=stress-session-1 edr read internal/session/session.go --lines 1:3
EDR_SESSION=stress-session-1 edr read internal/session/session.go --lines 1:3
```

**Actual:** Both reads returned the full body.

**Expected:** The second read should return an `unchanged`/delta result like symbol and full-file reads do.

### 12. Batch dry-run edit + read does not show post-edit state
The installed workflow says chained edit-then-read should show the post-edit view, but dry-run chaining does not.

**Repro:**
```bash
edr -e internal/session/session.go --old 'return "new", ""' --new 'return "brand_new", ""' --dry-run \
  -r internal/session/session.go --lines 441:446
```

**Actual:** The diff showed the proposed change, but the following read still showed `return "new", ""`.

**Expected:** Either the read should reflect the staged dry-run output, or dry-run chaining should be explicitly unsupported.

### 13. `run --full` does not consistently bypass session/baseline behavior
Full-output mode can still behave like deduped output on repeated runs.

**Repro:**
```bash
edr run --full -- echo "visible?"
edr run --full -- echo "visible?"
edr run --reset --full -- echo "visible?"
```

**Actual:** Repeated runs can suppress content until `--reset` is used.

**Expected:** `--full` should always show the raw command output.

### 14. Plain read output does not expose file hash
Read output lacks the hash needed for easy hash-guarded edits.

**Repro:**
```bash
edr read internal/session/session.go --lines 1:3
```

**Actual:** Output includes file and lines but no hash.

**Expected:** Include a hash in plain-format read output.

### 15. Batch vs standalone flag naming is inconsistent
Batch and standalone edit modes accept different flag names for the same concept.

**Repro:**
```bash
edr -e file.go --new "x"
edr edit file.go --old "x" --new "y"
```

**Actual:** One mode expects `--old/--new`, the other expects `--old-text/--new-text`.

**Expected:** Accept both forms everywhere or normalize the interface.

### 16. `run` stderr/stdout handling is unclear
Shell command execution does not make stream behavior obvious.

**Repro:**
```bash
edr run -- /bin/sh -c 'echo err 1>&2'
edr run -- /bin/sh -c 'echo out; echo err 1>&2'
```

**Actual:** The first case showed an empty body with `[1 lines, first run, exit 0]`; the second surfaced `err` but not `out`.

**Expected:** Clearly include combined output or explicitly label stdout vs stderr handling.

### 17. Remove remaining legacy `EDR_FORMAT=json` support and references
`format=json` is supposed to be removed, but the codepath and docs/comments still exist.

**Repro:**
```bash
EDR_FORMAT=json edr read internal/session/session.go --lines 1:3
edr search EDR_ --text --include 'internal/output/*.go'
```

**Actual:** JSON mode still works, and the codebase still references `EDR_FORMAT=json`.

**Expected:** Remove the legacy JSON-format path and its remaining references.

## Low

### 18. `write --content` treats `\n` as literal text
Backslash escapes in inline content are not converted to newlines.

**Repro:**
```bash
edr write tmp/new/nested/stress_file.go --content 'package nested\n' --mkdir --dry-run
```

**Actual:** The diff shows a literal `\n`.

**Expected:** Either interpret escapes or document that callers must pass real newlines.

### 19. Inserted write content can include literal escaped newlines in diffs
This shows up in placement operations as well as new-file writes.

**Repro:**
```bash
edr write internal/session/session.go --after ContentHash --content '\nfunc contentHashPrefix(data string) string {\n\tif len(data) < 8 {\n\t\treturn data\n\t}\n\treturn data[:8]\n}\n' --dry-run
```

**Actual:** The diff contains literal `\n` sequences instead of formatted multi-line code.

**Expected:** Render the inserted content as real lines in the preview.

### 20. `--no-body` is documented in help but not actually implemented
Search help advertises a negated body flag that errors at runtime.

**Repro:**
```bash
edr search LoadSession --no-body
```

**Actual:** `{"ec":"command_error","error":"unknown flag: --no-body"}`

**Expected:** Either implement `--no-body` or remove it from help text.

### 21. Garbled signatures output for some files
Some signatures output still contains formatting artifacts.

**Repro:**
```bash
edr -r internal/dispatch/dispatch_write.go --sig
```

**Actual:** First signature line shows a `// type "` artifact.

**Expected:** Clean signature rendering.

### 22. `--create-parents` does not exist
The workflow uses `--mkdir`, but agents may still look for `--create-parents`.

**Repro:**
```bash
edr write newdir/file.go --content 'x' --dry-run --create-parents
```

**Actual:** Unknown flag.

**Expected:** Either add `--create-parents` as an alias or keep docs/examples consistently on `--mkdir`.

### 22. `edr -s --in` only accepts `file:Symbol`, not bare file paths
Scoped search (`--in`) requires `file:Symbol` format, making it impossible to search within a file without specifying a symbol.

**Repro:**
```bash
edr -s "LoadSession" --text --in internal/session/session.go
```

**Actual:** `{"ec":"command_error","error":"--in requires file:Symbol format, got \"internal/session/session.go\""}`

**Expected:** Accept bare file paths to scope text search to a file (without requiring a symbol).

### 23. Plain search output only shows match count, not matching lines for symbol search
Symbol search (`edr -s` without `--text`) shows only a count in the header and no body content, making it impossible to see what matched without switching to `--text`.

**Repro:**
```bash
edr -s "Dispatch"
```

**Actual:** `{}` (empty header, no body lines showing symbol locations)

**Expected:** Show matched symbol names with file and line locations in the body, similar to text search output.

### 24. Session dedup returns empty body for unchanged files with no escape hatch
When a file is session-cached as "unchanged", the read returns no content at all. There is no way to force a re-read short of `edr session new` or `--full`, and `--full` is not obvious from the `{"session":"unchanged"}` output.

**Severity:** High — agents fall back to built-in Read when this happens, defeating the purpose of edr.

**Repro:**
```bash
edr session new
edr read src/file.c
# ... file is unchanged ...
edr read src/file.c
```

**Actual:** Second read returns `{"file":"src/file.c","session":"unchanged"}` with no body.

**Expected:** Either (a) include a hint like `"use --full to force re-read"`, or (b) make `--full` bypass session dedup so agents have a reliable escape hatch.

### 25. `--all` edit double-applies when new text contains old text
`edr -e file --old "X" --new "prefix_X" --all` replaces every occurrence of `X` including in the just-written `prefix_X`, causing double-prefixing.

**Severity:** High — silently corrupts code. Agents using `--all` for prefix-style renames will produce `prefix_prefix_X`.

**Repro:**
```bash
echo "hz_to_bark(\nhz_to_bark(" > /tmp/test.c
edr -e /tmp/test.c --old "hz_to_bark(" --new "psy_hz_to_bark(" --all
```

**Actual:** First occurrence becomes `psy_hz_to_bark(` correctly, but if `hz_to_bark(` appears inside the replacement zone of a prior match, it gets replaced again → `psy_psy_hz_to_bark(`.

**Expected:** `--all` should do a single-pass replacement (like `strings.ReplaceAll`), not iterative. Each original occurrence is replaced exactly once.

### 26. `edr -e` on files at repo root fails with "outside repo root"
Editing files at the repository top level (e.g., `CLAUDE.md`) can fail with `outside_repo` even though the file is inside the repo.

**Severity:** Medium — forces fallback to built-in Edit for top-level config files.

**Repro:**
```bash
edr -e CLAUDE.md --old "old text" --new "new text"
```

**Actual:** `{"ec":"outside_repo","error":"path \"CLAUDE.md\" is outside repo root"}`

**Expected:** Repo-root-relative paths like `CLAUDE.md` should resolve correctly.

### 27. `--start-line`/`--end-line` not supported in batch `-e` mode
Batch edit mode rejects `--start-line` and `--end-line` flags, requiring fallback to the standalone `edr edit` subcommand.

**Severity:** Medium — the error message is helpful ("this flag is available on `edr edit`"), but the inconsistency is surprising and forces agents to switch invocation styles mid-workflow.

**Repro:**
```bash
edr -e main.go --start-line 11 --end-line 19 --new "func main() { cmd.Execute() }" --dry-run
```

**Actual:** `{"ec":"command_error","error":"unknown flag: --start-line (this flag is available on \`edr edit\`, not in batch mode)"}`

**Expected:** Batch `-e` should accept `--start-line`/`--end-line` the same as standalone `edr edit`.
