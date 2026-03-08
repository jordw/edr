# EDR Roadmap

From "better repo tool" to "system agents can genuinely depend on."

---

## Phase 1: Foundations (weeks 1–2)

### Session trace collection

Instrument every MCP session to capture structured traces: query issued, results returned, which result the agent used next, whether the final edit passed verification. Store in SQLite alongside the index.

This is the foundation for everything else. Without traces, you can't train ranking, learn patterns, or measure what works.

Add `edr bench-session` that scores a completed session: tool calls used, tokens consumed, edits that stuck vs. reverted, verify passes/failures. This is the optimization target for every feature that follows.

### Persistent session state

Today, diffs, read deltas, and "already seen" bodies live only in process memory. That's fine for one MCP connection, but it means normal CLI calls forget everything and `diff` becomes much less useful across reconnects.

Persist session state in SQLite with explicit session IDs:

- resume a previous session
- inspect recent diffs after reconnect
- keep body-dedup and delta-reads working across long-running agent tasks

Phase 5's session memory graph should build on this, not reinvent it.

### Bug fixes from evaluation

- `explore` with nonexistent symbol returns `ok: true` with empty data instead of an error. Every other command errors correctly — explore is the outlier. Fix in `dispatch_explore.go:runExpand`.
- `diff` query returns "no diff stored" with no explanation that it's session-scoped. Improve the error message or fall back to `git diff`.
- `write --inside` doesn't accept `--new_text` in CLI mode (requires stdin), inconsistent with `edit`.

### Index health + freshness reporting

Agents need to know when the graph is incomplete or stale.

Add `edr status` / `edr doctor` that reports:

- index freshness
- files skipped as unsupported
- parse failures encountered during indexing
- per-language coverage stats
- whether the current command is running against a stale index snapshot

Also make CLI freshness behavior match MCP: if the repo is stale, auto-reindex before serving queries instead of only indexing when the DB is empty.

### Semantic refs for remaining languages

Go, Python, and JS/TS have import extraction and import-aware ref resolution. C, Rust, Java, and Ruby fall back to text-based refs — every `rename`, `refs`, and `explore --callers` is noisier for those languages. Add:

- **Rust**: `use` statements → import table, `mod` declarations for module resolution
- **Java**: `import` statements → import table, package-qualified resolution
- **Ruby**: `require` / `require_relative` → import table

This is foundational work — it makes Phases 2–3 (blast radius, ranking) more accurate across all supported languages.

### Stable symbol identity

The index already knows symbol nesting, but most user-facing operations still address symbols by bare name. That gets ambiguous fast and blocks later work like temporal history, conflict resolution, and high-precision refactors.

Add a stable symbol identity layer:

- fully-qualified symbol addresses (`pkg.Type.Method`, `file::Class.method`, etc.)
- persistent symbol IDs across one indexed snapshot
- parent/child-aware resolution for overloaded or repeated names

Everything in later phases that says "symbol" should eventually mean this identity, not just a raw string.

### Git diff integration

`diff` queries should fall back to `git diff` when no session-scoped diff is stored. Beyond fixing the error message, add `edr diff --file src/main.go` as a first-class command that maps uncommitted changes onto symbols: "which symbols changed since last commit?" Useful for `verify` scoping and the cascade planner in Phase 2.

### Doc comment indexing

Tree-sitter already parses comments adjacent to symbols. Extract and store doc comments / docstrings in the index alongside symbol metadata. Surface them in `map`, `search`, and `explore` output. Zero-model version of `edr explain` — lets agents understand intent without reading full bodies.

---

## Phase 2: Cascade planner + speculative verify (weeks 3–4)

### Semantic edit simulator

Before applying a patch, predict blast radius. Walk the refs graph from the edit target, check if the change affects a function signature or type definition, flag all direct consumers and associated tests.

```
edr simulate edit --file dispatch.go \
  --old_text "func Dispatch(ctx" \
  --new_text "func Dispatch(logger *log.Logger, ctx"
```

