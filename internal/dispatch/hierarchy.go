package dispatch

import (
	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/scope"
)

// HierarchyDeps is the per-language data the hierarchy walker needs.
// Implementations adapt the language's resolver (JavaResolver,
// TSResolver, etc.) to the small surface required for cross-file
// supertype/subtype walks.
type HierarchyDeps interface {
	// Result returns the parsed scope.Result for `file`. The walker
	// tolerates nil — files that fail to parse are skipped.
	Result(file string) *scope.Result

	// SamePackageFiles returns sibling files that share the same
	// logical module / package as `file` (Java package, Kotlin
	// package, Python package directory, Ruby module nesting in
	// Zeitwerk-style projects, etc.). Empty if the language has no
	// same-package concept.
	SamePackageFiles(file string) []string

	// FilesForImport resolves an import specification appearing in
	// `importingFile` to the target file paths. Multiple paths may
	// be returned for languages with ambiguous resolution rules
	// (Python sys.path, C #include search). Empty when the spec is
	// external (stdlib / third-party / unresolved).
	FilesForImport(spec, importingFile string) []string

	// ImportSpec reconstructs the language-native import spec from
	// a KindImport decl. Convention varies: Java fuses module +
	// name (`foo.bar.Baz`); TS keeps the module spec separate and
	// the name in a destructuring binding (`./foo`). Each adapter
	// formats so its FilesForImport accepts the result.
	ImportSpec(d *scope.Decl) string
}

// EmitOverrideSpans walks the inheritance hierarchy reachable from
// the rename target's enclosing class and appends override-method
// identifier spans to `out`. Both directions are walked:
//
//   - Up (supers): for each class/interface name in the enclosing's
//     SuperTypes, find its same-name method in same-package or
//     imported files and emit its identifier span. Renaming
//     ServiceImpl.run also rewrites Service.run on the supertype.
//
//   - Down (subs): for each class in same-package, imported, or
//     extraCandidates files whose SuperTypes includes the
//     enclosing's name, emit its same-name method's identifier
//     span. Renaming Service.run on the supertype also rewrites
//     ServiceImpl.run on every subclass.
//
// extraCandidates is for languages that don't have a same-package
// concept (TS/JS) — callers pass in files derived from a reverse
// reference query (e.g. FindSemanticReferences on the enclosing
// class name) so the down-walk can find subclasses that import the
// target file. Pass nil when the language already populates
// candidates via SamePackageFiles.
//
// Constraints (intentional v1):
//   - Method matching is by name only. Signature-aware matching
//     (arity, types) belongs to a later pass; method-overloading
//     languages (Java/C++) may emit too many spans for now.
//   - Only one hop in each direction. Transitive walks (A → B → C)
//     are out of scope until repeat-traversal cycles are guarded.
//   - sym.Receiver must be populated — non-method targets short-
//     circuit. The Receiver field is stamped by the symbol index
//     based on the parser's notion of the method's owner.
func EmitOverrideSpans(out map[string][]span, deps HierarchyDeps, sym *index.SymbolInfo, extraCandidates ...string) {
	if sym.Receiver == "" {
		return
	}
	if deps == nil {
		return
	}

	targetRes := deps.Result(sym.File)
	if targetRes == nil {
		return
	}

	// Find the enclosing class/interface decl in the target file by
	// matching name + file-scope + class-like kind. Receiver names
	// are language-specific but the lookup convention is shared.
	encl := findEnclosingClass(targetRes, sym.Receiver)
	if encl == nil {
		return
	}

	// Candidate files: same-package siblings + anything reachable via
	// the target file's import graph + extra candidates supplied by
	// the caller (e.g. files that reference the enclosing class
	// name, used by TS where subclasses import the base).
	candidates := gatherHierarchyCandidates(deps, sym.File, targetRes)
	if len(extraCandidates) > 0 {
		seen := make(map[string]struct{}, len(candidates))
		for _, c := range candidates {
			seen[c] = struct{}{}
		}
		for _, c := range extraCandidates {
			if _, ok := seen[c]; ok {
				continue
			}
			seen[c] = struct{}{}
			candidates = append(candidates, c)
		}
	}

	// Track emitted (file, startByte, endByte) so duplicate
	// candidate paths don't double-emit the same span.
	seen := map[hierarchyKey]struct{}{}

	// Up walk: for each super name, find a class with that name in
	// candidates and emit its same-name method.
	for _, superName := range encl.SuperTypes {
		emitMatchingMethodInClass(out, deps, candidates, superName, sym.Name, seen)
	}

	// Down walk: for each candidate, find classes whose SuperTypes
	// list contains the enclosing's name and emit their same-name
	// method.
	for _, cand := range candidates {
		candRes := deps.Result(cand)
		if candRes == nil {
			continue
		}
		for i := range candRes.Decls {
			d := &candRes.Decls[i]
			if !isClassLikeFileScope(d) {
				continue
			}
			if !containsString(d.SuperTypes, encl.Name) {
				continue
			}
			emitMatchingMethodInClassDecl(out, cand, candRes, d, sym.Name, seen)
		}
	}
}

