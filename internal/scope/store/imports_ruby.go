package store

// resolveImportsRuby is the Phase 1 cross-file import-graph resolver
// for Ruby. It is intentionally a no-op — Ruby's load semantics give
// the TS-style approach nothing to rewrite.
//
// # Why a no-op is correct for Ruby
//
// TypeScript's Phase-1 resolver works because `import { Foo } from
// './a'` does two things at once: it (a) loads a.ts and (b) binds the
// local name `Foo` to the thing named `Foo` exported from a.ts. The
// builder stamps a KindImport decl for `Foo` in the importing file;
// refs to `Foo` bind to that local import decl; the resolver then
// rewrites those bindings to point at a.ts's canonical `Foo` decl.
//
// Ruby's `require` / `require_relative` / `load` only do (a). They
// load the target file (and run it, interning its top-level constants
// and defs into the global / enclosing namespace), but they do NOT
// bind any local name. In source code, writing `require 'foo'` never
// introduces `foo` or any other identifier into the lexical scope
// where the require sits. The Ruby builder emits a KindImport decl
// named after the path string ("foo", "./foo/bar"), but that path
// string is NOT an identifier user code can reference — no Ref in a
// Ruby file can ever bind to such an import decl by name. There is
// therefore nothing to rewrite.
//
// # How cross-file linkage actually works in Ruby
//
// Ruby programs wire files together through the shared constant
// namespace, not through imports:
//
//   - `class Foo` in a.rb and `class Foo` in b.rb are the SAME class
//     (open-class reopening). Same for modules.
//   - Refs to `Foo` anywhere in the project resolve to that shared
//     class regardless of which file loaded first.
//
// This is handled by reconcileResults (internal/scope/store/reconcile.go),
// which unifies same-qualifier same-name KindClass / KindNamespace
// decls across files into a single canonical DeclID and rebinds every
// Ref pointing at a duplicate. That pass runs for Ruby already (see
// languageGroup + supportsCrossFileMerging). An import-graph resolver
// has nothing to add on top.
//
// # What about `autoload`, `include`, `extend`?
//
//   - `autoload :Foo, 'foo'` is the one construct that does bind a
//     name (`Foo`) to a specific file. The Ruby builder does not
//     currently recognize `autoload` specially — it flows through as
//     an ordinary method call. Adding autoload support would require
//     builder changes (emit a KindImport decl with Name=Foo and
//     Signature="<path>\x00Foo"), which is out of scope for this
//     Phase-1 pass. If the builder ever starts emitting such decls
//     with a proper Signature, the TS-style resolve loop could be
//     lifted here largely unchanged.
//
//   - `include SomeModule` / `extend SomeModule` / `prepend SomeModule`
//     emit `SomeModule` as an ordinary Ref. If SomeModule is defined
//     in another file, reconcileResults has already unified its DeclID,
//     so the per-file resolver in builder.resolveRefs will bind
//     correctly on its own. No import-graph intervention needed.
//
// If the Ruby builder is later extended to emit name-binding imports
// (e.g. via autoload, or via tracking which constants become visible
// from a particular require), this function is the right place to do
// the rewrite.
func resolveImportsRuby(parsed []parsedFile) {
	// Intentionally empty. See file-level doc for rationale.
}
