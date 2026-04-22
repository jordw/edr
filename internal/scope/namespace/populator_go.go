package namespace

import (
	"strings"

	"github.com/jordw/edr/internal/scope"
)

// goImportPathFromSignature extracts the import path from the
// Signature field of a Go KindImport decl. The Go scope builder packs
// it as "<importPath>\x00*" — see internal/scope/golang/builder.go
// handleImportPath.
func goImportPathFromSignature(sig string) string {
	if sig == "" {
		return ""
	}
	if i := strings.IndexByte(sig, 0); i >= 0 {
		return sig[:i]
	}
	return sig
}

// GoPopulator returns a Populator that resolves Go imports and same-
// package siblings, adding their exported decls as namespace entries.
//
// The returned Populator uses the GoResolver provided. Same-package
// siblings are added with SourceSamePackage, decls reached via import
// statements are added with SourceImported.
//
// Aliased imports DO work: `import f "github.com/x/output"` records
// the import as a KindImport decl named "f" with Signature
// "github.com/x/output\x00*". The cross-file rename path's import-
// gateway map (goImportGateways in cross_file_go.go) keys on the
// local name "f" and stores the import path; the property-access
// disambiguation accepts `f.Foo` when the path matches the target's
// package. Tested in TestGoPopulator_AliasedImport.
//
// What this Populator does NOT do (yet):
//   - Builtins (len, make, append, ...). Skipped because rename is
//     not a valid operation on them and refs to them are not in our
//     target's namespace anyway.
func GoPopulator(r *GoResolver) Populator {
	return func(ns *Namespace, sr *scope.Result, _ Resolver) {
		if sr == nil {
			return
		}
		// 1. Same-package siblings: every exported file-scope decl in a
		//    sibling .go file with the same package clause becomes a
		//    SourceSamePackage entry. Their DeclIDs match the targets
		//    DeclID when both are hashed canonically.
		for _, sib := range r.SamePackageFiles(ns.File) {
			sibRes := r.Result(sib)
			if sibRes == nil {
				continue
			}
			for i := range sibRes.Decls {
				d := &sibRes.Decls[i]
				if d.Kind == scope.KindImport || !d.Exported {
					continue
				}
				if d.Scope != scope.ScopeID(1) {
					continue // file-scope only
				}
				ns.Entries[d.Name] = append(ns.Entries[d.Name], Entry{
					DeclID: d.ID,
					Source: SourceSamePackage,
					File:   sib,
				})
			}
		}

		// 2. Imports: each KindImport decl in the importing file
		//    points at a Go import path. Resolve to the imported
		//    packages files and add their exported decls.
		for _, d := range sr.Decls {
			if d.Kind != scope.KindImport {
				continue
			}
			// The Go scope builder packs the import path into
			// Decl.Signature for KindImport as "<path>\x00*".
			importPath := goImportPathFromSignature(d.Signature)
			if importPath == "" {
				continue
			}
			files := r.FilesForImport(importPath, ns.File)
			for _, f := range files {
				impRes := r.Result(f)
				if impRes == nil {
					continue
				}
				for i := range impRes.Decls {
					id := &impRes.Decls[i]
					if id.Kind == scope.KindImport || !id.Exported {
						continue
					}
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