Returns: files that will break, callers that need signature updates, tests to run, symbols likely to become stale. No model needed — pure graph walking on the existing refs system.

MCP: `edr_do(edits: [...], simulate: true)` returns the impact report without applying.

### Speculative edit-verify-rollback

`edr_do(edits: [...], verify: true, speculative: true)`

Internally: snapshot → apply edits → run verify → if fail, rollback and return the error with the diff that caused it. If pass, commit. One call. The agent never sees a broken state.

This eliminates the most expensive agent failure mode: making a correct edit to file A that silently breaks file B, then spending 5 calls diagnosing and recovering.

### Transactional edit safety

Before speculative verify becomes a core primitive, tighten the write path so edits don't land in a half-successful state.

Specifically:

- rollback file edits if re-indexing fails
- surface partial-failure states explicitly
- expose a durable "applied / indexed / verified" result model instead of a single `ok`

Agents can recover from a failed edit. They recover badly from a file that changed successfully while the graph silently fell behind.

### Targeted test selection

Use the refs graph to answer "which tests are affected by this edit?" Run only those tests instead of the full suite. `edr_do(edits: [...], verify: true)` already runs verify — making it run *only affected tests* turns a 60-second build into a 2-second check. Pure graph walking, no ML.

```
edr verify --affected src/dispatch.go   # only tests that transitively depend on changed symbols
```

Pairs with speculative verify: fast feedback loop means more speculative edits are practical.

Expand this into a general targeted verify planner:

- `edr verify --affected src/dispatch.go`
- `edr verify --symbol Dispatch`
- `edr verify --from-diff`
- `edr verify --plan`

The output should distinguish:

- cheap checks to run now
- tests likely affected by the edit
- full-repo verification still required before commit

### Counterfactual refs

```
edr refs Dispatch --counterfactual delete
```

"What breaks if I delete this symbol?" — a ranked behavioral impact report, not just raw references. Combines impact analysis with type-checking to distinguish "this caller will fail to compile" from "this test mentions the name in a string."

---

## Phase 3: Learned symbol ranking (weeks 5–8)

### Synthetic training data from the index

The repo itself is the training data generator. No real user traces needed to bootstrap:

- Pick a symbol. Its callers, deps, and tests are known ground truth. Generate: *"task: modify `{symbol}`, correct files: `{callers + tests}`"*
- Pick two symbols connected by a call chain. Generate: *"task: find path from `{A}` to `{B}`, answer: `{refs --chain}`"*
- Pick a recent commit. The diff says which symbols changed. Generate: *"task: `{commit message}`, relevant symbols: `{changed symbols}`"*
- Pick a function, hypothetically delete it. Refs graph says what breaks. Generate: *"task: what breaks if I remove `{symbol}`, answer: `{refs --impact}`"*

Run across 1000 open-source repos → millions of labeled ranking examples, clean by construction.

### Reranker model

Start with XGBoost over ~30 structural features: tree depth in call graph, name similarity, same-package, same-file, is-test, is-caller, is-dep, task type. If that plateaus, upgrade to a 20M-param neural cross-encoder.

Input: `(query, task_type, candidate_symbol_with_context)`. Output: relevance score.

Ship as a compiled ONNX blob (or native Go decision tree) inside the edr binary. No Python, no API calls, <10ms per query.

This is the force multiplier — better ranking improves `search`, `explore`, `refs`, `map`, everything that returns a list. The agent finds what it needs on the first search instead of the third.

### Semantic rename

Make `rename` truly semantic before expanding into natural-language refactors.

Today, a rename that starts from raw identifier occurrences is still vulnerable to unrelated same-name symbols and string-adjacent false positives. Upgrade it to operate on resolved symbol identity plus import-aware refs, with a preview that explains why each occurrence is in-scope.

This is the first refactor primitive agents will trust or distrust. It needs to be boringly correct.

### Where synthetic beats real traces

