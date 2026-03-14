# bench/ — EDR Benchmark Suite

## Tracks

| File | Track | What it measures | How to run |
|---|---|---|---|
| `runtime_test.go` | Runtime | Wall time, allocations, DB size for individual commands | `go test ./bench/ -bench . -benchmem` |
| `context_test.go` | Context efficiency | Workflow response sizes, regression gates | `go test ./bench/ -run 'Test(ResponseSize\|Signatures)' -v` |
| `correctness_test.go` | Correctness | Ambiguous symbols, refs precision, rename safety, edit+reindex | `go test ./bench/ -run TestCorrectness -v` |
| `session_bench_test.go` | Session lifecycle | 55-call multi-language session with trace validation | `go test ./bench/ -run TestSessionMultiLang -v` |
| `scenario_test.go` | Scenario validation | JSON scenario schema/routing validation | `go test ./bench/ -run TestScenarioDispatch -v` |
| `native_comparison.sh` | Shell comparison | edr vs native tools (Read/Grep/Glob) on real repos | `bash bench/native_comparison.sh bench/scenarios/fixture.json` |

### What each track does and does not claim

**Runtime** measures latency and throughput of individual commands against the
bundled testdata. It does not claim to represent real-world repo performance.
Use the shell comparison track for that.

**Context efficiency** validates that progressive disclosure features (signatures,
depth, budget) produce smaller responses than naive reads, and that response sizes
don't regress. It does not measure query latency.

**Correctness** uses adversarial fixtures to test edge cases: same symbol name across
files/languages, shadowed locals, aliased imports, rename safety. Precision thresholds
are currently permissive (>= 0.5) and documented per-test. This track gates all
optimization work.

**Session lifecycle** exercises a full agent workflow (orient, read, search, explore,
edit, verify) with session optimizations (delta reads, body dedup, slim edits).
It validates session-level behavior, not individual command correctness.

**Scenario validation** checks that JSON scenario files parse correctly and their
commands dispatch without errors. It does not validate end-to-end equivalence with
the shell runner (see below).

**Shell comparison** is the context-efficiency benchmark for real repos. It compares
edr output sizes against what an agent would get from native tools (cat, grep, glob).
This is the externally credible comparison.

## Shared infrastructure

- `helpers_test.go` — `setupRepo`, `dispatchJSON`, `benchDispatch`, `heapAllocKB`

## Scenario files

JSON scenarios in `scenarios/` are the single source of truth for benchmark
definitions. Both the Go tests and the shell runner consume them directly.

```
scenarios/fixture.json          # bundled testdata (Go tests + shell)
scenarios/real/*.json           # real repos (shell runner)
profiles/*.sh                   # legacy shell profiles (backward compat only)
```

### Two consumers, different path semantics

The **Go tests** copy `bench/testdata/` to a temp dir and run dispatch in-process.
The scenario's path fields (root, dir) don't apply to the temp dir layout.
`TestScenarioDispatch` validates command routing (schema correctness), not paths.

The **shell runner** operates against real repo checkouts with BENCH_ROOT-relative
paths. It reads scenario fields directly via `jq`. This is the authoritative
consumer for real-repo path semantics.

## Test data

`testdata/` — Multi-language task queue system used by all Go benchmark tracks:
Go, Python, Rust, C/H, Java, Ruby, JS, TSX.

`testdata/adversarial/` — Targeted correctness fixtures:
Go (pkg_a, pkg_b with identical symbol names), Python (aliased imports),
JS (aliased imports). Used only by `correctness_test.go`.

## JSON output for automation

```bash
go run ./bench/cmd/benchjson                  # all benchmarks + tests → JSON
go run ./bench/cmd/benchjson -o results.json  # write to file
go run ./bench/cmd/benchjson -count 3         # repeated benchmark iterations
```

Tests and benchmarks run as separate processes to avoid global state interactions.
