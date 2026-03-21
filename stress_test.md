You are testing `edr`, a CLI tool for coding agents. Your goal is to use it on this codebase as a real agent would — navigating, understanding, editing, and verifying code — then report what broke or felt wrong.

**Important:** Your agent config already has edr instructions installed (via `edr setup`). Use those instructions to figure out how to accomplish the tasks below — do not look at this file for edr command syntax. The point is to test whether the installed instructions are sufficient for an agent to discover and use edr correctly.

If a task is unclear or the instructions don't cover it, note that as an instruction gap finding.

---

## Phase 1: Orient and understand

1. Get a high-level overview of this codebase. Keep it under 300 tokens.
2. Narrow the overview to just the `internal/dispatch/` directory.
3. Now narrow further: only Go files, only symbols whose names contain "Read".
4. Get the overview of a single file — pick one that looks interesting from the map.

## Phase 2: Read code at different depths

5. Read just the signatures of a file with many exported symbols.
6. Read the skeleton of that same file — compare what you learn vs signatures.
7. Pick a function that caught your eye. Read just that function's source.
8. Read lines 1–30 of a file to see its imports and package declaration.
9. Read a file with a tight token budget (~200 tokens). Did anything get cut that matters?
10. Read two different files in a single call. Did batching work as expected?
11. Force a full read of a file you've already read in this session. Does it bypass the session cache?

## Phase 3: Search

12. You want to find where sessions are loaded. Search for the right symbol.
13. Now search for the text string `"unchanged"` across the codebase.
14. Repeat that search but only in Go files.
15. Search for a regex pattern — find all functions that start with `execute`.
16. Search with context lines so you can see surrounding code.
17. Cap the search to 5 results.
18. Search for a symbol but suppress the body — you only want names and locations.
19. Run a text search but disable file grouping in the output.
20. Search for a pattern but only within the body of a specific function.

## Phase 4: Understand dependencies

21. Pick a function central to the codebase. Find all references to it.
22. Now find its transitive callers — what's the full impact if you change it?
23. Find the call chain from a high-level entry point down to that function.
24. Find what that function calls (its dependencies).
25. Get just the signatures of everything that references it — compact output.

## Phase 5: Make changes (dry-run only)

26. Pick a string constant in the codebase. Dry-run changing it to something else.
27. That string appears in multiple places. Dry-run replacing all occurrences.
28. Dry-run a line-range edit: replace lines 5–10 of a file with new content.
29. A function has a bug in one specific method. Dry-run an edit scoped to just that method's body so you don't accidentally match elsewhere.
30. You got a file hash from an earlier read. Use it to do a hash-guarded dry-run edit.
31. Dry-run an edit, then immediately read back the affected lines to confirm the change — in one call.

## Phase 6: Write new code (dry-run only)

32. Dry-run writing a brand new file that doesn't exist yet.
33. A struct needs a new method. Dry-run inserting it inside the struct's container.
34. A function needs a helper placed right after it. Dry-run writing it there.
35. Dry-run appending a comment block to the end of a file.
36. Dry-run writing a file in a directory that doesn't exist yet — use the create-parents option.

## Phase 7: Rename

37. Pick a helper function. Dry-run renaming it to something better. Check that all call sites and imports update.

## Phase 8: Verify and run

38. Run the project's build verification with auto-detection.
39. Run it again but at the test level instead of just build.
40. Override the verify command with something custom.
41. Run a short timeout verification against a slow command — does it timeout properly?
42. Run a shell command through edr. Run it again — confirm the output diff shows no changes.
43. Run a different command. Confirm the diff highlights what changed.
44. Run with full output mode to bypass the diffing.
45. Clear the run baseline and run again — should look like a first run.

## Phase 9: Sessions

46. Start a fresh session.
47. Read a file. Read it again. The second read should be deduplicated.
48. Do a dry-run edit on a file, then read it. What does the session report?

## Phase 10: Batch combinations

49. In a single call: read a file's signatures, search for a text pattern, and read another file.
50. In a single call: dry-run edit two different files.
51. In a single call: edit a file and then read back the edited region to confirm.

## Phase 11: Error handling and edge cases

Do NOT look up the expected error codes — just try these and evaluate whether the errors you get back are clear, structured, and actionable for an agent.

52. Try to read a file that doesn't exist.
53. Try to read a symbol that doesn't exist in a file that does.
54. Try to read a symbol name that's ambiguous (exists in multiple files).
55. Search for an empty string.
56. Try to write a file outside the repository root.
57. Try to edit a string that doesn't appear in the target file.
58. Try to read a line range that's past the end of the file.
59. Try to edit with a hash that doesn't match the file's current hash.
60. Try to edit where old and new text are identical.
61. Try to rename a symbol to its current name.
62. Try to use contradictory flags — like requesting both signatures and skeleton on a read.
63. Try signatures mode on a leaf function (not a container).
64. Try to write with both placement flags at once (inside + after).
65. Try a symbol search but pass context lines — does it make sense or error?
66. Batch a read of a valid file and a read of an invalid file in one call. Does the valid read still succeed?
67. Run edr with no arguments at all.
68. Pass an unknown flag to a batch operation.
69. Try to run an internal-only command directly.
70. Run a command that exits non-zero through `edr run`. Does the exit code pass through?
71. Run a command that writes to stderr. How is it handled?
72. Introduce a real syntax error via edit, then verify. Confirm it reports failure with details. Revert the edit after.

## Phase 12: Report

After testing, produce a single prioritized report:

```
## Bugs (something is broken or produces wrong output)
1. [critical|high|medium|low] description — reproduction command — actual vs expected

## Ergonomic issues (awkward, confusing, or wasteful for agents)
1. [high|medium|low] description — what would be better

## Instruction gaps (couldn't figure out how to do something from instructions alone)
1. description — what was the task, what was missing or misleading
```

Rank each section by impact on agent trust and token efficiency. Include the exact command and output for every bug.
