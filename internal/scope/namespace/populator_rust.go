package namespace

import (
	"strings"

	"github.com/jordw/edr/internal/scope"
)

// rustUsePathFromSignature unpacks the Signature a Rust KindImport
// decl carries. The builder encodes `use crate::foo::Bar` as
// Name="Bar", Signature="crate::foo\x00Bar". Returns (modPath, item).
// For `use crate::foo::{Bar, Baz}`, each brace item becomes its own
// KindImport decl with the same modPath and its own item.
func rustUsePathFromSignature(sig string) (string, string) {
	i := strings.IndexByte(sig, 0)
	if i < 0 {
		return "", ""
	}
	return sig[:i], sig[i+1:]
}

// RustPopulator returns a Populator that resolves intra-crate `use`
// imports and adds their target decls as namespace entries with
// SourceImported. External crate imports are skipped — their DeclIDs
// live in a different crate graph that we don't walk for v1.
//
// What this does NOT do:
//   - Glob imports (`use foo::*`): the builder emits no usable
//     decl for the glob, so there's nothing to populate.
//   - Re-exports (`pub use foo::Bar`): treated like ordinary use;
//     rename propagation through re-exports isn't modeled.
//   - Alias imports (`use foo::Bar as Baz`): not covered by the
//     current builder signature format. Renamed-import support can
//     be added later by extending the signature encoding.
func RustPopulator(r *RustResolver) Populator {
	return func(ns *Namespace, sr *scope.Result, _ Resolver) {
		if sr == nil {
			return
		}
		for _, d := range sr.Decls {
			if d.Kind != scope.KindImport {
				continue
			}
			modPath, item := rustUsePathFromSignature(d.Signature)
			if modPath == "" || item == "" {
				continue
			}
			// Resolve the file(s) that export `item` at their
			// module level.
			files := r.FilesForImport(modPath+"::"+item, ns.File)
			for _, f := range files {
				impRes := r.Result(f)
				if impRes == nil {
					continue
				}
				for i := range impRes.Decls {
					id := &impRes.Decls[i]
					if id.Name != item {
						continue
					}
					if id.Kind == scope.KindImport {
						continue
					}
					// File-scope only — matching the Rust `pub`
					// visibility concern by scope, not by the Exported
					// flag (the builder doesn't populate Exported for
					// Rust).
					if id.Scope != scope.ScopeID(1) {
						continue
					}
					ns.Entries[id.Name] = append(ns.Entries[id.Name], Entry{
						DeclID: id.ID,
						Source: SourceImported,
						File:   f,
					})
				}
			}
		}
	}
}
