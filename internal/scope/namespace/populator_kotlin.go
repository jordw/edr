package namespace

import (
	"strings"

	"github.com/jordw/edr/internal/scope"
)

func kotlinImportFromSignature(sig string) (modulePath, originalName string) {
	if sig == "" {
		return "", ""
	}
	i := strings.IndexByte(sig, 0)
	if i < 0 {
		return sig, ""
	}
	return sig[:i], sig[i+1:]
}

// KotlinPopulator is the Kotlin twin of JavaPopulator. The Kotlin
// scope builder uses the same Signature format (\`<modulePath>\x00
// <origName>\`) for KindImport decls, so the same shape works.
//
// Kotlin specifics: no requirement that a top-level decls file
// match its name, so cross-file lookup may scan multiple files in
// the package directory.
func KotlinPopulator(r *KotlinResolver) Populator {
	return func(ns *Namespace, sr *scope.Result, _ Resolver) {
		if sr == nil {
			return
		}

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

		for _, d := range sr.Decls {
			if d.Kind != scope.KindImport {
				continue
			}
			modulePath, originalName := kotlinImportFromSignature(d.Signature)
			if modulePath == "" {
				continue
			}
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
