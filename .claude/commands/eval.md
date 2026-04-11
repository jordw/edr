Run an interactive edr stress test and eval. Build edr from the current repo, then test it against real repos. Use `export EDR_ROOT=../repo` to target each repo.

## Setup

1. Run `go build -o edr . && go install` to build from current source
2. Run `edr status` to verify the build works

## Repos to test (use whichever are available in ../)

- Linux kernel (`../linux`) — C, 92K files
- VS Code (`../vscode`) — TypeScript, 8K files  
- Kubernetes (`../kubernetes`) — Go, 28K files
- Any others available in ../

## Tasks

For each repo, run these edr commands and evaluate the results. Record timing, surprises, and anything that feels wrong.

### 1. Ranking quality

Run bare `edr focus` for ambiguous names and check if the results are useful:
- Linux: `probe`, `init`, `open`, `rq`, `task_struct`, `sched_tick`
- VS Code: `ITextModel`, `dispose`, `render`, `open`
- Kubernetes: `Pod`, `Deployment`, `run`, `create`

For each, note: Did it resolve correctly? Was the shortlist useful? Did the right candidate rank first?

### 2. Speed

Time these operations and flag anything over 2 seconds:
- `edr focus <ambiguous_name>` on each repo
- `edr orient --grep <name> --budget 50` on each repo
- `edr focus <file>:<symbol>` (direct symbol read)
- `edr orient <directory>/ --budget 80`

### 3. Correctness

On one repo, do a full edit→search→undo cycle:
- Create a file with `edr edit test_file.c --content "test_marker" --mkdir`
- Search for it with `edr --search "test_marker"`
- Focus it with `edr focus test_file.c`
- Undo with `edr undo`
- Verify the file is gone with `edr focus test_file.c` (should fail)

### 4. Ergonomics

Test error handling:
- `edr focus nonexistent_file.c` — should exit 1 with clear error
- `edr focus file.c:nonexistent_symbol` — should exit 1
- `edr orient --grep "[invalid"` — should exit 1 with regex error
- `edr edit file.c --old "text"` (no --new) — should give clear error

Test orient at low budgets:
- `edr orient --budget 30` — should show directory summary, not random symbols

### 5. Import graph (if index is built)

Run `edr index` if needed, then check:
- Does `edr focus rq` resolve to `kernel/sched/sched.h` (heavily imported)?
- Does `edr focus task_struct` resolve to `include/linux/sched.h`?

## Baselines

Compare your findings against these known results:

| Query | Expected #1 | Notes |
|-------|-------------|-------|
| linux/probe | C driver file | Not Rust bindings |
| linux/rq | kernel/sched/sched.h | Import count signal |
| linux/task_struct | include/linux/sched.h | Auto-resolves |
| linux/sched_tick | kernel/sched/core.c | Auto-resolves |
| vscode/ITextModel | src/vs/editor/common/model.ts | Auto-resolves |
| linux/focus rq speed | < 2s | Trigram padding |
| linux/orient --grep speed | < 2s | Dirty walk fix |

## Report

After testing, report:
- **Speed**: X/10 — note any commands over 2s
- **Correctness**: X/10 — note any stale reads, wrong results, or broken undo
- **Ergonomics**: X/10 — note confusing output, missing errors, or unhelpful responses
- **Ranking**: X/10 — note any obviously wrong #1 picks

Focus on **surprises, wrong answers, and things that were slower than expected**. Don't report things that worked as expected unless they're notably good.