Real agent traces are noisy — agents make wrong choices, go down dead ends, read irrelevant files. Synthetic data from the ground-truth graph is clean by construction. Real traces later teach the human-taste layer (implicit couplings like "when people change the parser they also update the formatter"), but the first 80% is fully derivable from structure.

---

## Phase 3.5: Language breadth + performance (weeks 7–8)

### New language support

8 languages covers most repos but leaves out major ecosystems. Priority order by real-world usage:

- **C++ / C#** — massive codebases, tree-sitter grammars mature, high demand
- **Kotlin / Swift** — mobile-first shops need these
- **PHP / Scala** — large legacy codebases where agents add the most value

Each language needs: tree-sitter grammar binding, `LangConfig` entry, symbol node types, signature extraction, and (ideally) import resolution. Ship as build tags so users can compile a slim binary with only the languages they need.

### Parallel indexing

`IndexRepo` parses files sequentially. Tree-sitter parsing is per-file with no shared state — fan out across cores. For large repos (10k+ files), cold-start `edr init` could drop from 30s to 5s on an 8-core machine. Use a worker pool bounded by `runtime.NumCPU()`, merge results under the existing write lock.

### Richer signature extraction

`rubySignature` is a 3-line stub. Rust and Java signatures are minimal (just the declaration line). Extract return types, parameter types, generics, and trait bounds. Better signatures → `--signatures` becomes genuinely useful for all languages, not just Go and Python.

---

## Phase 4: Semantic compression (weeks 9–10)

### Agent context packets

```
edr pack internal/dispatch/ --budget 2000
```

Produce an ultra-dense subsystem summary — not the map (too sparse) and not the code (too verbose), but a structured intermediate: key types, their relationships, main control flow paths, invariants, and gotchas.

The agent loads the packet, understands a subsystem in 2k tokens instead of 30k, then drills into specific symbols only when needed. Generated by reading code + refs graph and compressing with a model.

### Intent-aware repo maps

```
edr map --intent "debug"    # emphasizes error paths, logging, test coverage gaps
edr map --intent "rename"   # emphasizes all refs, cross-file usage, public API surface
edr map --intent "add-test" # emphasizes uncovered symbols, test patterns, fixtures
```

Different maps for different tasks, instead of one generic symbol listing. The intent biases which symbols appear, how they're ordered, and what metadata is shown.

---

## Phase 4.5: Project configuration (weeks 10–11)

### `.edr.toml` project config

No config file today. As edr moves toward multi-agent and team use, per-project configuration becomes essential:

```toml
[index]
exclude = ["vendor/**", "generated/**", "*.pb.go"]
languages = ["go", "typescript"]   # only index these (smaller binary story)

[verify]
command = "go test ./..."
timeout = 120
affected_only = true               # use targeted test selection by default

[budgets]
default_read = 500
default_map = 300

[search]
exclude_patterns = ["*_generated.go", "*.min.js"]
```

Subsumes scattered flags into a declarative, version-controlled file. Agents read it once; humans edit it rarely.

### Monorepo / multi-root support

Currently edr assumes one repo root with one index. Monorepos with multiple Go modules, JS workspaces, or polyglot subdirectories need:

- Multiple index roots (one `.edr/` per module root, or a unified index with root-scoped queries)
- Cross-root `refs` and `rename` that understand module boundaries
- `edr map --root services/auth/` to scope orientation to a subtree

---

## Phase 5: Multi-agent infrastructure (weeks 11–14)

### Session memory graph

Track what the agent has read, edited, and verified across the full session. Bias future outputs toward novelty and unresolved dependencies. When the agent reads a file, highlight symbols it hasn't visited that are in the dependency cone of its current task.

This should extend the persisted session foundation from Phase 1, not rely on process-local memory.

### File watch mode

`edr watch` — inotify/fsevents-based daemon that keeps the index hot as files change outside of edr. Essential for multi-agent scenarios where agents edit files through other tools, and for human-in-the-loop workflows where the developer edits in their IDE while an agent works in parallel.

