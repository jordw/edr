package namespace

import (
	"strings"

	"github.com/jordw/edr/internal/scope"
)

// javaImportFromSignature decodes a Java KindImport decls Signature
// into (modulePath, originalName). Signature format is
// "<modulePath>\x00<originalName>" — see internal/scope/java/builder.go
// where it is emitted. originalName is "*" for star imports.
func javaImportFromSignature(sig string) (modulePath, originalName string) {
	if sig == "" {
		return "", ""
	}
	i := strings.IndexByte(sig, 0)
	if i < 0 {
		return sig, ""
	}
	return sig[:i], sig[i+1:]
}

// JavaPopulator returns a Populator that resolves Java imports and
// same-package siblings.
//
// Same-package siblings: every exported file-scope decl in another
// .java file that shares the importing files package becomes a
// SourceSamePackage entry.
//
// Imports: each KindImport decl carries a modulePath in its
// Signature. We resolve via the JavaResolver to a set of files and
// add each files exported file-scope decls as SourceImported. Star
// imports (`import foo.bar.*`) bring in every class in the package.
func JavaPopulator(r *JavaResolver) Populator {
	return func(ns *Namespace, sr *scope.Result, _ Resolver) {
		if sr == nil {
			return
		}

		// Same-package siblings.
		for _, sib := range r.SamePackageFiles(ns.File) {
			sibRes := r.Result(sib)
			if sibRes == nil {
				continue
			}
			for i := range sibRes.Decls {
				d := &sibRes.Decls[i]
				if d.Kind == scope.KindImport {
					continue
				}
				if d.Scope != scope.ScopeID(1) {
					continue
				}
				ns.Entries[d.Name] = append(ns.Entries[d.Name], Entry{
					DeclID: d.ID,
					Source: SourceSamePackage,
					File:   sib,
				})
			}
		}

		// Imports.
		for _, d := range sr.Decls {
			if d.Kind != scope.KindImport {
				continue
			}
			modulePath, originalName := javaImportFromSignature(d.Signature)
			if modulePath == "" {
				continue
			}
			// Build the importSpec the resolver expects: either
			// "<pkg>.<className>" for a single-class import or
			// "<pkg>.*" for a star import. modulePath is "<pkg>".
			var importSpec string
			if originalName == "*" {
				importSpec = modulePath + ".*"
			} else if originalName != "" {
				importSpec = modulePath + "." + originalName
			} else {
				importSpec = modulePath
			}
			files := r.FilesForImport(importSpec, ns.File)
			for _, f := range files {
				impRes := r.Result(f)
				if impRes == nil {
					continue
				}
				for i := range impRes.Decls {
					id := &impRes.Decls[i]
					if id.Kind == scope.KindImport {
						continue
					}
					if id.Scope != scope.ScopeID(1) {
						continue
					}
					// For a single-class import, only that class enters
					// the namespace under that name. For a star import,
					// all of the files file-scope decls do.
					if originalName != "" && originalName != "*" && id.Name != originalName {
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