type hierarchyKey struct {
	file  string
	start uint32
	end   uint32
}

// findEnclosingClass returns the file-scope class/interface/enum
// decl whose name matches receiverName, or nil. The match relies on
// the convention that all class-like decls live at file scope
// (Scope == ScopeID(1) — the file's own scope, since 0 is the zero
// value for "no scope assigned").
//
// When multiple decls share the receiverName (e.g. Rust's
// `struct Foo` + synthetic `impl Trait for Foo` decl, or
// TS class+interface declaration merging), the lookup uses two
// tie-breakers in order:
//
//  1. Prefer a decl with non-empty SuperTypes. The up-walk has
//     somewhere to go; without supers there's no hierarchy edge
//     to follow. This matters for Rust where the struct decl
//     carries no SuperTypes and the synthetic impl-block decl
//     carries the trait.
//
//  2. If all candidates are empty-supers, pick by Kind precedence
//     (Class > Interface > Enum > Type > Namespace) — falling
//     back to first-encountered within the same kind. The choice
//     is mostly cosmetic when supers are empty (no walk fires
//     either way) but makes the result deterministic across
//     repeated runs and across the dual-emit pattern that some
//     languages use (TS class shadow into NSType, Rust struct
//     KindType + synthetic KindClass).
func findEnclosingClass(r *scope.Result, receiverName string) *scope.Decl {
	var supered, fallback *scope.Decl
	for i := range r.Decls {
		d := &r.Decls[i]
		if d.Name != receiverName {
			continue
		}
		if !isClassLikeFileScope(d) {
			continue
		}
		if len(d.SuperTypes) > 0 {
			if supered == nil || classKindRank(d.Kind) < classKindRank(supered.Kind) {
				supered = d
			}
			continue
		}
		if fallback == nil || classKindRank(d.Kind) < classKindRank(fallback.Kind) {
			fallback = d
		}
	}
	if supered != nil {
		return supered
	}
	return fallback
}

// classKindRank ranks class-like decl kinds for the deterministic
// tie-breaker in findEnclosingClass. Lower value = higher
// preference. Kinds outside the class-like set return a large
// sentinel so they never beat a real class-like decl.
func classKindRank(k scope.DeclKind) int {
	switch k {
	case scope.KindClass:
		return 0
	case scope.KindInterface:
		return 1
	case scope.KindEnum:
		return 2
	case scope.KindType:
		return 3
	case scope.KindNamespace:
		return 4
	}
	return 100
}

// isClassLikeFileScope reports whether d is a top-level class /
// interface / enum / record / module declaration. We accept anything
// that can host methods and participate in inheritance — per-
// language nuances (records, enums, traits, protocols, Ruby modules)
// all act as receiver types and can be referenced by SuperTypes.
func isClassLikeFileScope(d *scope.Decl) bool {
	if d.Scope != scope.ScopeID(1) {
		return false
	}
	switch d.Kind {
	case scope.KindClass, scope.KindInterface, scope.KindEnum, scope.KindType, scope.KindNamespace:
		return true
	}
	return false
}

