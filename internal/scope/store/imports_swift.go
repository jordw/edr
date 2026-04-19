package store

import (
	"path/filepath"
	"strings"

	"github.com/jordw/edr/internal/scope"
)

// resolveImportsSwift is the Phase 1 import graph resolver for Swift.
//
// Swift's visibility model is module-level, not name-level: `import Foo`
// names a module; all of that module's public API becomes available
// unqualified inside the importer. edr has no notion of SwiftPM targets,
// so a v1 useful-everywhere implementation treats the WHOLE REPO as a
// single Swift module. Under that assumption, every public/internal
// top-level decl in any .swift file in the repo is visible unqualified
// from every other .swift file — exactly what Swift developers see when
// everything lives in one target.
//
// Concretely: for each Ref with Binding.Kind == BindUnresolved whose
// Name matches EXACTLY ONE exported file-scope decl in another .swift
// file, rewrite the ref to BindResolved with Reason="same_module".
//
// Scope (v1):
//   - Refs that already resolved locally (direct_scope, self_dot_field,
//     builtin, signature_scope, property_access) are NEVER touched.
//   - Only exported file-scope decls participate. A decl is "exported"
//     when Decl.Exported==true (set by the Swift builder: default
//     visibility + `public`/`internal`/`open` are exported, `private`/
//     `fileprivate` are not).
//   - Name ambiguity across the repo-module leaves the ref unresolved.
//     Two files exporting `Foo` at file scope are ambiguous by bare
//     name and we refuse to guess.
//   - Stdlib / SwiftPM-external modules (`Foundation`, `UIKit`, etc.)
//     are never "resolved" — the ref stays bound to the local Import
//     decl if one exists, or stays unresolved otherwise.
//   - Refs to members declared inside a type body (nested types,
//     fields, methods) are not cross-file-resolved; only file-scope
//     decls are indexed.
//
// Out of scope (v1): SwiftPM target boundaries (treating each
// `Sources/<Target>/` subtree as a separate module), `@testable import`
// special-casing, re-exports via `@_exported import`.
func resolveImportsSwift(parsed []parsedFile) {
	if len(parsed) == 0 {
		return
	}

	// Collect repo-wide exported file-scope decls, keyed by bare name.
	// Value is a slice so we can detect ambiguity (len > 1).
	exports := make(map[string][]scope.DeclID)
	anySwift := false
	for _, p := range parsed {
		if !isSwiftLike(p.rel) {
			continue
		}
		anySwift = true
		r := p.result
		for i := range r.Decls {
			d := &r.Decls[i]
			if !d.Exported {
				continue
			}
			if d.Kind == scope.KindImport {
				continue
			}
			if !isFileScopeDecl(r, d) {
				continue
			}
			exports[d.Name] = append(exports[d.Name], d.ID)
		}
	}
	if !anySwift || len(exports) == 0 {
		return
	}

	// Rewrite unresolved refs whose name has exactly one exported match
	// in the repo-module.
	for _, p := range parsed {
		if !isSwiftLike(p.rel) {
			continue
		}
		r := p.result
		for i := range r.Refs {
			ref := &r.Refs[i]
			if ref.Binding.Kind != scope.BindUnresolved {
				continue
			}
			cands, ok := exports[ref.Name]
			if !ok || len(cands) != 1 {
				// No match, or ambiguous. Leave as-is.
				continue
			}
			target := cands[0]
			// Don't rewrite a ref to a decl in the same file that
			// happens to have been missed by scope-chain resolution —
			// that would be a parser bug, not a cross-file resolution.
			// Preserving BindUnresolved in that case keeps the signal
			// honest.
			if isSameFileDecl(r, target) {
				continue
			}
			ref.Binding = scope.RefBinding{
				Kind:   scope.BindResolved,
				Decl:   target,
				Reason: "same_module",
			}
		}
	}
}

// isSwiftLike reports whether a repo-relative path is a Swift source
// file (case-insensitive on the extension).
func isSwiftLike(rel string) bool {
	return strings.EqualFold(filepath.Ext(rel), ".swift")
}

// isFileScopeDecl reports whether d lives at the file (top) scope of r.
// The file scope is the one whose Parent == 0.
func isFileScopeDecl(r *scope.Result, d *scope.Decl) bool {
	if d.Scope == 0 {
		return true
	}
	idx := int(d.Scope) - 1
	if idx < 0 || idx >= len(r.Scopes) {
		return false
	}
	return r.Scopes[idx].Parent == 0
}

// isSameFileDecl reports whether a DeclID belongs to r (checked by
// scanning r.Decls). Used to suppress self-matches in the repo-module
// name index, which would otherwise rewrite an unresolved same-file
// ref onto its own (evidently out-of-scope-chain) decl.
func isSameFileDecl(r *scope.Result, id scope.DeclID) bool {
	for i := range r.Decls {
		if r.Decls[i].ID == id {
			return true
		}
	}
	return false
}
