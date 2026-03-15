# Real Repo Benchmark Set

Use the same workflow shapes across a small repo, a large repo subtree, and a
few non-Go repos. That gives you a headline benchmark and a credibility set
without letting monorepo noise dominate every number.

## Recommended set

1. `urfave/cli`
   - Use this for the top-line benchmark.
   - Goal: clean, medium-sized, mostly Go benchmark with approachable numbers.

2. `vitess/go/vt/sqlparser`
   - Use this as the first large-repo target.
   - Goal: dense library code with lots of symbol/read/search/refs pressure.

3. `vitess/go/vt/vtgate`
   - Use this as the larger orchestration target.
   - Goal: stress orientation, cross-file exploration, and larger ref graphs.

4. `pallets/click`
   - Use `src/click` as the benchmark root.
   - Goal: source-only Python benchmark with real CLI parsing flows.

5. `rails/thor`
   - Use `lib/thor` as the benchmark root.
   - Goal: source-only Ruby benchmark with option parsing and command wiring.

6. `reduxjs/redux-toolkit`
   - Use `packages/toolkit/src` as the benchmark root.
   - Goal: TypeScript benchmark with a real package-sized source tree.

7. `django/django`
   - Use `django` (the source package) as the benchmark root.
   - Goal: large Python codebase (1500+ files). Stress-tests indexing, ref
     graphs, and search at scale. The other repos are all tightly-scoped
     libraries (30-100 files); Django proves edr works on real-world codebases.

## Workflow set

Run the same scenarios everywhere:

- `Understand API`: full-file read vs `edr read ... --signatures`
- `Read symbol`: full-file read vs `edr read file:Symbol`
- `Find refs`: `grep` + read matched files vs `edr refs`
- `Search with context`: `grep -C3` vs `edr search --text --context 3`
- `Orient`: `glob` + 3-5 reads vs `edr map --budget 500`
- `Edit function`: `read + edit + verify/read` vs `edr edit --dry-run`
- `Multi-file read`: separate reads vs one batched `edr read`
- `Explore symbol`: `grep` + 2 reads vs `edr explore --body --callers --deps`

`Add method` is optional. Only include it when the target has a clean container
edit case.

## Symbol selection rubric

Do not hardcode symbols from memory. Index the repo first, then choose targets
that fit the same shape in each benchmark:

- API target: exported type with 5-15 methods
- Read-symbol target: function roughly 30-100 LOC
- Refs target: symbol with 2-10 references
- Explore target: symbol with at least one caller and one dependency
- Search target: a term like `retry`, `parser`, `plan`, `route`, or `schema`
  that returns several hits but not hundreds

## Running it

Fast path:

```bash
bash bench/run_real_repo_benchmarks.sh
```

This clones `urfave/cli`, `vitessio/vitess`, `pallets/click`, `rails/thor`,
and `reduxjs/redux-toolkit` into `/tmp` by default, then runs:

- [urfave_cli.sh](profiles/real/urfave_cli.sh)
- [vitess_sqlparser.sh](profiles/real/vitess_sqlparser.sh)
- [vitess_vtgate.sh](profiles/real/vitess_vtgate.sh)
- [click.sh](profiles/real/click.sh)
- [thor.sh](profiles/real/thor.sh)
- [redux_toolkit.sh](profiles/real/redux_toolkit.sh)

Override checkout and output locations with `BASE_DIR` and `RESULTS_DIR`.

1. Copy [template.sh](profiles/template.sh)
   to a repo-specific profile.
2. Fill in `BENCH_ROOT`, `SCOPE_DIR`, and the scenario variables.
3. Run:

```bash
bash bench/native_comparison.sh /path/to/profile.sh
```

If you omit the profile path, the script defaults to the bundled fixture profile
at [fixture.sh](profiles/fixture.sh).
