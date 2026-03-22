# Bugs to Fix

## High

### 25. `--all` edit double-applies when new text contains old text
`edr -e file --old "X" --new "prefix_X" --all` replaces every occurrence of `X` including in the just-written `prefix_X`, causing double-prefixing.

Silently corrupts code. Agents using `--all` for prefix-style renames will produce `prefix_prefix_X`.

**Repro:**
```bash
echo "hz_to_bark(\nhz_to_bark(" > /tmp/test.c
edr -e /tmp/test.c --old "hz_to_bark(" --new "psy_hz_to_bark(" --all
```

**Expected:** Single-pass replacement (like `strings.ReplaceAll`), not iterative.

### 26. `edr -e` on files at repo root fails with "outside repo root"
Editing files at the repository top level (e.g., `CLAUDE.md`) can fail with `outside_repo` even though the file is inside the repo.

**Repro:**
```bash
edr -e CLAUDE.md --old "old text" --new "new text"
```

**Actual:** `{"ec":"outside_repo","error":"path \"CLAUDE.md\" is outside repo root"}`

### 29. Search with zero results outputs `{}` with no explanation
When text search finds nothing, the header is `{}`. Agents cannot distinguish "nothing matched" from "filter misconfiguration."

**Repro:**
```bash
edr -s "xyznonexistent" --text
```

**Expected:** `{"n":0}` so agents know the search ran and found nothing.

### 24. Session dedup returns empty body with no escape hatch
When a file is session-cached as "unchanged", agents see no content and no hint about how to force a re-read. They fall back to built-in Read, defeating edr's purpose.

**Expected:** Include a hint like `"use --full to force re-read"`, or make `--full` bypass session dedup.

## Medium

### 28. Map output header is always empty `{}` — no metadata
The `plainMap` renderer writes an empty JSON header before results. No count, no truncation indicator when `--budget` clips output.

**Expected:** Header should include `{"files":N,"symbols":M}` at minimum, and `{"trunc":true}` when budget caused truncation.

### 22. `--in` on search only accepts `file:Symbol`, not bare file paths
Scoped search (`--in`) requires `file:Symbol` format. Agents naturally try `--in file.go` to scope a text search to a file without knowing a symbol name.

**Expected:** Accept bare file paths (and optionally directories) to scope text search.

### 27. `--start-line`/`--end-line` not supported in batch `-e` mode
Batch edit mode rejects these flags, requiring fallback to standalone `edr edit`. The error message is helpful but the inconsistency is surprising.

**Expected:** Batch `-e` should accept `--start-line`/`--end-line` the same as standalone `edr edit`.

### 16. `run` drops first line of stdout
Shell command execution via `edr run` consistently drops the first line of stdout from `CombinedOutput`. Needs investigation.

**Repro:**
```bash
edr run --full --reset -- /bin/sh -c 'echo LINE1; echo LINE2; echo LINE3'
```

**Actual:** Shows `LINE2\nLINE3`, drops `LINE1`.

### 12. Batch dry-run edit + read does not show post-edit state
Chained edit-then-read shows the diff but the following read still shows pre-edit content.

**Expected:** Either the read should reflect the staged dry-run output, or dry-run chaining should be explicitly documented as unsupported.

### 17. Remove remaining legacy `EDR_FORMAT=json` support
`EDR_FORMAT=json` still works and the codebase still references it. Should be removed as a cleanup.

## Low

### 18/19. `write --content` treats `\n` as literal text
Backslash escapes in inline content are not interpreted. Shows up in both new-file writes and placement operations (`--after`, `--inside`).

**Expected:** Either interpret `\n`/`\t` escapes or document that callers must pass real newlines.

### 20. `--no-body` documented in help but not implemented
Search help advertises the flag but it errors at runtime.

**Expected:** Either implement or remove from help.

### 21. Garbled signatures output for some files
First signature line in some files shows a `// type "` artifact.

### 23. `--create-parents` does not exist
Agents may look for `--create-parents` but the flag is `--mkdir`.

**Expected:** Add `--create-parents` as a hidden alias for `--mkdir`.
