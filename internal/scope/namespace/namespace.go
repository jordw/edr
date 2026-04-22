// Package namespace builds the effective set of names visible in a
// single file — the files imports, same-module siblings, included
// headers, inherited members, and builtins, keyed by identifier name
// and carrying the DeclID each name could resolve to.
//
// The abstraction makes cross-file reference resolution language-
// independent at the algorithm level: rename, refs-to, callers, and
// go-to-definition all answer the same question ("in file F, what
// does name N refer to?") against Namespace. Per-language logic is
// confined to Populators, which know how to translate imports and
// package clauses into Entries.
//
// This package replaces the ad-hoc per-language supplements in
// internal/dispatch (same_package_go.go, same_crate_rust.go, etc.).
package namespace

import (
	"github.com/jordw/edr/internal/scope"
)

// Namespace is the effective set of names visible in File. For each
// name, Entries holds every DeclID the name could resolve to, given
// the files scope chain and imports. Names with multiple entries
// are ambiguous: consumers decide how to handle them (refuse, prompt,
// pick most-likely).
type Namespace struct {
	File    string
	Entries map[string][]Entry
}

// Entry describes one possible binding for a name in a file.
type Entry struct {
	// DeclID is the canonical identity of the declaration this name
	// could bind to. For matching against a rename target, consumers
	// compare DeclIDs directly — the scope builder produces the same
	// DeclID for a given logical declaration regardless of which file
	// references it (once canonical-path hashing lands in Phase 2).
	DeclID scope.DeclID

	// Source describes how the name entered this files namespace.
	// Lets consumers apply policy: e.g., a rename tool may refuse to
	// act on SourceBuiltin or SourceProbable entries.
	Source Source

	// File is the origin file of the declaration, or "" for builtins
	// and language-provided entries without a source file.
	File string
}

// Source tells the consumer how a name entered a files namespace.
type Source uint8

const (
	// SourceLocal — declared in this file at any scope.
	SourceLocal Source = iota + 1
	// SourceSamePackage — same Go package / Java package / Kotlin
	// package / Rust sibling mod. Visible without an import.
	SourceSamePackage
	// SourceImported — brought in by an import statement (Go import,
	// Java import, Python from-import, Rust use, TS import).
	SourceImported
	// SourceIncluded — included by preprocessor (C/C++ #include).
	SourceIncluded
	// SourceInherited — inherited from a supertype (class extension,
	// interface implementation, trait impl). Populated once the
	// hierarchy index lands.
	SourceInherited
	// SourceBuiltin — language prelude or builtin (Go len/make, Rust
	// Option/Result, Python print). Never a rename target.
	SourceBuiltin
	// SourceProbable — the exact binding could not be resolved
	// (dynamic import, reflection, eval). Listed as a best guess.
	// Consumers should treat these as low-confidence.
	SourceProbable
)

// Resolver is the dependency Populators use to look up other files
// scope results and resolve imports. Abstracts over the scope store
// (persistent) vs on-demand parsing so Populators can be tested with
// fakes.
type Resolver interface {
	// Result returns the parsed scope.Result for file, or nil if the
	// file cannot be parsed (missing, unsupported extension, syntax
	// error). Callers must tolerate nil.
	Result(file string) *scope.Result

	// FilesForImport returns the file paths an import specification
	// in importingFile could resolve to. Multi-valued because some
	// languages (C #include search paths, Python sys.path) can resolve
	// one spec to several candidates. Empty if unresolvable.
	FilesForImport(importSpec string, importingFile string) []string
}

// Populator adds entries to ns based on the contents of r and data
// retrievable via resolver. Each supported language implements one.
// Populators are responsible for:
//   - Walking r.Decls with Kind == scope.KindImport to find imports.
//   - Resolving each import to sibling files via resolver.
//   - Looking up exported decls in those sibling files and adding
//     them to ns.Entries with the appropriate Source.
//   - Adding language builtins where that is useful for filtering.
//
// Populators do NOT add SourceLocal entries — Build handles those
// uniformly. The Populator starts from a namespace that already
// contains every local decl.
type Populator func(ns *Namespace, r *scope.Result, resolver Resolver)

// Build constructs the effective namespace for a file. Every local
// decl (any scope, any kind except KindImport) is added as
// SourceLocal; import decls are not added by Build — the Populator
// translates them into SourceImported entries pointing at the
// imported files decls. Builtins, same-package siblings, and
// inherited members are also Populator-driven.
//
// If populator is nil, the returned Namespace contains only
// SourceLocal entries (useful for same-file rename, which does not
// need cross-file resolution).
func Build(file string, r *scope.Result, resolver Resolver, populator Populator) *Namespace {
	ns := &Namespace{
		File:    file,
		Entries: make(map[string][]Entry, 0),
	}
	if r != nil {
		for i := range r.Decls {
			d := &r.Decls[i]
			if d.Kind == scope.KindImport {
				// Import decls are Populator territory — they describe
				// a gateway to another files decls, not an entry in
				// their own right.
				continue
			}
			ns.Entries[d.Name] = append(ns.Entries[d.Name], Entry{
				DeclID: d.ID,
				Source: SourceLocal,
				File:   file,
			})
		}
	}
	if populator != nil {
		populator(ns, r, resolver)
	}
	return ns
}

// Matches reports whether any Entry for name has DeclID == target.
// Convenience for the common "does this ref bind to our rename
// target?" check.
func (ns *Namespace) Matches(name string, target scope.DeclID) bool {
	if ns == nil {
		return false
	}
	for _, e := range ns.Entries[name] {
		if e.DeclID == target {
			return true
		}
	}
	return false
}

// Lookup returns the entries for name, or nil if none.
func (ns *Namespace) Lookup(name string) []Entry {
	if ns == nil {
		return nil
	}
	return ns.Entries[name]
}