### MCP-first orchestration layer

Multiple agents share one live edr session with:
- **Leases**: agent A claims exclusive edit access to `dispatch.go`
- **Locks**: writer lock already exists; extend to semantic regions (symbol-level locks)
- **Shared notes**: agent A's edits are visible to agent B's reads immediately
- **Task-local views**: each agent sees the repo filtered to its task's dependency cone

### Agent-native conflict resolution

When two agents race on the same file, edr detects semantic overlap. If the edits are to disjoint symbols, auto-merge. If they touch the same symbol, surface the conflict with both proposed changes and ask for disambiguation. No git merge — structural merge using the symbol graph.

---

## Phase 6: Specialized reasoning workflows (weeks 15+)

### Repository copilots as tools

Each is a specialized workflow over the same index:

- `edr security` — taint analysis from user inputs to unsafe sinks, flagged as refs/explore output
- `edr test-gap` — symbols with high fan-in but no test coverage
- `edr migration` — API change impact across all consumers, with edit plan generation
- `edr drift` — compare current dependency structure against intended layering, flag violations

### Natural-language refactors

```
edr refactor "split this function into validation and execution" --target processTask
edr refactor "inline this abstraction" --target HandlerRegistry
edr refactor "move this method next to its main caller" --target commitEdits
```

All previewable (`--dry-run`) and reversible. Builds on the cascade planner from Phase 2.

### API surface command

```
edr api internal/dispatch/    # exported symbols + signatures only
edr api --diff HEAD~5         # what changed in the public API since 5 commits ago
```

Show only exported/public symbols and their signatures for a package or directory. Useful for understanding boundaries, generating documentation stubs, and validating that a refactor didn't accidentally change the public contract.

### Edit history + undo

Lightweight edit journal — last N edits per file with before/after content snapshots, stored in `.edr/history/`. `edr undo` reverts the last edit without git. Agents can speculatively edit and roll back without `git checkout`. Pairs with speculative verify from Phase 2 but works standalone.

```
edr undo                      # revert last edit
edr undo --file src/main.go   # revert last edit to specific file
edr history src/main.go       # show recent edits
```

### Temporal repo intelligence

Snapshot symbol graphs over time. Answer questions like:
- "What changed this subsystem's shape over the last 20 commits?"
- "Which symbols churn the most?"
- "When did this dependency get introduced?"

Index git history into the symbol graph — each commit becomes a graph snapshot. Diff the graphs, not the text.

### Execution-trace indexing

Index runtime traces, failing test stacks, logs, and benchmark output into the same symbol graph. A stack trace becomes refs from the crash site back through the call chain. A failing test becomes a weighted edge to the symbols it exercises.

```
edr explore parseConfig --traces   # shows runtime callers, not just static refs
```

### "Why is this here?" mode

```
edr explain commitEdits
```

Synthesize the purpose of a symbol from its callers (who needs it), history (when/why it was added), tests (what behavior it guarantees), and nearby comments. Return with confidence score and evidence links.

---

## Design principles

1. **The data flywheel comes first.** Trace collection enables ranking, ranking enables better traces. Build the instrumentation before the features.
2. **Synthetic data before real data.** The repo's own structure provides clean, unlimited training signal. Real traces add the last 20%.
3. **No model until you need one.** Cascade planning, speculative verify, and counterfactual refs are pure graph algorithms. Ship them without ML dependencies.
4. **Ship inside the binary.** No Python, no API calls, no sidecar processes. If it needs a model, it ships as ONNX or a native Go decision tree.
5. **Every feature must reduce round trips.** The metric is tool calls per successful edit, not feature count.
6. **Language breadth compounds.** Every feature — refs, rename, signatures, ranking — gets better across more codebases when more languages have full semantic support. Prioritize depth (imports/refs) for existing languages before adding new ones.
7. **Configuration is a feature.** Sensible defaults for zero-config, `.edr.toml` for teams. Never require config to get value.
