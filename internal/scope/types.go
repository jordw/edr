// Package scope defines the universal types for edr's cross-language
// scope and binding index.
//
// Language-specific builders (in internal/scope/<lang>/) produce Result
// values conforming to the types in this file. Consumers (rename,
// changesig, focus) treat the types uniformly regardless of source
// language. Per the extractive-vs-computational architectural ceiling,
// everything here is derivable from classified token streams plus
// bounded local analysis — no type inference, no whole-program
// propagation.
package scope

// Span is a byte range in a file's source.
type Span struct {
	StartByte uint32
	EndByte   uint32
}

// LocID identifies a syntactic occurrence at a specific (file, span).
// Location-bound: moving a file changes its LocIDs.
type LocID uint64

// DeclID identifies a logical declaration. Derived from canonical path
// (module/package/namespace), name, namespace, and signature — NOT
// file path — so it survives file renames and cross-file moves.
type DeclID uint64

// ScopeID identifies a lexical scope within a file. Indices are local
// to a file; to compare scopes across files, use (File, ScopeID).
type ScopeID uint32

// Namespace distinguishes identifier roles in languages with multiple
// namespaces (TS class+interface+namespace merging, Rust macros, Ruby
// constants). Builders use language-specific strings; consumers treat
// them opaquely.
type Namespace string

const (
	NSValue     Namespace = "value"
	NSType      Namespace = "type"
	NSNamespace Namespace = "namespace"
	NSMacro     Namespace = "macro"
	NSConstant  Namespace = "constant"
	// NSField is for struct/interface members (fields and methods as
	// declared within a type body). Field/method refs happen via
	// property access (obj.x), which scope resolution does not walk.
	// Putting them in their own namespace prevents shadowing of
	// same-name top-level decls during scope-chain resolution.
	NSField Namespace = "field"
)

// DeclKind classifies a declaration's syntactic role (for display and
// rename eligibility, not semantics). Builders emit language-appropriate
// strings.
type DeclKind string

const (
	KindFunction  DeclKind = "function"
	KindMethod    DeclKind = "method"
	KindClass     DeclKind = "class"
	KindInterface DeclKind = "interface"
	KindType      DeclKind = "type"
	KindEnum      DeclKind = "enum"
	KindVar       DeclKind = "var"
	KindLet       DeclKind = "let"
	KindConst     DeclKind = "const"
	KindParam     DeclKind = "param"
	KindField     DeclKind = "field"
	KindImport    DeclKind = "import"
	KindNamespace DeclKind = "namespace"
)

// ScopeKind classifies a scope's syntactic role.
type ScopeKind string

const (
	ScopeFile      ScopeKind = "file"
	ScopeFunction  ScopeKind = "function"
	ScopeBlock     ScopeKind = "block"
	ScopeClass     ScopeKind = "class"
	ScopeInterface ScopeKind = "interface"
	ScopeNamespace ScopeKind = "namespace"
	ScopeFor       ScopeKind = "for"
)

// Scope describes one lexical scope in a file.
type Scope struct {
	ID     ScopeID
	Parent ScopeID // zero means no parent (file scope)
	Kind   ScopeKind
	Span   Span
}