// gatherHierarchyCandidates returns the deduped list of files to
// scan for hierarchy-related class decls: same-package siblings,
// import-resolvable files, plus the target file itself (so sibling
// classes in the same file get walked too — TS often co-locates a
// base class and its concrete subclass). Duplicate spans are
// guarded by the caller's seen-set.
func gatherHierarchyCandidates(deps HierarchyDeps, targetFile string, targetRes *scope.Result) []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(f string) {
		if _, ok := seen[f]; ok {
			return
		}
		seen[f] = struct{}{}
		out = append(out, f)
	}

	add(targetFile)

	for _, f := range deps.SamePackageFiles(targetFile) {
		add(f)
	}

	for i := range targetRes.Decls {
		d := &targetRes.Decls[i]
		if d.Kind != scope.KindImport {
			continue
		}
		spec := deps.ImportSpec(d)
		if spec == "" {
			continue
		}
		for _, f := range deps.FilesForImport(spec, targetFile) {
			add(f)
		}
	}
	return out
}

// SplitImportSignature parses the cross-language import-decl
// signature convention "<modulePath>\x00<origName>" into its parts.
// origName == "*" means a whole-module import. Returns ("", "") on
// malformed input. Adapters use this to build language-native
// import specs.
func SplitImportSignature(d *scope.Decl) (module, orig string) {
	sig := d.Signature
	if sig == "" {
		return "", ""
	}
	for i := 0; i < len(sig); i++ {
		if sig[i] == 0 {
			return sig[:i], sig[i+1:]
		}
	}
	return sig, ""
}

// emitMatchingMethodInClass searches every candidate file for a
// class whose Name matches className, then within that class emits
// the identifier span of any method whose Name matches methodName.
func emitMatchingMethodInClass(
	out map[string][]span,
	deps HierarchyDeps,
	candidates []string,
	className, methodName string,
	seen map[hierarchyKey]struct{},
) {
	for _, cand := range candidates {
		candRes := deps.Result(cand)
		if candRes == nil {
			continue
		}
		for i := range candRes.Decls {
			d := &candRes.Decls[i]
			if d.Name != className {
				continue
			}
			if !isClassLikeFileScope(d) {
				continue
			}
			emitMatchingMethodInClassDecl(out, cand, candRes, d, methodName, seen)
		}
	}
}

// emitMatchingMethodInClassDecl emits identifier spans for every
// method in classDecl's body whose name matches methodName, modulo
// previously-seen spans.
func emitMatchingMethodInClassDecl(
	out map[string][]span,
	file string,
	res *scope.Result,
	classDecl *scope.Decl,
	methodName string,
	seen map[hierarchyKey]struct{},
) {
	bodyEnd := classDecl.FullSpan.EndByte
	if bodyEnd == 0 {
		// FullSpan unset — fall back to the identifier span end,
		// which is at least non-zero. Methods inside a class
		// without FullSpan won't be matched, but the span check
		// also won't false-positive into other decls.
		bodyEnd = classDecl.Span.EndByte
	}
	for i := range res.Decls {
		m := &res.Decls[i]
		// Accept both KindMethod and KindFunction. The Python
		// builder (and likely future Lua/Ruby/etc.) emit methods
		// inside class bodies as KindFunction; the body-span
		// containment check below filters out free functions.
		if m.Kind != scope.KindMethod && m.Kind != scope.KindFunction {
			continue
		}
		if m.Name != methodName {
			continue
		}
		if m.Span.StartByte < classDecl.Span.StartByte || m.Span.EndByte > bodyEnd {
			continue
		}
		key := hierarchyKey{file: file, start: m.Span.StartByte, end: m.Span.EndByte}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out[file] = append(out[file], span{
			start: m.Span.StartByte,
			end:   m.Span.EndByte,
		})
	}
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
