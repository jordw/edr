# `edr rename` — what works, what doesn't

`edr rename` performs scope-aware identifier rewriting. It binds each
candidate ref to a decl, skips shadowed locals, and rewrites only the
identifier bytes of every ref that resolves to the rename target.
This page tells you which language patterns it handles cleanly, where
it falls back, and the cases it explicitly refuses.

## Coverage matrix

All twelve languages with a scope builder are admitted for the
scope-aware write path. Anything else falls back to the regex +
symbol-index path with `mode:"name-match"` and a warning.

| Language | Extensions | Mode | Cross-file | Notes |
|---|---|---|---|---|
| Go | `.go` | `scope` | yes | Same-package walker + namespace-driven cross-package; sibling-interface propagation (see hard blocker 3 below). |
| TypeScript / JavaScript | `.ts .tsx .js .jsx .mts .cts .mjs .cjs .d.ts` | `scope` | yes | ES modules + CommonJS destructure + tsconfig `paths`. |
| Python | `.py .pyi` | `scope` | yes | `from x import Y`, `import x.y`. Star imports fall through to the generic ref filter. |
| Java | `.java` | `scope` | yes | FQN-import + same-package siblings + supertype hierarchy. |
| Kotlin | `.kt .kts` | `scope` | yes | Same as Java; companion-object methods route through enclosing class. |
| Rust | `.rs` | `scope` | yes | `use` paths + `mod` resolution; aborts to regex when sibling files define same-name symbols (extractive ambiguity). |
| C | `.c .h` | `scope` | yes | `.h`/`.c` decl/def pair treated as one logical symbol. |
| C++ | `.cpp .cxx .cc .c++ .hpp .hxx .hh .h++` | `scope` | yes | Out-of-line method defs disambiguated via base-ident filter. |
| Ruby | `.rb` | `scope` | yes | `require_relative` + cross-class FP guard. |
| C# | `.cs` | `scope` | yes | `namespace { ... }` blocks. |
| Swift | `.swift` | `scope` | yes | No package clause; uses file-scope decls. |
| PHP | `.php .phtml` | `scope` | yes | Backslash-qualified names; `$compute` ref name is sigil-distinct from function `compute`. |
| Scala, Lua | `.scala .sc .lua` | `name-match` | partial | Symbol-indexed but no scope builder — regex fallback emits `mode:"name-match"` with a warning. |

## Result modes

The `mode` field on the rename JSON header tells you how the rewrite
was computed:

- **`scope`** — the scope builder resolved each ref by binding. Locals
  with the same name as the target are skipped. Cross-class same-name
  identifiers are filtered. **Trust the rewrite.**
- **`name-match`** — fallback path: regex over the symbol-index range.
  No shadow filtering. Cross-class same-name identifiers may be
  rewritten as false positives. **Verify the diff before committing.**
  A `warnings:[...]` entry names the extension that triggered the
  fallback.
- **`refused`** — the rename was not applied. The `warnings` field
  explains why. See "Refusal cases" below.

## Refusal cases

The dispatch layer refuses certain renames rather than producing
output that's likely wrong. Each refusal returns
`{"status":"refused", ...}` with a structured `warnings` array.

### Blast radius (`--cross-file` only)

> rename refused: --cross-file would edit N files and M occurrences (limits: 50 files, 200 occurrences). The name "X" likely collides with unrelated identifiers.

Triggers when the rewrite would touch more than 50 files OR 200
occurrences. Override with `--force` if you've inspected the diff and
confirmed the breadth is intentional.

### Unsupported language

For symbol-indexed languages without a scope builder (currently Scala,
Lua), `--cross-file` runs the regex fallback. The result carries
`mode:"name-match"` and a warning naming the extension. Not technically
a refusal — the rewrite proceeds — but treat it like one for
correctness purposes.

## Known gaps

These are cases the scope path cannot disambiguate extractively.
None of them are silent — but none are caught by the rename engine
either. **Run `--verify` for any cross-file method rename where the
receiver might satisfy an interface.**

### Stdlib / third-party interface conformance

Renaming a method whose name + signature satisfies an interface
declared in source we can't edit (Go's `io.Reader`, Java's
`Comparable`, third-party types via `gorm`, `testify`, etc.) silently
breaks compile because the interface decl can't be rewritten.

The rename engine has no way to verify this extractively (the type
info lives in the importer, not in the source). The canonical
recourse is **`--verify`**, which runs the project's build after the
rewrite and surfaces compile errors.

```sh
edr rename pkg/reader.go:Read --to ReadLine --cross-file --verify
```

We deliberately do **not** ship per-language stdlib catalogs as a
gate. They would cover only a subset of the surface area, give false
confidence on third-party interfaces, and add per-language maintenance
burden without approaching what `--verify` already provides.

### Same-package interface propagation (Go)

When you rename a method on a type that satisfies a SAME-PACKAGE Go
interface, the rename engine DOES propagate to the interface decl AND
to other types in the package that implement the same interface
(matched by method-name + arity). This was hard blocker 3.

Cross-package interfaces are NOT propagated this way — they live
through the namespace path, which only catches direct refs to the
target's canonical DeclID. If your method satisfies an interface
declared in a different package, treat that as the "third-party
interface" gap above and run `--verify`.

### Method chains

Receiver-type inference for chained calls like `a().b().Method()` is
not modeled. The scope path treats `Method` as a property access on
an unknown receiver and skips it (false-negative). The legacy
`name-match` path would rewrite all `Method` calls regardless of
receiver type (false-positive). Pick your tradeoff per-rename and
prefer `--verify`.

### Reflection-driven dispatch

Calls through `reflect.Value.MethodByName(...)` (Go) or equivalents in
other languages are invisible to extractive analysis. The rename
engine cannot find them; the build won't surface them either; you'd
discover at runtime. Grep your codebase for the old name as a string
literal before committing wide renames.

## Cross-file blast control

For renames you're confident in but that span many files, use
`--cross-file --force --dry-run` to preview the full diff first, then
re-run without `--dry-run`.

For renames you're NOT confident in:
1. `--dry-run` first
2. Verify the diff
3. `--verify` to run the build
4. If anything looks off, `edr undo`

## See also

- `internal/dispatch/dispatch_rename.go` — entry point + blast-radius gate
- `internal/dispatch/dispatch_rename_scope.go` — scope dispatcher per language
- `internal/dispatch/cross_file_*.go` — per-language cross-file handlers
- `scripts/eval/rename_correctness.sh` — 59-tuple compile-oracle eval
- `scripts/eval/rename_dogfood.sh` — at-scale random-sample correctness eval
- `scripts/eval/rename_fp.sh` — corpus-scale over-rewrite measurement