// Decl is a single declaration within a file. Multiple Decls can share a
// DeclID when they participate in declaration merging (TS class+interface
// with same name) or open-class reopening (Ruby, C# partial classes) —
// the merge is performed by a post-extraction reconciliation pass.
type Decl struct {
	ID        DeclID
	LocID     LocID
	Name      string
	Namespace Namespace
	Kind      DeclKind
	Scope     ScopeID
	File      string
	Span      Span // span of the declaring identifier
	FullSpan  Span // span of the full declaration (function body, class body, etc.)
	// Signature is opaque, populated when overloading matters. For
	// KindImport decls, the TS builder repurposes this field to carry
	// the module specifier + original name, encoded as
	// "<modulePath>\x00<originalName>" — consumed by the import-graph
	// resolver in internal/scope/store/imports.go. Callers that treat
	// Signature as a human-readable string should check Kind first.
	Signature string
	// Exported is true when a declaration is visible to importers
	// (TypeScript: prefixed with `export`; other languages may use
	// capitalization or other conventions — each builder decides).
	// The import-graph resolver only rewrites refs to exported decls.
	Exported bool
	// SuperTypes lists the class/interface/trait names this decl
	// extends or implements. Populated only for KindClass and
	// KindInterface decls in languages with explicit hierarchy
	// syntax (Java `extends`/`implements`, Kotlin `: Base, Iface`,
	// Rust `impl Trait for Struct` — the trait name). Enables the
	// Phase 8 hierarchy-aware rename: renaming a method on a class
	// that implements an interface also rewrites the interface's
	// same-name method declaration. Go's structural interfaces are
	// not populated here (they have no explicit syntax).
	SuperTypes []string
}

// BindingKind describes the confidence of a ref -> decl binding.
type BindingKind int

const (
	BindResolved   BindingKind = iota // exactly one decl
	BindAmbiguous                     // candidate set (overloads, structural match)
	BindProbable                      // heuristic match (dynamic dispatch w/o types)
	BindUnresolved                    // cannot resolve; Reason explains why
)

// RefBinding describes how a Ref binds to one or more Decls. Provenance
// (Reason) is populated on all kinds, not only Unresolved, to enable
// honest output and debugging.
type RefBinding struct {
	Kind       BindingKind
	Decl       DeclID   // set when Kind == BindResolved
	Candidates []DeclID // set when Kind == BindAmbiguous or BindProbable
	// Reason codes: "direct_scope" | "imported_export" |
	// "structural_method_match" | "dynamic_dispatch" | "adl" |
	// "macro" | "missing_import" | "duck_typed" | "eval" | "external" |
	// "inherited_field" (Java: one-level same-file supertype field lookup) |
	// "trait_method" (Rust: same-file trait-declared method reachable via
	// `self.X` inside `impl Trait for Type`) |
	// "import_export" (TS: ref to a local Import decl rewritten by the
	// import-graph resolver to target the exported decl in the source file)
	Reason string
}

// Ref is a single identifier occurrence that references (or might
// reference) a declaration.
type Ref struct {
	LocID     LocID
	File      string
	Span      Span
	Name      string
	Namespace Namespace
	Scope     ScopeID
	Binding   RefBinding
}

// Result is the full per-file extraction output. Builders produce one
// Result per parsed file; a merge pass may later reconcile Decls sharing
// a DeclID across files.
type Result struct {
	File   string
	Scopes []Scope
	Decls  []Decl
	Refs   []Ref
}

// RefsToDecl returns every Ref in the result whose Binding points (or
// could point) to the given DeclID. Includes Resolved, Ambiguous, and
// Probable kinds; a caller that wants only definite hits should filter
// to Kind == BindResolved.
func RefsToDecl(r *Result, id DeclID) []Ref {
	if r == nil || id == 0 {
		return nil
	}
	var out []Ref
	for _, ref := range r.Refs {
		b := ref.Binding
		if b.Decl == id && (b.Kind == BindResolved || b.Kind == BindProbable || b.Kind == BindAmbiguous) {
			out = append(out, ref)
			continue
		}
		for _, c := range b.Candidates {
			if c == id {
				out = append(out, ref)
				break
			}
		}
	}
	return out
}

// FindDeclByName returns the first Decl with the given name in the
// result's file-scope (ScopeID 1). For same-name local decls in nested
// scopes, use DeclsInScope or iterate r.Decls directly.
func FindDeclByName(r *Result, name string) *Decl {
	if r == nil {
		return nil
	}
	for i := range r.Decls {
		if r.Decls[i].Name == name && r.Decls[i].Scope == 1 {
			return &r.Decls[i]
		}
	}
	return nil
}
